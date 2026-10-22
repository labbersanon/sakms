package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
	"github.com/labbersanon/sakms/internal/usenetsearch/predb"
)

// Claude 2026-10-15: PreDB runtime (HTTP lookup + optional IRC feed) for NNTP
// name recovery.
// Reason: the PreDB lives in the api layer's wiring (settings + secrets) so
//   usenetsearch never imports them. The IRC NickServ password is stored only
//   as secrets.Store ciphertext in KV and decrypted here, in memory, when the
//   client starts; it is never logged or returned by any endpoint.
// Troubleshooting: PreDB/IRC KV changes apply on restart or after the IRC
//   password PUT (Reload). No names recovered → o2cli grep "predb" and check
//   usenet_nntp_predb_http_enabled / usenet_nntp_predb_http_url.
// Review if: PreDB settings move into Apply/Config so Reload is automatic.

// NNTPPreDB owns the PreDB service wiring and the optional IRC goroutine.
type NNTPPreDB struct {
	sqlDB    *sql.DB
	settings *settings.Store
	secrets  *secrets.Store
	svc      *usenetsearch.Service
	parent   context.Context

	mu         sync.Mutex
	cancelIRC  context.CancelFunc
	cancelDump context.CancelFunc
}

// StartNNTPPreDB builds the runtime, wires the PreDB into svc, and starts the
// IRC feed when enabled. ctx bounds the IRC goroutine and the daily vacuum.
func StartNNTPPreDB(ctx context.Context, sqlDB *sql.DB, st *settings.Store, sec *secrets.Store, svc *usenetsearch.Service) *NNTPPreDB {
	p := &NNTPPreDB{sqlDB: sqlDB, settings: st, secrets: sec, svc: svc, parent: ctx}
	go p.vacuumLoop(p.parent)
	p.Reload(ctx)
	return p
}

// Reload re-reads the PreDB settings, rewires the lookup, and (re)starts or
// stops the IRC client.
func (p *NNTPPreDB) Reload(ctx context.Context) {
	g, err := loadNNTPGapFill(ctx, p.settings)
	if err != nil {
		log.Printf("usenetsearch: predb settings: %v", err)
		return
	}
	store := predb.NewStore(p.sqlDB)
	svc := &usenetsearch.PreDBService{Store: store}
	if g.PreDBHTTPEnabled {
		svc.HTTP = predb.NewHTTPClient(g.PreDBHTTPURL)
	}
	p.svc.SetPreDB(svc)
	if g.SRRDBEnabled {
		p.svc.SetSRRDB(usenetsearch.NewSRRDBClient(""))
	} else {
		p.svc.SetSRRDB(nil)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancelIRC != nil {
		p.cancelIRC()
		p.cancelIRC = nil
	}
	if p.cancelDump != nil {
		p.cancelDump()
		p.cancelDump = nil
	}
	path := strings.TrimSpace(g.PreDBDumpPath)
	// Claude 2026-10-18: never fetch a URL (api.predb.net ToS: don't build a DB).
	// if path := strings.TrimSpace(g.PreDBDumpPath); path != "" && !strings.Contains(path, "://") {
	if path != "" {
		if strings.Contains(path, "://") && !predb.IsFetchURL(path) {
			log.Printf("usenetsearch: predb dump path is not a dump file URL (search APIs are not imported)")
		} else {
			dumpCtx, dumpCancel := context.WithCancel(p.parent)
			p.cancelDump = dumpCancel
			go p.dumpLoop(dumpCtx, path)
		}
	}
	if !g.PreDBIRCEnabled {
		return
	}
	cfg, err := p.ircConfig(ctx)
	if err != nil {
		log.Printf("usenetsearch: predb irc config: %v", err)
		return
	}
	ircCtx, cancel := context.WithCancel(p.parent)
	p.cancelIRC = cancel
	go (&predb.IRCClient{Cfg: cfg, Store: store}).Run(ircCtx)
	// Claude 2026-10-19: second (and further) IRC networks besides synIRC.
	// Reason: dump is a snapshot; api.predb.net forbids building a local DB;
	//   Corrupt-net #pre is a live independent announce feed.
	// Troubleshooting: only one "predb irc connected" line → extra JSON failed
	//   or KV is "[]"; o2cli grep "predb irc extra".
	// Review if: DefaultExtraIRCFeeds changes.
	// Related files: irc.go ParseExtraIRCFeeds, service.go KeyPreDBIRCExtra
	for _, extra := range g.PreDBIRCExtra {
		if strings.EqualFold(extra.Server, cfg.Server) {
			continue
		}
		go (&predb.IRCClient{Cfg: extra, Store: store}).Run(ircCtx)
	}
}

// Claude 2026-10-18: empty server/channel/nick fall back to synIRC #PreNNTmux
// and a generated nick. NickServ password stays optional (usually unset).
// Reason: unregistered JOIN works; confirm mail does not.
// Troubleshooting: connected but parsed=0 → tagged TT parser, not JOIN.
// Review if: DefaultIRCChannel moves off #PreNNTmux.
func (p *NNTPPreDB) ircConfig(ctx context.Context) (predb.IRCConfig, error) {
	cfg := predb.IRCConfig{}
	get := func(key string) (string, error) {
		v, err := p.settings.Get(ctx, key)
		if err != nil && !isSettingsNotFound(err) {
			return "", err
		}
		return strings.TrimSpace(v), nil
	}
	var err error
	if cfg.Server, err = get(usenetsearch.KeyPreDBIRCServer); err != nil {
		return cfg, err
	}
	if cfg.Server == "" {
		cfg.Server = predb.DefaultIRCServer
	}
	if cfg.Nick, err = get(usenetsearch.KeyPreDBIRCNick); err != nil {
		return cfg, err
	}
	if cfg.Port, err = getSettingInt(ctx, p.settings, usenetsearch.KeyPreDBIRCPort, predb.DefaultIRCPort); err != nil {
		return cfg, err
	}
	if cfg.TLS, err = p.settings.GetBool(ctx, usenetsearch.KeyPreDBIRCTLS, true); err != nil {
		return cfg, err
	}
	chans, err := get(usenetsearch.KeyPreDBIRCChannels)
	if err != nil {
		return cfg, err
	}
	cfg.Channels = strings.FieldsFunc(chans, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })
	if len(cfg.Channels) == 0 {
		cfg.Channels = []string{predb.DefaultIRCChannel}
	}
	enc, err := get(usenetsearch.KeyPreDBIRCPasswordEnc)
	if err != nil {
		return cfg, err
	}
	if enc != "" {
		if cfg.Password, err = p.secrets.Decrypt(enc); err != nil {
			// Never include enc/err detail beyond the fact it failed.
			return cfg, errIRCPasswordUndecryptable
		}
	}
	return cfg, nil
}

var errIRCPasswordUndecryptable = errors.New("stored IRC password could not be decrypted (secret.key changed?); set it again")

const dumpPoll = 15 * time.Minute

// Claude 2026-10-22: daily 05:15 America/New_York PreDB vacuum (planner consensus).
// Reason: autovacuum defaults wait for ~1.44M dead; pg_stat resets on sakms-db
//
//	restart so a timer must not key off n_dead_tup. SKIP_LOCKED so this never
//	queues behind dump-import VACUUM. statement_timeout=0 is set inside
//	VacuumAnalyze. Do not VACUUM FULL. Do not raise shm here.
//
// Troubleshooting: o2cli grep "predb vacuum"; missing next= → this goroutine
//
//	not started. Review if: stop_grace_period on sakms-db is raised and
//	pg_stat survives deploy (then weekly is enough).
const (
	preDBVacuumHour = 5
	preDBVacuumMin  = 15
)

func preDBVacuumLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Local
	}
	return loc
}

func nextPreDBVacuum(now time.Time) time.Time {
	loc := preDBVacuumLocation()
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), preDBVacuumHour, preDBVacuumMin, 0, 0, loc)
	if !local.Before(next) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func (p *NNTPPreDB) vacuumLoop(ctx context.Context) {
	if p == nil || p.sqlDB == nil {
		return
	}
	store := predb.NewStore(p.sqlDB)
	for {
		next := nextPreDBVacuum(time.Now())
		log.Printf("usenetsearch: predb vacuum next=%s", next.Format(time.RFC3339))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			t0 := time.Now()
			err := store.VacuumAnalyze(ctx, true)
			elapsed := time.Since(t0).Milliseconds()
			if err != nil {
				log.Printf("usenetsearch: predb vacuum elapsed_ms=%d err=%v", elapsed, err)
				continue
			}
			log.Printf("usenetsearch: predb vacuum done elapsed_ms=%d skip_locked=true", elapsed)
		}
	}
}

// dumpLoop imports an operator-supplied nZEDb TSV (local path or dump-file URL)
// when the stamp changes. Search APIs are not fetched (api.predb.net ToS).
func (p *NNTPPreDB) dumpLoop(ctx context.Context, path string) {
	p.importDump(ctx, path)
	t := time.NewTicker(dumpPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.importDump(ctx, path)
		}
	}
}

func (p *NNTPPreDB) importDump(ctx context.Context, path string) {
	stamp, err := dumpStamp(ctx, path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("usenetsearch: predb dump path: %v", err)
		}
		return
	}
	prev, err := p.settings.Get(ctx, usenetsearch.KeyPreDBDumpImported)
	if err != nil && !isSettingsNotFound(err) {
		log.Printf("usenetsearch: predb dump stamp: %v", err)
		return
	}
	if prev == stamp {
		return
	}
	stats, err := predb.NewStore(p.sqlDB).ImportPath(ctx, path)
	if err != nil {
		log.Printf("usenetsearch: predb dump import failed: %v", err)
		return
	}
	if err := p.settings.Set(ctx, usenetsearch.KeyPreDBDumpImported, stamp); err != nil {
		log.Printf("usenetsearch: predb dump stamp save: %v", err)
	}
	log.Printf("usenetsearch: predb dump imported files=%d read=%d upserted=%d skipped=%d",
		stats.Files, stats.Read, stats.Upserted, stats.Skipped)
}

func dumpStamp(ctx context.Context, path string) (string, error) {
	if predb.IsFetchURL(path) {
		return predb.HeadStamp(ctx, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return fmt.Sprintf("%s|%d|%d", path, st.ModTime().Unix(), st.Size()), nil
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !predb.IsDumpFilename(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fp := filepath.Join(path, e.Name())
		parts = append(parts, fmt.Sprintf("%s|%d|%d", fp, info.ModTime().Unix(), info.Size()))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n"), nil
}

var (
	nntpPreDBMu sync.RWMutex
	nntpPreDB   *NNTPPreDB
)

// SetNNTPPreDB registers the PreDB runtime for the IRC-password endpoint
// (same process-wide pattern as SetNNTPNativeService). Pass nil to clear.
func SetNNTPPreDB(p *NNTPPreDB) {
	nntpPreDBMu.Lock()
	nntpPreDB = p
	nntpPreDBMu.Unlock()
}

func getNNTPPreDB() *NNTPPreDB {
	nntpPreDBMu.RLock()
	defer nntpPreDBMu.RUnlock()
	return nntpPreDB
}

// putUsenetNNTPPreDBIRCPasswordHandler stores (or clears, with an empty value)
// the IRC NickServ password as secrets.Store ciphertext and reloads the PreDB
// runtime. The response reports presence only.
func putUsenetNNTPPreDBIRCPasswordHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rt := getNNTPPreDB()
		if rt == nil {
			http.Error(w, "predb runtime not available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		enc := ""
		if req.Password != "" {
			var err error
			if enc, err = rt.secrets.Encrypt(req.Password); err != nil {
				http.Error(w, "could not encrypt password", http.StatusInternalServerError)
				return
			}
		}
		if err := settingsStore.Set(r.Context(), usenetsearch.KeyPreDBIRCPasswordEnc, enc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rt.Reload(r.Context())
		writeJSON(w, map[string]any{"ok": true, "passwordSet": enc != ""})
	}
}
