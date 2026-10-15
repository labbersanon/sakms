package usenet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tensai75/nntp"
)

// ErrBusy is returned by Overview when no pool connection is free. The crawler
// must yield rather than block ahead of segment downloads.
var ErrBusy = errors.New("usenet: no free connection")

// GroupRange is the GROUP response for one newsgroup.
type GroupRange struct {
	Number int
	Low    int
	High   int
}

// MessageOverview is one OVER/XOVER line, detached from the vendored type so
// internal/usenetsearch does not import Tensai75/nntp.
type MessageOverview struct {
	Number    int
	Subject   string
	From      string
	MessageID string
	Bytes     int
	DateUnix  int64 // 0 if unparseable
}

// HeaderSource is the read-only NNTP surface the native index crawler uses.
type HeaderSource interface {
	Capabilities(ctx context.Context) ([]string, error)
	GroupRange(ctx context.Context, group string) (GroupRange, error)
	Overview(ctx context.Context, group string, from, to int) ([]MessageOverview, error)
	// ListActive returns group names from LIST ACTIVE [wildmat]. wildmat empty
	// means the full active list (avoid on large providers — use media wildmats).
	ListActive(ctx context.Context, wildmat string) ([]string, error)
	// Claude 2026-10-15: STAT for continuous index liveness (delete only when gone).
	// Reason: age prune must not drop still-available headers; monitor STATs msgid.
	// Troubleshooting: ErrBusy → yield (tryOnly); never block download sockets.
	// Review if: liveness gets a dedicated connection budget separate from OVER.
	// Superseded by StatArticleAll below (single-pool STAT could false-delete
	// on one provider's lie); kept so this interface change stays additive.
	StatArticle(ctx context.Context, msgID string) (found bool, err error)
	// Claude 2026-10-15: all-pool tryOnly STAT for crawl-time admission + liveness.
	// Reason: StatArticle only asks ONE pool (first with a free slot) — a single
	//   provider's false 430 would wrongly skip/delete a release every other
	//   provider still serves. StatArticleAll asks every pool before deciding.
	// Troubleshooting: ErrBusy from every pool / a transport error → Inconclusive,
	//   never Missing — only a confirmed 430-everywhere or any 451 is Missing.
	// Review if: liveness and crawl admission want different pool-exhaustion policies.
	// Related files: usenetsearch/monitor.go LivenessOnce, usenetsearch/crawler.go
	StatArticleAll(ctx context.Context, msgID string) (StatVerdict, error)
}

// StatVerdict is the outcome of an all-pool StatArticleAll STAT.
type StatVerdict int

const (
	// StatPresent: at least one pool answered 223 (article exists there).
	StatPresent StatVerdict = iota
	// StatMissing: every pool answered 430/451, or any pool answered 451
	// (a removed/DMCA takedown — permanent regardless of other 430s).
	StatMissing
	// StatInconclusive: no pool said present, and at least one pool was busy
	// (ErrBusy) or had a transport/protocol error — never treat as missing.
	StatInconclusive
)

func (v StatVerdict) String() string {
	switch v {
	case StatPresent:
		return "present"
	case StatMissing:
		return "missing"
	case StatInconclusive:
		return "inconclusive"
	default:
		return "unknown"
	}
}

// HeaderSource returns a HeaderSource bound to this Manager's pools, or nil
// when no subscriptions are configured.
func (m *Manager) HeaderSource() HeaderSource {
	if m == nil || !m.HasSubscriptions() {
		return nil
	}
	return &managerHeaderSource{m: m}
}

type managerHeaderSource struct {
	m *Manager
}

func (h *managerHeaderSource) Capabilities(ctx context.Context) ([]string, error) {
	c, p, err := h.borrow(ctx, false)
	if err != nil {
		return nil, err
	}
	caps, err := c.Capabilities()
	if err != nil {
		p.put(c, false)
		return nil, err
	}
	p.put(c, true)
	return caps, nil
}

func (h *managerHeaderSource) GroupRange(ctx context.Context, group string) (GroupRange, error) {
	c, p, err := h.borrow(ctx, false)
	if err != nil {
		return GroupRange{}, err
	}
	number, low, high, err := c.Group(group)
	if err != nil {
		p.put(c, false)
		return GroupRange{}, err
	}
	p.put(c, true)
	return GroupRange{Number: number, Low: low, High: high}, nil
}

func (h *managerHeaderSource) Overview(ctx context.Context, group string, from, to int) ([]MessageOverview, error) {
	c, p, err := h.borrow(ctx, true)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := c.Group(group); err != nil {
		p.put(c, false)
		return nil, fmt.Errorf("usenet: GROUP %s: %w", group, err)
	}
	rows, err := c.Overview(from, to)
	if err != nil {
		p.put(c, false)
		return nil, err
	}
	p.put(c, true)
	out := make([]MessageOverview, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapOverview(r))
	}
	return out, nil
}

func (h *managerHeaderSource) ListActive(ctx context.Context, wildmat string) ([]string, error) {
	c, p, err := h.borrow(ctx, false)
	if err != nil {
		return nil, err
	}
	var lines []string
	if strings.TrimSpace(wildmat) == "" {
		lines, err = c.List("ACTIVE")
	} else {
		lines, err = c.List("ACTIVE", wildmat)
	}
	if err != nil {
		p.put(c, false)
		return nil, err
	}
	p.put(c, true)
	out := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		name := ParseListActiveName(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// StatArticle STATs msgID with tryOnly borrow (never waits on download sockets).
// found=true on 223; found=false on 430/451; ErrBusy when every pool is busy;
// other errors are transport/protocol failures (do not treat as missing).
func (h *managerHeaderSource) StatArticle(ctx context.Context, msgID string) (bool, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return false, errors.New("usenet: empty message-id")
	}
	c, p, err := h.borrow(ctx, true)
	if err != nil {
		return false, err
	}
	disarm := armNNTPDeadline(c)
	_, _, serr := c.Stat(ensureAngleMsgID(msgID))
	mapped := mapNNTPError(serr)
	missing := errors.Is(mapped, ErrArticleNotFound) || errors.Is(mapped, ErrArticleRemoved)
	keep := mapped == nil || missing
	if keep {
		disarm()
	}
	p.put(c, keep)
	if keep {
		return !missing, nil
	}
	return false, mapped
}

// StatArticleAll STATs msgID across EVERY pool with a tryOnly borrow — never
// waits on a download socket (ErrBusy counts as inconclusive, not missing).
// Present short-circuits on the first 223. Otherwise: any 451 (removed) with
// no other pool problem is Missing; any busy/transport problem is
// Inconclusive (takes priority over a plain 430 so provider load can never
// manifest as a false index deletion); all-430 with no problems is Missing.
//
// Claude 2026-10-15: mirrors Manager.statArticleAny's priority (otherErr wins
// over sawRemoved) but with tryGet (never blocks) instead of getCtx/retries —
// this runs on the crawl/liveness path, which must yield to downloads, not
// queue behind them.
// Troubleshooting: StatArticleAll_test.go covers present/all-430/451/busy/transport.
// Review if: a dedicated STAT connection budget replaces tryOnly entirely.
func (h *managerHeaderSource) StatArticleAll(ctx context.Context, msgID string) (StatVerdict, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return StatInconclusive, errors.New("usenet: empty message-id")
	}
	pools := h.m.currentPools()
	if len(pools) == 0 {
		return StatInconclusive, errors.New("usenet: no subscriptions")
	}
	sawRemoved := false
	var otherErr error
	for _, p := range pools {
		if ctx.Err() != nil {
			return StatInconclusive, ctx.Err()
		}
		c, err := p.tryGet()
		if err != nil {
			// Busy or dial failure — this pool couldn't answer either way.
			if otherErr == nil {
				otherErr = err
			}
			continue
		}
		disarm := armNNTPDeadline(c)
		_, _, serr := c.Stat(ensureAngleMsgID(msgID))
		mapped := mapNNTPError(serr)
		missing := errors.Is(mapped, ErrArticleNotFound) || errors.Is(mapped, ErrArticleRemoved)
		keep := mapped == nil || missing
		if keep {
			disarm()
		}
		p.put(c, keep)
		switch {
		case mapped == nil:
			return StatPresent, nil
		case errors.Is(mapped, ErrArticleRemoved):
			sawRemoved = true
		case errors.Is(mapped, ErrArticleNotFound):
			// Not here — keep checking the remaining pools.
		default:
			if otherErr == nil {
				otherErr = mapped
			}
		}
	}
	if otherErr != nil {
		return StatInconclusive, otherErr
	}
	if sawRemoved {
		return StatMissing, nil
	}
	return StatMissing, nil
}

// ParseListActiveName extracts the group name from one LIST ACTIVE line
// ("name high low status"). Returns "" for blank/malformed lines.
func ParseListActiveName(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func mapOverview(r nntp.MessageOverview) MessageOverview {
	var unix int64
	if !r.Date.IsZero() {
		unix = r.Date.Unix()
	}
	return MessageOverview{
		Number:    r.MessageNumber,
		Subject:   r.Subject,
		From:      r.From,
		MessageID: strings.Trim(r.MessageId, "<>"),
		Bytes:     r.Bytes,
		DateUnix:  unix,
	}
}

// borrow checks out one connection. When tryOnly, returns ErrBusy instead of
// waiting when every live slot is held by a download.
func (h *managerHeaderSource) borrow(ctx context.Context, tryOnly bool) (*nntp.Conn, *pool, error) {
	pools := h.m.currentPools()
	if len(pools) == 0 {
		return nil, nil, errors.New("usenet: no subscriptions")
	}
	var lastErr error
	for _, p := range pools {
		var (
			c   *nntp.Conn
			err error
		)
		if tryOnly {
			c, err = p.tryGet()
			if errors.Is(err, ErrBusy) {
				lastErr = ErrBusy
				continue
			}
		} else {
			c, err = p.getCtx(ctx)
		}
		if err != nil {
			lastErr = err
			continue
		}
		return c, p, nil
	}
	if lastErr == nil {
		lastErr = ErrBusy
	}
	return nil, nil, lastErr
}
