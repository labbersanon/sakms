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
	KeyNativeEnabled = "usenet_nntp_native_enabled"
	KeyMoviesEnabled = "movies_nntp_native_enabled"
	KeySeriesEnabled = "series_nntp_native_enabled"
	KeyAdultEnabled  = "adult_nntp_native_enabled"
	KeyGroups        = "usenet_nntp_groups" // legacy shared list; soft-migrated
	KeyMoviesGroups  = "usenet_nntp_groups_movies"
	KeySeriesGroups  = "usenet_nntp_groups_series"
	KeyAdultGroups   = "usenet_nntp_groups_adult"
	// Claude 2026-10-15: per-mode package step (1–5) or "custom".
	// Reason: Settings slider; effective groups still in Key*Groups.
	// Troubleshooting: missing key → InferPackageFromGroups on read.
	// Review if: auto-apply re-rank (slice 4) writes these + group keys.
	KeyMoviesPackage  = "usenet_nntp_package_movies"
	KeySeriesPackage  = "usenet_nntp_package_series"
	KeyAdultPackage   = "usenet_nntp_package_adult"
	KeyIndexDir       = "usenet_nntp_index_dir"
	KeyIndexMaxGB     = "usenet_nntp_index_max_gb"
	KeyWindowDays     = "usenet_nntp_index_window_days"
	KeyCrawlInterval  = "usenet_nntp_crawl_interval_seconds"
	KeyProbeState     = "usenet_nntp_probe_state"
	KeyProbeDetail    = "usenet_nntp_probe_detail"
	DefaultIndexMaxGB = 20
	DefaultWindowDays = 14
)

// Config is a snapshot of operator settings for the native backend.
type Config struct {
	Enabled       bool
	Movies        bool
	Series        bool
	Adult         bool
	MoviesGroups  []string
	SeriesGroups  []string
	AdultGroups   []string
	MoviesPackage string // "1".."5" or "custom"
	SeriesPackage string
	AdultPackage  string
	IndexDir      string
	IndexMaxGB    int
	WindowDays    int
	CrawlInterval time.Duration
}

// PackageCeilingForMode returns the crawl/search ceiling step for mode.
func (c Config) PackageCeilingForMode(mode string) int {
	raw := ""
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "movies":
		raw = c.MoviesPackage
	case "series":
		raw = c.SeriesPackage
	case "adult":
		raw = c.AdultPackage
	}
	step, custom, ok := ParsePackageValue(raw)
	if !ok || custom {
		// Custom: treat as max ceiling; grade filter still applies vs step 5.
		return PackageStepMax
	}
	return step
}

// AllGroups is the deduped union of per-mode selections — what the crawler indexes.
func (c Config) AllGroups() []string {
	return MergeGroups(c.MoviesGroups, c.SeriesGroups, c.AdultGroups)
}

// GroupsForMode returns the operator-selected groups for mode (never nil).
func (c Config) GroupsForMode(mode string) []string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "movies":
		return append([]string{}, c.MoviesGroups...)
	case "series":
		return append([]string{}, c.SeriesGroups...)
	case "adult":
		return append([]string{}, c.AdultGroups...)
	default:
		return []string{}
	}
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

// ValidateIndexDir is a no-op; the index lives in Postgres (migration 0026).
func ValidateIndexDir(dir string) string {
	_ = dir
	return ""
}

// Claude 2026-10-15: continuous crawl defaults (conservative — protect downloads).
// Reason: no nightly cron; idle only when caught up / busy. CrawlInterval setting
//
//	is idle backoff when >0; else defaultIdleBackoff.
//
// Troubleshooting: index lag while downloads idle — lower idle; stall downloads — raise.
// Review if: rates become Settings knobs separate from CrawlInterval.
const (
	defaultIdleBackoff = 60 * time.Second
	busyBackoff        = 5 * time.Second
	catchUpPause       = 500 * time.Millisecond
)

// Service owns the index, crawler loop, and probe state.
type Service struct {
	mu             sync.Mutex
	cfg            Config
	src            usenet.HeaderSource
	sqlDB          *sql.DB
	idx            *Index
	cancel         context.CancelFunc
	probe          Readiness
	livenessOffset int
	// availCache: mode → last LIST ACTIVE media groups (settings resolve).
	availCache map[string][]string
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
	if len(cfg.AllGroups()) == 0 {
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
	// Claude 2026-10-15: continuous rate-limited crawl + liveness (no schedules).
	// Reason: approved plan — OVER catch-up and STAT deletions, idle only when quiet.
	// Troubleshooting: loop exits only on Apply cancel / disable.
	// Review if: CrawlInterval is renamed to idle_backoff_seconds in settings KV.
	for {
		if ctx.Err() != nil {
			return
		}
		cres := s.runProbeAndCrawl(ctx)
		s.runLiveness(ctx)

		var wait time.Duration
		switch {
		case cres.Busy:
			wait = busyBackoff
		case cres.Advanced:
			wait = catchUpPause
		default:
			wait = s.idleBackoff()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (s *Service) idleBackoff() time.Duration {
	s.mu.Lock()
	d := s.cfg.CrawlInterval
	s.mu.Unlock()
	if d <= 0 {
		return defaultIdleBackoff
	}
	return d
}

func (s *Service) runProbeAndCrawl(ctx context.Context) CrawlResult {
	s.mu.Lock()
	src := s.src
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	groups := s.crawlEligibleGroups(ctx)
	r := Probe(ctx, src, groups)
	s.mu.Lock()
	s.probe = r
	s.mu.Unlock()
	if !r.Ready || idx == nil || src == nil {
		return CrawlResult{}
	}
	_ = s.PruneBeyondCeilings(ctx)
	window := time.Duration(cfg.WindowDays) * 24 * time.Hour
	if cfg.WindowDays <= 0 {
		window = DefaultWindowDays * 24 * time.Hour
	}
	maxGB := cfg.IndexMaxGB
	if maxGB <= 0 {
		maxGB = DefaultIndexMaxGB
	}
	c := &Crawler{Src: src, Index: idx, Groups: groups, Window: window, MaxGB: maxGB}
	cres, err := c.CrawlOnce(ctx)
	if err != nil {
		log.Printf("usenetsearch: crawl: %v", err)
	}
	return cres
}

// crawlEligibleGroups is the union of per-mode groups with grade ≤ ceiling.
func (s *Service) crawlEligibleGroups(ctx context.Context) []string {
	s.mu.Lock()
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return cfg.AllGroups()
	}
	var lists [][]string
	for _, mode := range []string{"movies", "series", "adult"} {
		if !cfg.ModeEnabled(mode) {
			continue
		}
		ceiling := cfg.PackageCeilingForMode(mode)
		elig, err := idx.EligibleGroups(ctx, mode, ceiling, cfg.GroupsForMode(mode))
		if err != nil {
			log.Printf("usenetsearch: eligible %s: %v", mode, err)
			continue
		}
		lists = append(lists, elig)
	}
	return MergeGroups(lists...)
}

// PruneBeyondCeilings deletes index rows for groups graded past each mode's ceiling.
func (s *Service) PruneBeyondCeilings(ctx context.Context) error {
	s.mu.Lock()
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return nil
	}
	for _, mode := range []string{"movies", "series", "adult"} {
		if !cfg.ModeEnabled(mode) {
			continue
		}
		ceiling := cfg.PackageCeilingForMode(mode)
		if err := idx.PruneGroupsBeyondCeiling(ctx, mode, ceiling, cfg.GroupsForMode(mode)); err != nil {
			return err
		}
	}
	return nil
}

// DemoteForFailure bumps the group's grade for mode and deletes its index
// when the new grade is beyond the mode's package ceiling.
func (s *Service) DemoteForFailure(ctx context.Context, mode, group string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	idx := s.idx
	cfg := s.cfg
	s.mu.Unlock()
	if idx == nil {
		return
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return
	}
	grade, err := idx.DemoteGroup(ctx, mode, group)
	if err != nil {
		log.Printf("usenetsearch: demote %s/%s: %v", mode, group, err)
		return
	}
	ceiling := cfg.PackageCeilingForMode(mode)
	log.Printf("usenetsearch: demote mode=%s group=%s grade=%d ceiling=%d", mode, group, grade, ceiling)
	if grade > ceiling {
		n, err := idx.DeleteGroupIndex(ctx, group)
		if err != nil {
			log.Printf("usenetsearch: delete index %s: %v", group, err)
			return
		}
		log.Printf("usenetsearch: deleted %d headers for %s (grade %d > ceiling %d)", n, group, grade, ceiling)
	}
}

// ListGroupGrades returns stored grades for mode (empty if index closed).
func (s *Service) ListGroupGrades(ctx context.Context, mode string) ([]GroupGrade, error) {
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return nil, nil
	}
	return idx.ListGrades(ctx, mode)
}

// SearchGroupsForMode returns grade-eligible groups for search in mode.
func (s *Service) SearchGroupsForMode(ctx context.Context, mode string) []string {
	s.mu.Lock()
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	cands := cfg.GroupsForMode(mode)
	if idx == nil {
		return cands
	}
	elig, err := idx.EligibleGroups(ctx, mode, cfg.PackageCeilingForMode(mode), cands)
	if err != nil {
		log.Printf("usenetsearch: search eligible %s: %v", mode, err)
		return cands
	}
	return elig
}

func (s *Service) runLiveness(ctx context.Context) {
	s.mu.Lock()
	src := s.src
	idx := s.idx
	off := s.livenessOffset
	s.mu.Unlock()
	if src == nil || idx == nil {
		return
	}
	_, next, err := LivenessOnce(ctx, src, idx, off)
	if err != nil {
		log.Printf("usenetsearch: liveness: %v", err)
		return
	}
	s.mu.Lock()
	s.livenessOffset = next
	s.mu.Unlock()
}

// Ready implements Backend.
func (s *Service) Ready(ctx context.Context) (Readiness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.probe
	if !s.cfg.Enabled {
		return Readiness{Ready: false, State: "unknown", Detail: "disabled"}, nil
	}
	if len(s.cfg.AllGroups()) == 0 {
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

// ListAvailableGroups returns LIST ACTIVE media groups for mode (settings picker).
func (s *Service) ListAvailableGroups(ctx context.Context, mode string) ([]string, error) {
	s.mu.Lock()
	src := s.src
	s.mu.Unlock()
	groups, err := ListMediaGroups(ctx, src, mode)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.availCache == nil {
		s.availCache = map[string][]string{}
	}
	cp := append([]string{}, groups...)
	s.availCache[strings.ToLower(strings.TrimSpace(mode))] = cp
	s.mu.Unlock()
	return groups, nil
}

// CachedAvailableGroups returns the last LIST ACTIVE result for mode, or nil.
func (s *Service) CachedAvailableGroups(mode string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.availCache == nil {
		return nil
	}
	return append([]string{}, s.availCache[strings.ToLower(strings.TrimSpace(mode))]...)
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
