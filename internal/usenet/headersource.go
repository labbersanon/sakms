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
