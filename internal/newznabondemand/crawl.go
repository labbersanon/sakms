package newznabondemand

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/usenet"
)

const (
	getInterval  = time.Second
	idleRecheck  = time.Minute
	rateLimitMin = 5 * time.Second
	rateLimitMax = 10 * time.Minute
	quotaBackoff = time.Hour
	errorBackoff = 30 * time.Second
)

var catalogServices = []string{"newznab", "newznab2"}

// Run fetches NZB XML for catalog rows, keeps streamable ones, then STATs
// with at most OnDemandStatWorkers Usenet sockets. After verify it maps
// IMDb/TVDB onto catalog.tmdb_id (finder). It does not scrape HTML and
// does not store enclosure URLs.
func Run(ctx context.Context, store *Store, conns *connections.Store, nntp *usenet.Manager, dataDir string, httpClient *http.Client, finder IMDBFinder) {
	if store == nil || conns == nil {
		return
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := &newznab.Client{HTTP: httpClient, Interval: getInterval}
	backoff := rateLimitMin
	for {
		if ctx.Err() != nil {
			return
		}
		idxs := indexersFromStore(ctx, conns)
		if len(idxs) == 0 {
			if !sleepCtx(ctx, idleRecheck) {
				return
			}
			continue
		}
		worked, err := step(ctx, store, client, idxs, nntp, dataDir, finder)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isQuota(err) {
				log.Printf("newznab ondemand: quota backoff=%s", quotaBackoff)
				if !sleepCtx(ctx, quotaBackoff) {
					return
				}
				backoff = rateLimitMin
				continue
			}
			if isRateLimit(err) {
				wait := backoff
				if ra := newznab.RetryAfter(err); ra > wait {
					wait = ra
				}
				if wait > rateLimitMax {
					wait = rateLimitMax
				}
				log.Printf("newznab ondemand: rate-limit backoff=%s", wait)
				if !sleepCtx(ctx, wait) {
					return
				}
				if backoff < rateLimitMax/2 {
					backoff *= 2
				}
				continue
			}
			log.Printf("newznab ondemand: step err=%v", err)
			if !sleepCtx(ctx, errorBackoff) {
				return
			}
			continue
		}
		backoff = rateLimitMin
		if !worked {
			if !sleepCtx(ctx, idleRecheck) {
				return
			}
		}
	}
}

func step(ctx context.Context, store *Store, client *newznab.Client, idxs []newznab.Indexer, nntp *usenet.Manager, dataDir string, finder IMDBFinder) (bool, error) {
	// Claude 2026-10-23: TMDB backfill before STAT. A 30k-seg STAT would hide
	//   already-verified titles for a long time; Find-by-IMDb is one HTTP call.
	// Troubleshooting: letter tiles after deploy → NextFetched is starving this.
	// Review if: Play starts-while-downloading.
	row, ok, err := store.NextVerifiedNeedingTMDB(ctx)
	if err != nil {
		return false, err
	}
	if ok {
		err := resolveCatalogTMDB(ctx, store, finder, row)
		if !errors.Is(err, ErrTMDBUnset) {
			return true, err
		}
	}
	row, ok, err = store.NextFetched(ctx)
	if err != nil {
		return false, err
	}
	if ok {
		inconc, err := statRow(ctx, store, nntp, dataDir, row, finder)
		if err != nil || !inconc {
			return true, err
		}
		// Inconclusive STAT must not HOL-block t=get on the same fetched row.
	}
	return fetchNext(ctx, store, client, idxs, dataDir)
}

func fetchNext(ctx context.Context, store *Store, client *newznab.Client, idxs []newznab.Indexer, dataDir string) (bool, error) {
	ref, ok, err := store.NextFromCatalog(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	idx, ok := indexerNamed(idxs, ref.Indexer)
	if !ok {
		now := time.Now().Unix()
		return true, store.Save(ctx, Row{
			Indexer: ref.Indexer, GUID: ref.GUID, Kind: ref.Kind, Title: ref.Title,
			Status: StatusError, RejectReason: "indexer unset", UpdatedAt: now,
		})
	}
	return true, fetchRow(ctx, store, client, idx, dataDir, ref)
}

func fetchRow(ctx context.Context, store *Store, client *newznab.Client, idx newznab.Indexer, dataDir string, ref CatalogRef) error {
	nzb, data, err := client.GetNZB(ctx, idx, ref.GUID)
	if err != nil {
		if isQuota(err) || isRateLimit(err) {
			return err
		}
		reason := strings.TrimSpace(err.Error())
		if looksLikeURL(reason) {
			reason = "get failed"
		}
		if len(reason) > 200 {
			reason = reason[:200]
		}
		log.Printf("newznab ondemand: get-fail indexer=%s kind=%s", idx.Name, ref.Kind)
		return store.Save(ctx, Row{
			Indexer: ref.Indexer, GUID: ref.GUID, Kind: ref.Kind, Title: ref.Title,
			Status: StatusError, RejectReason: reason, UpdatedAt: time.Now().Unix(),
		})
	}
	rel, err := writeNZB(dataDir, ref.Indexer, ref.GUID, data)
	if err != nil {
		return err
	}
	a := usenet.AssessNZB(nzb)
	now := time.Now().Unix()
	row := Row{
		Indexer:         ref.Indexer,
		GUID:            ref.GUID,
		Kind:            ref.Kind,
		Title:           ref.Title,
		Streamable:      a.OK,
		RejectReason:    a.Reason,
		NZBRelPath:      rel,
		PayloadFiles:    a.Files,
		PayloadSegments: a.Segments,
		Plan:            a.Plan,
		UpdatedAt:       now,
	}
	if !a.OK {
		row.Status = StatusXMLRejected
		log.Printf("newznab ondemand: xml-reject indexer=%s kind=%s reason=%s", ref.Indexer, ref.Kind, a.Reason)
		return store.Save(ctx, row)
	}
	if !usenet.StatFitsDeadline(a.Segments) {
		return skipStatBudget(ctx, store, row)
	}
	row.Status = StatusFetched
	log.Printf("newznab ondemand: fetched indexer=%s kind=%s files=%d segs=%d", ref.Indexer, ref.Kind, a.Files, a.Segments)
	return store.Save(ctx, row)
}

func skipStatBudget(ctx context.Context, store *Store, row Row) error {
	row.Status = StatusStatSkipped
	row.RejectReason = "stat budget exceeded"
	row.UpdatedAt = time.Now().Unix()
	log.Printf("newznab ondemand: skip-stat indexer=%s kind=%s segs=%d", row.Indexer, row.Kind, row.PayloadSegments)
	return store.Save(ctx, row)
}

// statRow STATs one fetched NZB. inconclusive means the caller should t=get
// another catalog row instead of retrying this guid immediately.
func statRow(ctx context.Context, store *Store, nntp *usenet.Manager, dataDir string, row Row, finder IMDBFinder) (inconclusive bool, err error) {
	if !usenet.StatFitsDeadline(row.PayloadSegments) {
		return false, skipStatBudget(ctx, store, row)
	}
	nzb, err := loadNZB(dataDir, row.NZBRelPath)
	if err != nil {
		row.Status = StatusError
		row.RejectReason = "nzb cache missing"
		row.UpdatedAt = time.Now().Unix()
		return false, store.Save(ctx, row)
	}
	res, err := nntp.StatPayload(ctx, nzb, usenet.OnDemandStatWorkers)
	now := time.Now().Unix()
	row.StatChecked = res.Checked
	row.StatMissing = res.Missing
	row.UpdatedAt = now
	if res.Inconclusive && err == nil {
		_ = store.Save(ctx, row)
		log.Printf("newznab ondemand: stat inconclusive indexer=%s checked=%d", row.Indexer, res.Checked)
		return true, nil
	}
	if err != nil {
		row.Status = StatusDead
		row.RejectReason = "articles missing"
		log.Printf("newznab ondemand: dead indexer=%s missing=%d/%d worst=%q",
			row.Indexer, res.Missing, res.Checked, res.WorstFile)
		return false, store.Save(ctx, row)
	}
	row.Status = StatusVerified
	row.VerifiedAt = now
	row.RejectReason = ""
	log.Printf("newznab ondemand: verified indexer=%s kind=%s checked=%d", row.Indexer, row.Kind, res.Checked)
	if err := store.Save(ctx, row); err != nil {
		return false, err
	}
	if err := resolveCatalogTMDB(ctx, store, finder, row); err != nil && !errors.Is(err, ErrTMDBUnset) {
		return false, err
	}
	return false, nil
}

func writeNZB(dataDir, indexer, guid string, data []byte) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("newznab ondemand: empty data dir")
	}
	sum := sha256.Sum256([]byte(guid))
	rel := filepath.Join("ondemand", "nzb", indexer, hex.EncodeToString(sum[:])+".nzb")
	abs := filepath.Join(dataDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o600); err != nil {
		return "", err
	}
	return rel, nil
}

func loadNZB(dataDir, rel string) (*usenet.NZB, error) {
	if rel == "" || strings.Contains(rel, "..") {
		return nil, errors.New("newznab ondemand: bad nzb path")
	}
	data, err := os.ReadFile(filepath.Join(dataDir, rel))
	if err != nil {
		return nil, err
	}
	return usenet.ParseNZB(data)
}

func indexerNamed(idxs []newznab.Indexer, name string) (newznab.Indexer, bool) {
	for _, idx := range idxs {
		if idx.Name == name {
			return idx, true
		}
	}
	return newznab.Indexer{}, false
}

func indexersFromStore(ctx context.Context, store *connections.Store) []newznab.Indexer {
	if store == nil {
		return nil
	}
	var out []newznab.Indexer
	for _, svc := range catalogServices {
		conn, err := store.Get(ctx, svc)
		if errors.Is(err, connections.ErrNotFound) || conn == nil {
			continue
		}
		if err != nil {
			log.Printf("newznab ondemand: %s err=%v", svc, err)
			continue
		}
		url := strings.TrimSpace(conn.URL)
		key := strings.TrimSpace(conn.APIKey)
		if url == "" || key == "" {
			continue
		}
		out = append(out, newznab.Indexer{Name: svc, Endpoint: url, APIKey: key})
	}
	return out
}

func isQuota(err error) bool {
	var ae newznab.APIError
	if errors.As(err, &ae) && ae.Quota() {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "request limit reached")
}

func isRateLimit(err error) bool {
	if err == nil || isQuota(err) {
		return false
	}
	var se *newznab.HTTPStatusError
	if errors.As(err, &se) && se.Status == http.StatusTooManyRequests {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "too many request")
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
