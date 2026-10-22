package usenetsearch

import (
	"context"
	"database/sql"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/release"
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
	KeyMoviesPackage = "usenet_nntp_package_movies"
	KeySeriesPackage = "usenet_nntp_package_series"
	KeyAdultPackage  = "usenet_nntp_package_adult"
	KeyIndexDir      = "usenet_nntp_index_dir"
	KeyIndexMaxGB    = "usenet_nntp_index_max_gb"
	KeyWindowDays    = "usenet_nntp_index_window_days"
	KeyCrawlInterval = "usenet_nntp_crawl_interval_seconds"
	KeyProbeState    = "usenet_nntp_probe_state"
	KeyProbeDetail   = "usenet_nntp_probe_detail"
	// Claude 2026-10-15: crawl-time quality admission kill switch (Step 4).
	// Reason: locked decision #10 — default true; set "false" via Settings
	//   KV directly (no UI toggle yet) to reproduce the pre-admission
	//   upsert-everything crawl without disabling native search entirely.
	// Troubleshooting: api.loadNNTPNativeSettings reads this with GetBool
	//   default true; configFromNNTPResponse carries it into Config.
	// Review if: a Settings UI toggle for this ships.
	KeyAdmissionEnabled = "usenet_nntp_admission_enabled"

	// Claude 2026-10-15: gap-fill kill switches and knobs (plan nntp-gap-fill).
	// Reason: every feature has an independent off switch so all-off ≈ the
	//   pre-gap-fill crawl. KV-only (no Settings UI); read by
	//   api.loadNNTPNativeSettings on every GET/PUT/boot. PreDB/IRC keys are
	//   read when the PreDB runtime starts (boot, or after the IRC password PUT).
	// Troubleshooting: knob ignored → check it is stored under exactly this key.
	// Review if: a Settings UI for these ships.
	KeyNameFixEnabled       = "usenet_nntp_namefix_enabled"
	KeyNameFixBodyMBPerHour = "usenet_nntp_namefix_body_mb_per_hour"
	KeyUnnamedTTLDays       = "usenet_nntp_unnamed_ttl_days"
	KeyUnnamedMaxPct        = "usenet_nntp_unnamed_max_pct"
	KeyPreDBHTTPEnabled     = "usenet_nntp_predb_http_enabled"
	KeyPreDBHTTPURL         = "usenet_nntp_predb_http_url"
	KeyPreDBIRCEnabled      = "usenet_nntp_predb_irc_enabled"
	KeyPreDBIRCServer       = "usenet_nntp_predb_irc_server"
	KeyPreDBIRCPort         = "usenet_nntp_predb_irc_port"
	KeyPreDBIRCTLS          = "usenet_nntp_predb_irc_tls"
	KeyPreDBIRCNick         = "usenet_nntp_predb_irc_nick"
	KeyPreDBIRCChannels     = "usenet_nntp_predb_irc_channels"
	KeyPreDBIRCPasswordEnc  = "usenet_nntp_predb_irc_password_enc" // secrets.Store ciphertext, never plaintext
	// Claude 2026-10-19: extra IRC feeds besides the primary synIRC client.
	// Reason: dump file ends 2024-05; api.predb.net forbids building a local DB;
	//   a second network keeps reverse-lookup fresh if synIRC drops.
	// Troubleshooting: empty KV uses Corrupt-net #pre; "[]" or "none" disables extras.
	// Review if: Corrupt-net #pre requires a key or goes invite-only.
	KeyPreDBIRCExtra     = "usenet_nntp_predb_irc_extra"     // JSON []{server,port,tls,channels}; empty = default
	KeyPreDBDumpPath     = "usenet_nntp_predb_dump_path"     // local nZEDb TSV or http(s) dump file URL; empty = off
	KeyPreDBDumpImported = "usenet_nntp_predb_dump_imported" // path|mtime|size stamp, not a knob
	// Claude 2026-10-20: srrdb archive-crc kill switch (CRC lookup only).
	// Reason: remaining hash.par2 FileDesc names miss PreDB; srrdb ToS is
	//   "Use but don't scrape" — CRC queries, never catalog paging.
	// Troubleshooting: name_source=srrdb stays 0 → this key false or SRRDB
	//   client nil. Review if: provider rate-limits sakms-srrdb/1.
	KeySRRDBEnabled = "usenet_nntp_srrdb_enabled"
	// Claude 2026-10-21: Newznab password lookup kill switch (default on).
	// Reason: namefix uses the Settings Newznab host via t=search/t=get.
	//   Off = skip the lookup. Review if: a Settings UI toggle ships.
	// Troubleshooting: nzb-password never logs → this key false or the
	//   newznab connection is unset.
	KeyNZBPasswordEnabled = "usenet_nntp_nzb_password_enabled"
	KeyMinCompletenessPct = "usenet_nntp_min_completeness_pct"
	KeyIncompleteTTLDays  = "usenet_nntp_incomplete_ttl_days"
	KeyRepairEnabled      = "usenet_nntp_repair_enabled"
	KeyBackfillEnabled    = "usenet_nntp_backfill_enabled"
	KeyBackfillDays       = "usenet_nntp_backfill_days"
	KeyBackfillBudgetPct  = "usenet_nntp_backfill_budget_pct"
	// Claude 2026-10-19: v3 rekeys ident-quoted episode filenames.
	// Reason: v2 left season-pack stems / obf: on quoted SxxEyy 1080p names;
	//   Searchable stayed a pack row at 4.5% of [k/N]×parts. Watermarks stay.
	// Troubleshooting: o2cli grep "collection rewrite"; stuck cursor → delete
	//   these two keys so the next loop resumes. v2 KV is leftover, unused.
	// Review if: parseHeader grouping changes again (then v4, do not reuse v3).
	KeyCollectionRewriteDone   = "usenet_nntp_collection_rewrite_v3"
	KeyCollectionRewriteCursor = "usenet_nntp_collection_rewrite_v3_cursor"
	// Claude 2026-10-16: extra RE2 collection patterns + drift kill switch.
	// Reason: AI-proposed strip_prefix/lead_token live in KV; ingest reads the
	//   published set. Kill switch default true, no-op without an AI client.
	// Troubleshooting: o2cli grep "collection drift"; set this key false to
	//   stop new proposals (existing extras still apply until KV is cleared).
	// Review if: extras should also trigger a v3 header rewrite.
	KeyCollectionDriftEnabled  = "usenet_nntp_collection_drift_enabled"
	KeyCollectionExtraPatterns = "usenet_nntp_collection_extra_patterns"
	KeyCollectionSeederCache   = "usenet_nntp_collection_seeder_cache"
	KeyCollectionSeederSeen    = "usenet_nntp_collection_seeder_seen"

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
	// Claude 2026-10-15: crawl-time quality admission kill switch (settings
	// key usenet_nntp_admission_enabled, default true — see
	// api.LoadUsenetSearchConfig, Step 4). false (or Service.admit unset)
	// reproduces the pre-admission upsert-everything crawl byte-for-byte.
	AdmissionEnabled bool
	// MinCompletenessPct is the strict payload completeness gate (settings key
	// usenet_nntp_min_completeness_pct; 0 = default 95, clamped 80-100).
	MinCompletenessPct int
	// IncompleteTTLDays reaps below-gate named releases (key
	// usenet_nntp_incomplete_ttl_days; 0 = default 3).
	IncompleteTTLDays int
	// RepairEnabled gates OVER gap capture + missed-range repair (key
	// usenet_nntp_repair_enabled, default true via api; zero value = off).
	RepairEnabled bool
	// NameFixEnabled stages obfuscated sets and recovers true names (key
	// usenet_nntp_namefix_enabled; default true via api, zero value = off).
	NameFixEnabled bool
	// UnnamedTTLDays / UnnamedMaxPct bound the staging area (0 = defaults 7 / 25).
	UnnamedTTLDays int
	UnnamedMaxPct  int
	// NameFixBodyMBPerHour caps BODY volume for name recovery (0 = default 512).
	NameFixBodyMBPerHour int
	// BackfillEnabled walks history below the forward crawl down to provider
	// retention (key usenet_nntp_backfill_enabled; default true via api).
	// BackfillDays limits depth (0 = retention); BackfillBudgetPct pauses at
	// that share of IndexMaxGB (0 = DefaultBackfillBudgetPct).
	BackfillEnabled   bool
	BackfillDays      int
	BackfillBudgetPct int
	// CollectionDriftEnabled gates the idle ChatJSON pass that may add extra
	// RE2 grouping patterns (key usenet_nntp_collection_drift_enabled;
	// default true via api). Zero value = off. No-op when DriftAI is nil.
	CollectionDriftEnabled bool
}

// GroupModesFor returns the enabled modes (a subset of "movies"/"series"/
// "adult") whose GroupsForMode includes group — PolicySnapshot.Resolve's
// groupModes input for a shared newsgroup (e.g. multimedia under both
// movies and series).
func (c Config) GroupModesFor(group string) []string {
	var out []string
	for _, mode := range []string{"movies", "series", "adult"} {
		if !c.ModeEnabled(mode) {
			continue
		}
		for _, g := range c.GroupsForMode(mode) {
			if g == group {
				out = append(out, mode)
				break
			}
		}
	}
	return out
}

func (c Config) packageRawForMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "movies":
		return c.MoviesPackage
	case "series":
		return c.SeriesPackage
	case "adult":
		return c.AdultPackage
	default:
		return ""
	}
}

func (c *Config) setGroupsForMode(mode string, groups []string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "movies":
		c.MoviesGroups = groups
	case "series":
		c.SeriesGroups = groups
	case "adult":
		c.AdultGroups = groups
	}
}

// PackageCeilingForMode returns the crawl/search ceiling step for mode.
func (c Config) PackageCeilingForMode(mode string) int {
	raw := c.packageRawForMode(mode)
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
	// Claude 2026-10-22: force one OVER after this many leftover skips.
	// Reason: DuePendingNames is 40; live BODY does ~5/90s so leftover stays
	//   >0 for hours. Uncapped skip would starve HIGH catch-up.
	// Troubleshooting: skip crawl leftover with skips stuck below this →
	//   crawl after leftover never logs. Review if: HIGH lag >1d with
	//   leftover always >0.
	namefixSkipCrawlMax = 4
)

// skipForwardCrawl holds OVER/repair/backfill while BODY leftover remains,
// then forces a crawl after namefixSkipCrawlMax consecutive skips.
func skipForwardCrawl(bodyLeftover, consecutiveSkips int) bool {
	return bodyLeftover > 0 && consecutiveSkips < namefixSkipCrawlMax
}

// ProtectedGroupsFn returns newsgroup names that must not have their index
// deleted (in-flight native grabs still need ResolveLocator).
type ProtectedGroupsFn func(ctx context.Context) map[string]bool

// PreferredLanguagesFn returns the Global preferred grab language IDs
// (empty = english-assumed). Nil fn disables NNTP language scoping.
type PreferredLanguagesFn func(ctx context.Context) []string

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
	protectFn  ProtectedGroupsFn
	langsFn    PreferredLanguagesFn
	lastLangs  []string
	langPruned map[string]bool
	// membershipExpanded is true after a successful LIST ACTIVE rewrite of
	// non-custom mode groups (retry while false so a boot LIST miss recovers).
	membershipExpanded bool
	membershipRetryAt  time.Time

	// admit is nil until SetAdmissionPolicy is called (api wiring, Step 4).
	// crawlEligibleGroups-selected groups get pruned once each (prunedOnce)
	// the first crawl pass after admission is enabled.
	admit      *admitter
	prunedOnce map[string]bool
	// predb resolves recovered names/hashes during name-fix (nil = no PreDB).
	predb PreDBLookup
	// srrdb resolves RAR/ZIP/SFV inner-file CRC32 (nil disables it).
	srrdb ArchiveCRCLookup
	// nzbpw looks up NZB unpack passwords by obfuscated filename (nil = off).
	nzbpw NZBPasswordLookup
	// ranges is the last crawl pass's per-group provider range (read-only once
	// stored; each pass builds a fresh map) — GapFillStatus reports backfill
	// targets from it.
	ranges map[string]usenet.GroupRange
	// lastChunk / lastPass feed the Dashboard crawler card.
	lastChunk CrawlChunkStat
	lastPass  CrawlResult
	// identID / identBin / headerN are the last identifiedStats snapshot.
	// The dashboard GET must not scan usenet_nntp_headers.
	identID  int64
	identBin int64
	headerN  int64

	driftAIFn        DriftAIFunc
	driftFetch       DriftFetchFunc
	driftLastAt      time.Time
	driftLastCluster string
}

// SetPreDB wires the PreDB lookup used by name recovery (nil disables it).
func (s *Service) SetPreDB(p PreDBLookup) {
	s.mu.Lock()
	s.predb = p
	s.mu.Unlock()
}

// SetSRRDB wires the srrdb CRC lookup used by name recovery (nil disables it).
func (s *Service) SetSRRDB(c ArchiveCRCLookup) {
	s.mu.Lock()
	s.srrdb = c
	s.mu.Unlock()
}

// SetNZBPassword wires the optional Newznab supplement after a local 7z-aes miss (nil disables it).
func (s *Service) SetNZBPassword(p NZBPasswordLookup) {
	s.mu.Lock()
	s.nzbpw = p
	s.mu.Unlock()
}

// NewService constructs a stopped Service. Call Apply to open the index / start crawl.
func NewService(src usenet.HeaderSource, sqlDB *sql.DB) *Service {
	return &Service{src: src, sqlDB: sqlDB, probe: Readiness{State: "unknown"}}
}

// SetProtectedGroupsFn registers groups whose index must not be deleted.
func (s *Service) SetProtectedGroupsFn(fn ProtectedGroupsFn) {
	s.mu.Lock()
	s.protectFn = fn
	s.mu.Unlock()
}

func (s *Service) SetPreferredLanguagesFn(fn PreferredLanguagesFn) {
	s.mu.Lock()
	s.langsFn = fn
	s.mu.Unlock()
}

func (s *Service) preferredLanguages(ctx context.Context) (langs []string, filter bool) {
	s.mu.Lock()
	fn := s.langsFn
	s.mu.Unlock()
	if fn == nil {
		return nil, false
	}
	return fn(ctx), true
}

// SetAdmissionPolicy wires the crawl-time quality admission policy (Step 4,
// api.NewNNTPAdmissionPolicy). Replacing the policy starts a fresh admitter
// (snapshot cache + verdict LRU) — call InvalidateAdmissionPolicy instead
// for a prefs change, which keeps the verdict LRU intact.
func (s *Service) SetAdmissionPolicy(policy AdmissionPolicy) {
	s.mu.Lock()
	s.admit = newAdmitter(policy)
	s.prunedOnce = nil
	s.mu.Unlock()
}

// InvalidateAdmissionPolicy forces the next crawl pass to re-fetch the
// PolicySnapshot — called on a title/mode quality-prefs PUT or DELETE.
func (s *Service) InvalidateAdmissionPolicy() {
	s.mu.Lock()
	a := s.admit
	s.mu.Unlock()
	if a != nil {
		a.Invalidate()
	}
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
	s.membershipExpanded = false
	s.membershipRetryAt = time.Time{}
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
	idx.SetMinCompletenessPct(cfg.MinCompletenessPct)
	idx.SetSpanRepair(cfg.RepairEnabled)
	s.idx = idx
	s.loadExtraPatterns(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.loop(ctx)
	go s.refreshIdentified(ctx)
	return nil
}

func (s *Service) loop(ctx context.Context) {
	// Claude 2026-10-15: continuous rate-limited crawl + liveness (no schedules).
	// Reason: approved plan — OVER catch-up and STAT deletions, idle only when quiet.
	// Troubleshooting: loop exits only on Apply cancel / disable.
	// Review if: CrawlInterval is renamed to idle_backoff_seconds in settings KV.
	budget := &bodyBudget{}
	backfillStart := 0
	crawlStart := 0
	coreStart := 0
	backfillSkipLogged := false
	backfillFilterLogged := false
	namefixCrawlSkips := 0
	for {
		if ctx.Err() != nil {
			return
		}
		s.runCollectionRewrite(ctx)
		s.refreshMembershipFromList(ctx)
		// Claude 2026-10-22: recompute + namefix before OVER.
		// Reason: live OVER of hundreds of groups sat minutes between 90s
		//   BODY passes (leftover=38 after FileDesc-target load). Namefix
		//   cadence is independent of crawl catch-up. Skip OVER/repair/
		//   backfill while BodyLeftover>0, cap consecutive skips so HIGH
		//   cannot starve. ErrBusy still skips crawl (downloads win).
		// Troubleshooting: o2cli grep "skip crawl leftover"; no crawl
		//   chunks for >10min → skip cap not firing. leftover=0 every
		//   pass and OVER still first → this order reversed.
		// Review if: BODY leftover stays 0 and searchable still waits on OVER.
		crawler := s.makeCrawler(ctx)
		// Claude 2026-10-16: recompute before namefix, and drain extra dirty
		// batches when the queue is huge.
		// Reason: DuePendingNames requires dirty=FALSE; a 314k FIFO backlog
		//   (0045 + crawl upserts) left complete pending_name un-namefixed.
		// Troubleshooting: namefix pass sets=0 with pending_name in the 100ks
		//   → dirty complete sets are not at the front of RecomputeDirty.
		// Review if: crawl should pause while dirty pending_name exceeds a knob.
		s.runRecompute(ctx)
		s.refreshIdentified(ctx)
		busy, advanced := false, false
		bodyLeftover := 0
		if crawler != nil && crawler.NameFixEnabled {
			nr := s.runNameFix(ctx, crawler, budget)
			busy = nr.Busy
			advanced = nr.Advanced()
			bodyLeftover = nr.BodyLeftover
		}
		holdCrawl := skipForwardCrawl(bodyLeftover, namefixCrawlSkips)
		if crawler != nil {
			if holdCrawl || busy {
				if holdCrawl {
					namefixCrawlSkips++
				}
				log.Printf("usenetsearch: skip crawl leftover=%d skips=%d busy=%t",
					bodyLeftover, namefixCrawlSkips, busy)
			} else {
				if namefixCrawlSkips > 0 {
					log.Printf("usenetsearch: crawl after leftover skips=%d leftover=%d",
						namefixCrawlSkips, bodyLeftover)
				}
				namefixCrawlSkips = 0
				cres := s.runCrawl(ctx, crawler, crawlStart, coreStart)
				crawlStart = cres.NextStart
				coreStart = cres.NextCoreStart
				busy = busy || cres.Busy
				advanced = advanced || cres.Advanced
				if !busy && crawler.RepairEnabled {
					rr, err := crawler.RepairOnce(ctx)
					if err != nil {
						log.Printf("usenetsearch: repair: %v", err)
					}
					busy = busy || rr.Busy
					advanced = advanced || rr.Advanced
				}
				// Claude 2026-10-16: backfill still runs while SkipListTail; dvd/bloaf
				// are filtered in backfillWanted so yield-core history keeps moving.
				if !busy && crawler.shouldBackfill() {
					crawler.BackfillStart = backfillStart
					br, err := crawler.BackfillOnce(ctx)
					if err != nil {
						log.Printf("usenetsearch: backfill: %v", err)
					}
					if br.Filtered > 0 && !backfillFilterLogged {
						log.Printf("usenetsearch: backfill skipping %d groups (not a package core and no named index)", br.Filtered)
						backfillFilterLogged = true
					}
					backfillStart = br.NextStart
					if br.Skipped != "" && !backfillSkipLogged {
						log.Printf("usenetsearch: backfill skipped reason=%s", br.Skipped)
					}
					backfillSkipLogged = br.Skipped != ""
					busy = busy || br.Busy
					advanced = advanced || br.Advanced || br.HasWork
				}
			}
		}
		s.runSweeps(ctx)
		s.runLiveness(ctx)
		// Claude 2026-10-16: one ChatJSON grouping pass while idle (plan 2A).
		// Reason: never per article; skip when NNTP is busy or still catching
		//   up so Ollama/cloud is not hit every 500ms. Rewrite must finish
		//   first so samples are not pre-stem fragments.
		// Troubleshooting: no "collection drift" logs → AI nil, kill switch
		//   off, rewrite v3 still running, or crawler not idle.
		// Review if: extras should also rewrite stored headers.
		if !busy && !advanced {
			s.runCollectionDrift(ctx)
		}

		var wait time.Duration
		switch {
		case busy:
			wait = busyBackoff
		case advanced:
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

// makeCrawler probes NNTP and builds the per-pass Crawler without OVER.
// The loop runs namefix first, then runCrawl unless BODY leftover holds it.
func (s *Service) makeCrawler(ctx context.Context) *Crawler {
	s.mu.Lock()
	src := s.src
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	langs, langFilter := s.preferredLanguages(ctx)
	s.mu.Lock()
	if langFilter && !slices.Equal(s.lastLangs, langs) {
		s.lastLangs = append([]string(nil), langs...)
		s.langPruned = nil
		log.Printf("usenetsearch: preferred languages %v — index scoped to matching groups/titles", langs)
	}
	s.mu.Unlock()
	groups := s.crawlEligibleGroups(ctx)
	r := Probe(ctx, src, groups)
	s.mu.Lock()
	s.probe = r
	s.mu.Unlock()
	if !r.Ready || idx == nil || src == nil {
		return nil
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
	s.mu.Lock()
	admit := s.admit
	s.mu.Unlock()
	admissionEnabled := cfg.AdmissionEnabled && admit != nil
	if admissionEnabled {
		s.pruneNonMatchingOnce(ctx, groups, cfg, admit, idx)
	}
	s.pruneWrongLanguageOnce(ctx, groups, langs, langFilter, idx)
	c := &Crawler{
		Src: src, Index: idx, Groups: groups, Window: window, MaxGB: maxGB,
		AdmissionEnabled:   admissionEnabled,
		GroupModesFn:       cfg.GroupModesFor,
		Admitter:           admit,
		RepairEnabled:      cfg.RepairEnabled,
		NameFixEnabled:     cfg.NameFixEnabled,
		UnnamedMaxPct:      cfg.UnnamedMaxPct,
		BackfillEnabled:    cfg.BackfillEnabled,
		BackfillDays:       cfg.BackfillDays,
		BackfillBudgetPct:  cfg.BackfillBudgetPct,
		LanguageFilter:     langFilter,
		PreferredLanguages: langs,
		// Claude 2026-10-16: publish last OVER chunk as it lands, not after the pass.
		// Reason: LIST membership is hundreds of groups; a Dashboard poll mid-pass
		// should show the current chunk, not the previous pass.
		// Troubleshooting: lastChunk stuck until idle → OnChunk not wired.
		// Review if: Dashboard joins sysinfo SSE instead of REST poll.
		OnChunk: func(st CrawlChunkStat) {
			s.mu.Lock()
			s.lastChunk = st
			s.mu.Unlock()
		},
	}
	return c
}

func (s *Service) runCrawl(ctx context.Context, c *Crawler, crawlStart, coreStart int) CrawlResult {
	c.CrawlStart = crawlStart
	c.CoreStart = coreStart
	cres, err := c.CrawlOnce(ctx)
	if err != nil {
		log.Printf("usenetsearch: crawl: %v", err)
	}
	s.mu.Lock()
	s.ranges = c.ranges
	s.lastPass = cres
	s.mu.Unlock()
	return cres
}

// pruneNonMatchingOnce runs the one-shot prune of already-indexed rows that
// fail the quality gate (locked decision #8) — once per group, per process,
// the first crawl pass after admission is enabled. It never rewinds a
// watermark; it only deletes rows the gate rejects under every candidate
// mode. No STAT check: pruning is quality-gate-only (plan: "no STAT needed
// for prune — quality only").
func (s *Service) pruneNonMatchingOnce(ctx context.Context, groups []string, cfg Config, admit *admitter, idx *Index) {
	s.mu.Lock()
	if s.prunedOnce == nil {
		s.prunedOnce = map[string]bool{}
	}
	pending := make([]string, 0, len(groups))
	for _, g := range groups {
		if !s.prunedOnce[g] {
			pending = append(pending, g)
		}
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	snap, err := admit.Snapshot(ctx)
	if err != nil || snap == nil {
		return
	}
	for _, group := range pending {
		if err := ctx.Err(); err != nil {
			return
		}
		n, err := pruneGroupNonMatching(ctx, idx, group, cfg.GroupModesFor(group), snap)
		if err != nil {
			log.Printf("usenetsearch: prune non-matching %s: %v", group, err)
			continue // retry on the next crawl pass
		}
		s.mu.Lock()
		s.prunedOnce[group] = true
		s.mu.Unlock()
		if n > 0 {
			log.Printf("usenetsearch: pruned %d non-matching headers for %s (one-shot admission backfill)", n, group)
		}
	}
}

func (s *Service) pruneWrongLanguageOnce(ctx context.Context, groups []string, langs []string, filter bool, idx *Index) {
	if !filter || idx == nil {
		return
	}
	s.mu.Lock()
	if s.langPruned == nil {
		s.langPruned = map[string]bool{}
	}
	pending := make([]string, 0, len(groups))
	for _, g := range groups {
		if !s.langPruned[g] {
			pending = append(pending, g)
		}
	}
	s.mu.Unlock()
	for _, group := range pending {
		if err := ctx.Err(); err != nil {
			return
		}
		n, err := pruneGroupWrongLanguage(ctx, idx, group, langs)
		if err != nil {
			log.Printf("usenetsearch: prune language %s: %v", group, err)
			continue
		}
		s.mu.Lock()
		s.langPruned[group] = true
		s.mu.Unlock()
		if n > 0 {
			log.Printf("usenetsearch: pruned %d non-preferred-language headers for %s", n, group)
		}
	}
}

func pruneGroupWrongLanguage(ctx context.Context, idx *Index, group string, langs []string) (int64, error) {
	names, err := idx.DistinctReleases(ctx, group)
	if err != nil {
		return 0, err
	}
	var reject []string
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !release.TitleLanguageAllowed(name, langs) {
			reject = append(reject, name)
		}
	}
	if len(reject) == 0 {
		return 0, nil
	}
	return idx.DeleteReleases(ctx, group, reject)
}

// crawlEligibleGroups is the union of per-mode groups with grade ≤ ceiling.
func (s *Service) crawlEligibleGroups(ctx context.Context) []string {
	s.mu.Lock()
	cfg := s.cfg
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return s.filterGroupsByLanguage(ctx, cfg.AllGroups())
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
	return s.filterGroupsByLanguage(ctx, MergeGroups(lists...))
}

func (s *Service) filterGroupsByLanguage(ctx context.Context, groups []string) []string {
	langs, filter := s.preferredLanguages(ctx)
	if !filter {
		return groups
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if release.TitleLanguageAllowed(groupLanguageQuery(g), langs) {
			out = append(out, g)
		}
	}
	return out
}

// PruneBeyondCeilings deletes index rows for groups that no enabled mode still allows.
func (s *Service) PruneBeyondCeilings(ctx context.Context) error {
	s.mu.Lock()
	cfg := s.cfg
	idx := s.idx
	protectFn := s.protectFn
	s.mu.Unlock()
	if idx == nil {
		return nil
	}
	allowed := map[string]bool{}
	for _, g := range s.crawlEligibleGroups(ctx) {
		allowed[g] = true
	}
	var protected map[string]bool
	if protectFn != nil {
		protected = protectFn(ctx)
	}
	for _, g := range cfg.AllGroups() {
		if allowed[g] || protected[g] {
			continue
		}
		n, err := idx.DeleteGroupIndex(ctx, g)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Printf("usenetsearch: pruned %d headers for %s (not eligible under any mode ceiling/grade)", n, g)
		}
	}
	return nil
}

// PromoteForSuccess records a successful native import for mode/group.
// After two successes the grade improves by one tier (floor 1).
func (s *Service) PromoteForSuccess(ctx context.Context, mode, group string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return
	}
	grade, promoted, err := idx.NoteSuccess(ctx, mode, group)
	if err != nil {
		log.Printf("usenetsearch: promote note %s/%s: %v", mode, group, err)
		return
	}
	if promoted {
		log.Printf("usenetsearch: promote mode=%s group=%s grade=%d (after %d successes)",
			mode, group, grade, promoteEverySuccesses)
	}
}

// DemoteForFailure bumps the group's grade for mode and deletes its index
// when no enabled mode still allows the group (and no in-flight grab protects it).
func (s *Service) DemoteForFailure(ctx context.Context, mode, group, downloadGID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	idx := s.idx
	protectFn := s.protectFn
	s.mu.Unlock()
	if idx == nil {
		return
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return
	}
	grade, err := idx.DemoteGroup(ctx, mode, group, downloadGID)
	if err != nil {
		log.Printf("usenetsearch: demote %s/%s: %v", mode, group, err)
		return
	}
	log.Printf("usenetsearch: demote mode=%s group=%s grade=%d gid=%s", mode, group, grade, downloadGID)
	// Claude 2026-10-15: never delete index while any enabled mode still allows the group.
	// Reason: shared groups (multimedia, hdtv.x264) would re-crawl/delete-loop.
	// Troubleshooting: index empty for series after movies demote — check eligibility union.
	// Review if: grades become global per group_name.
	if s.groupAllowedByAnyMode(ctx, group) {
		return
	}
	if protectFn != nil && protectFn(ctx)[group] {
		log.Printf("usenetsearch: skip index delete for %s (active grab)", group)
		return
	}
	n, err := idx.DeleteGroupIndex(ctx, group)
	if err != nil {
		log.Printf("usenetsearch: delete index %s: %v", group, err)
		return
	}
	log.Printf("usenetsearch: deleted %d headers for %s (ineligible in all modes)", n, group)
}

// ResetGroupGrade restores grade 1 for mode/group (operator escape).
func (s *Service) ResetGroupGrade(ctx context.Context, mode, group string) error {
	if s == nil {
		return errString("usenetsearch: service nil")
	}
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return ErrIndexClosed
	}
	return idx.ResetGrade(ctx, mode, group)
}

// groupAllowedByAnyMode reports whether any enabled mode still crawls group.
func (s *Service) groupAllowedByAnyMode(ctx context.Context, group string) bool {
	for _, g := range s.crawlEligibleGroups(ctx) {
		if g == group {
			return true
		}
	}
	return false
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

// runNameFix runs one name-recovery pass over staged obfuscated sets.
func (s *Service) runNameFix(ctx context.Context, c *Crawler, budget *bodyBudget) NameFixResult {
	s.mu.Lock()
	cfg := s.cfg
	predb := s.predb
	srrdb := s.srrdb
	nzbpw := s.nzbpw
	s.mu.Unlock()
	budget.setCapMB(cfg.NameFixBodyMBPerHour)
	nf := &NameFixer{
		Src: c.Src, Index: c.Index, Crawler: c, PreDB: predb, SRRDB: srrdb,
		NZBPassword: nzbpw,
		Groups:      c.Groups, Budget: budget,
		UnnamedTTL: time.Duration(cfg.UnnamedTTLDays) * 24 * time.Hour,
	}
	res, err := nf.Run(ctx)
	if err != nil {
		log.Printf("usenetsearch: namefix: %v", err)
	}
	return res
}

const (
	recomputeCatchupMax        = 8
	recomputeCatchupMinBacklog = 2000
	recomputeMaxDuration       = 20 * time.Second
)

// runRecompute drains a bounded batch of dirty releases (DB only, no NNTP),
// plus up to recomputeCatchupMax more while the backlog is large and the
// wall-clock budget remains.
//
// Claude 2026-10-16: 20s cap + per-batch log so crawl is not silent for 10 min.
// Reason: extra_batches=8 × 500 recomputeOne of huge keys blocked the loop
// (WALWrite) with one summary log at the end; dashboard looked crashed.
// Troubleshooting: crawl chunks missing >30s after "recompute working" →
// deadline not stopping the per-key loop.
// Review if: dirty backlog is gone and extra catch-up is unused.
func (s *Service) runRecompute(ctx context.Context) {
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	if idx == nil {
		return
	}
	idx.dirtyIdentifiableNamed(ctx)
	deadline := time.Now().Add(recomputeMaxDuration)
	st, err := idx.recomputeDirtyBounded(ctx, recomputeBatch, deadline)
	if err != nil {
		log.Printf("usenetsearch: recompute: %v", err)
		return
	}
	total := st
	extra := 0
	for extra < recomputeCatchupMax && st.Backlog >= recomputeCatchupMinBacklog {
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		st, err = idx.recomputeDirtyBounded(ctx, recomputeBatch, deadline)
		if err != nil {
			log.Printf("usenetsearch: recompute: %v", err)
			return
		}
		extra++
		total.Releases += st.Releases
		total.Searchable += st.Searchable
		total.BelowGate += st.BelowGate
		total.Undeclared += st.Undeclared
		total.Orphans += st.Orphans
		total.Backlog = st.Backlog
		log.Printf("usenetsearch: completeness recompute in-progress releases=%d backlog=%d extra_batches=%d",
			total.Releases, total.Backlog, extra)
	}
	if total.Releases > 0 {
		log.Printf("usenetsearch: completeness recompute releases=%d searchable=%d below_gate=%d undeclared=%d orphans=%d backlog=%d extra_batches=%d",
			total.Releases, total.Searchable, total.BelowGate, total.Undeclared, total.Orphans, total.Backlog, extra)
	}
}

// runSweeps runs the DB-only TTL cleanups (plan D7 step 6).
func (s *Service) runSweeps(ctx context.Context) {
	s.mu.Lock()
	idx := s.idx
	cfg := s.cfg
	s.mu.Unlock()
	if idx == nil {
		return
	}
	days := cfg.IncompleteTTLDays
	if days <= 0 {
		days = DefaultIncompleteTTLDays
	}
	ttl := time.Duration(days) * 24 * time.Hour
	const incompleteCatchupMax = 40
	const incompleteSweepBudget = 20 * time.Second
	deadline := time.Now().Add(incompleteSweepBudget)
	var removed, extra int
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := idx.SweepIncomplete(ctx, ttl)
		if err != nil {
			log.Printf("usenetsearch: incomplete sweep: %v", err)
			return
		}
		removed += n
		if n < incompleteSweepBatch || extra >= incompleteCatchupMax || time.Now().After(deadline) {
			break
		}
		extra++
	}
	if removed > 0 {
		log.Printf("usenetsearch: incomplete sweep removed=%d extra_batches=%d ttl_days=%d", removed, extra, days)
	}
	if spans, err := idx.EnqueueStalePendingSpans(ctx); err != nil {
		log.Printf("usenetsearch: pending span enqueue: %v", err)
	} else if spans > 0 {
		log.Printf("usenetsearch: pending span enqueue ranges=%d", spans)
	}
	// Claude 2026-10-16: drain unique-ID yEnc pending_name (part_m > maxYencPartM).
	// Reason: 104k junk sets sat at 0% complete and held unnamed caps on teevee/hdtv.x264.
	// Troubleshooting: pending_name not falling after deploy → this log missing.
	// Review if: the ingest cap made the backlog empty (then this is a no-op).
	const implausibleYencCatchupMax = 20
	removed, extra = 0, 0
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := idx.SweepImplausibleYenc(ctx, implausibleYencSweepBatch)
		if err != nil {
			log.Printf("usenetsearch: implausible yenc sweep: %v", err)
			return
		}
		removed += n
		if n < implausibleYencSweepBatch || extra >= implausibleYencCatchupMax {
			break
		}
		extra++
	}
	if removed > 0 {
		log.Printf("usenetsearch: implausible yenc sweep removed=%d extra_batches=%d", removed, extra)
	}
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

// refreshMembershipFromList replaces non-custom mode groups with LIST ACTIVE.
// Retries while LIST fails so a boot miss still expands once NNTP is up.
func (s *Service) refreshMembershipFromList(ctx context.Context) {
	s.mu.Lock()
	if s.membershipExpanded || !s.cfg.Enabled || time.Now().Before(s.membershipRetryAt) {
		s.mu.Unlock()
		return
	}
	cfg := s.cfg
	s.mu.Unlock()

	allOK := true
	changed := false
	for _, mode := range []string{"movies", "series", "adult"} {
		if !cfg.ModeEnabled(mode) {
			continue
		}
		_, custom, ok := ParsePackageValue(cfg.packageRawForMode(mode))
		if ok && custom {
			continue
		}
		list, err := s.ListAvailableGroups(ctx, mode)
		if err != nil {
			log.Printf("usenetsearch: LIST ACTIVE %s: %v", mode, err)
			allOK = false
			continue
		}
		if len(list) == 0 {
			allOK = false
			continue
		}
		if sameGroupSet(list, cfg.GroupsForMode(mode)) {
			continue
		}
		cfg.setGroupsForMode(mode, list)
		changed = true
		log.Printf("usenetsearch: %s groups from LIST ACTIVE: %d", mode, len(list))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if changed {
		s.cfg = cfg
	}
	if allOK {
		s.membershipExpanded = true
	} else {
		s.membershipRetryAt = time.Now().Add(time.Minute)
	}
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
