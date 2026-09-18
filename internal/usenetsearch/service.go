package usenetsearch

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Settings keys (KV store).
const (
	KeyNativeEnabled   = "usenet_nntp_native_enabled"
	KeyMoviesEnabled   = "movies_nntp_native_enabled"
	KeySeriesEnabled   = "series_nntp_native_enabled"
	KeyAdultEnabled    = "adult_nntp_native_enabled"
	KeyGroups          = "usenet_nntp_groups"
	KeyIndexDir        = "usenet_nntp_index_dir"
	KeyIndexMaxGB      = "usenet_nntp_index_max_gb"
	KeyWindowDays      = "usenet_nntp_index_window_days"
	KeyCrawlInterval   = "usenet_nntp_crawl_interval_seconds"
	KeyProbeState      = "usenet_nntp_probe_state"
	KeyProbeDetail     = "usenet_nntp_probe_detail"
	DefaultIndexMaxGB  = 20
	DefaultWindowDays  = 14
)

// Config is a snapshot of operator settings for the native backend.
type Config struct {
	Enabled       bool
	Movies        bool
	Series        bool
	Adult         bool
	Groups        []string
	IndexDir      string
	IndexMaxGB    int
	WindowDays    int
	CrawlInterval time.Duration
}

// ModeEnabled reports whether native search is on for the given mode name
// ("movies"|"series"|"adult").
func (c Config) ModeEnabled(mode string) bool {
	if !c.Enabled {
		return false
	}
	switch mode {
	case "movies":
		return c.Movies
	case "series":
		return c.Series
	case "adult":
		return c.Adult
	default:
		return false
	}
}

// ParseGroups splits a newline/comma-separated group list.
func ParseGroups(raw string) []string {
	raw = strings.ReplaceAll(raw, ",", "\n")
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		g := strings.TrimSpace(line)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out
}

// ValidateIndexDir is retained for settings API compatibility. The index now
// lives in Postgres (migration 0026); a path is never required.
//
// Claude 2026-09-18: no longer fail-closed on blank path.
// Reason: modernc SQLite banned from cmd/sakms; index moved to app DB.
func ValidateIndexDir(dir string) string {
	_ = dir
	return ""
}

// Service owns the index, crawler loop, and probe state.
type Service struct {
	mu     sync.Mutex
	cfg    Config
	src    usenet.HeaderSource
	sqlDB  *sql.DB
	idx    *Index
	cancel context.CancelFunc
	probe  Readiness
}

// NewService constructs a stopped Service. Call Apply to open the index / start crawl.
func NewService(src usenet.HeaderSource, sqlDB *sql.DB) *Service {
	return &Service{src: src, sqlDB: sqlDB, probe: Readiness{State: "unknown"}}
}

// SetHeaderSource updates the NNTP source (e.g. after SetSubscriptions).
func (s *Service) SetHeaderSource(src usenet.HeaderSource) {
	s.mu.Lock()
	s.src = src
	s.mu.Unlock()
}

// Apply reconfigures from cfg. Safe to call repeatedly.
func (s *Service) Apply(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.idx != nil {
		_ = s.idx.Close()
		s.idx = nil
	}
	s.cfg = cfg
	if !cfg.Enabled {
		s.probe = Readiness{Ready: false, State: "unknown", Detail: "native backend disabled"}
		return nil
	}
	if len(cfg.Groups) == 0 {
		s.probe = Readiness{Ready: false, State: "degraded", Detail: "no groups configured"}
		return nil
	}
	if s.sqlDB == nil {
		s.probe = Readiness{Ready: false, State: "degraded", Detail: "database not configured"}
		return nil
	}
	idx, err := OpenIndex(s.sqlDB)
	if err != nil {
		s.probe = Readiness{Ready: false, State: "degraded", Detail: err.Error()}
		return err
	}
	s.idx = idx
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.loop(ctx)
	return nil
}

func (s *Service) loop(ctx context.Context) {
	s.runProbeAndCrawl(ctx)
	interval := s.cfg.CrawlInterval
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runProbeAndCrawl(ctx)
		}
	}
}

func (s *Service) runProbeAndCrawl(ctx context.Context) {
	s.mu.Lock()
	src := s.src
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	r := Probe(ctx, src, cfg.Groups)
	s.mu.Lock()
	s.probe = r
	s.mu.Unlock()
	if !r.Ready || idx == nil || src == nil {
		return
	}
	window := time.Duration(cfg.WindowDays) * 24 * time.Hour
	if cfg.WindowDays <= 0 {
		window = DefaultWindowDays * 24 * time.Hour
	}
	maxGB := cfg.IndexMaxGB
	if maxGB <= 0 {
		maxGB = DefaultIndexMaxGB
	}
	c := &Crawler{Src: src, Index: idx, Groups: cfg.Groups, Window: window, MaxGB: maxGB}
	if err := c.CrawlOnce(ctx); err != nil {
		log.Printf("usenetsearch: crawl: %v", err)
	}
}

// Ready implements Backend.
func (s *Service) Ready(ctx context.Context) (Readiness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.probe
	if !s.cfg.Enabled {
		return Readiness{Ready: false, State: "unknown", Detail: "disabled"}, nil
	}
	if len(s.cfg.Groups) == 0 {
		return Readiness{Ready: false, State: "degraded", Detail: "no groups"}, nil
	}
	if s.idx == nil {
		return Readiness{Ready: false, State: "degraded", Detail: "index not open"}, nil
	}
	empty, err := s.idx.Empty(ctx)
	if err != nil {
		return Readiness{Ready: false, State: "degraded", Detail: err.Error()}, nil
	}
	if empty {
		return Readiness{Ready: false, State: "degraded", Detail: "index empty"}, nil
	}
	if r.State == "" {
		r.State = "unknown"
	}
	if r.State != "ok" {
		r.Ready = false
		return r, nil
	}
	r.Ready = true
	return r, nil
}

// Config returns a copy of the current config.
func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// ProbeSnapshot returns the last probe result.
func (s *Service) ProbeSnapshot() Readiness {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probe
}

// ResolveLocator loads a Candidate from the index for a sakms-nntp: URL.
func (s *Service) ResolveLocator(ctx context.Context, raw string) (Candidate, error) {
	parts, ok, err := DecodeLocator(raw)
	if err != nil {
		return Candidate{}, err
	}
	if !ok {
		return Candidate{}, ErrNotLocator
	}
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return Candidate{}, ErrIndexClosed
	}
	return idx.LoadBySeed(ctx, parts.Group, parts.SeedMsgID)
}

// Close stops the crawler and closes the index.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.idx != nil {
		_ = s.idx.Close()
		s.idx = nil
	}
}

var (
	ErrNotLocator  = errString("usenetsearch: not a sakms-nntp locator")
	ErrIndexClosed = errString("usenetsearch: index not open")
)

type errString string

func (e errString) Error() string { return string(e) }
