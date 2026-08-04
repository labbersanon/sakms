// Package discoverrefresh background-populates a read cache for the three
// Discover row categories that previously made a live external API call on
// every render: Mainstream's six fixed TMDB rows, every enabled custom slider,
// and the Trakt watchlist.
//
// Claude 2026-08-03: the fourth planned source (StashDB/FansDB catalog rows)
// was retired — commit e25274f deleted those browse routes before BE-7 landed.
// Reason: caching a read path that no longer exists would be dead code.
// Troubleshooting: if you expected stashboxrows.go, see the plan header note.
// Review if: Adult Discover re-adds structural stash-box catalog rows.
//
// The package holds BOTH the cache Store (store.go) and the scheduler that
// fills it. That is internal/adultnewest's exact shape (ReleaseStore + Run in
// one package, with internal/api importing it for the read path) rather than a
// two-package split this codebase has no precedent for.
//
// # The read contract, in one paragraph
//
// A read handler consults the cache AFTER every existing precondition check has
// passed and immediately before its external call. A hit inside the cached
// window serves the slice with zero external calls; ANY miss falls through to
// the existing live handler, unchanged. A cold install therefore behaves
// exactly as it does today — an empty table is not a regression, the same
// contract 0045_adult_merged_row_cache.sql recorded. See Entry.Slice for the
// window arithmetic and store.go's doc comments for the write-path invariant
// that makes the fall-through reliable.
//
// # What this package deliberately does NOT import
//
// Not internal/api (import cycle) and not internal/mode, whose mode.Build
// constructs a cache-ENABLED tmdb.Client. Clients are built straight from
// connections.Store, exactly as recheck.buildProwlarr/recheck.buildTMDB do, and
// the TMDB one sets tmdb.Config.BypassCache so the scheduler's traffic neither
// reads nor writes the package-level LRU in internal/tmdb/cache.go.
//
// # Safety boundary
//
// This is read-only content caching. It never proposes, applies, grabs,
// dispatches, renames, deletes or writes anything outside its own cache table,
// creates no AutoGrabTrigger and cannot reach a mutating call site by
// construction — so unlike internal/scanschedule it carries no compile-time
// allowlist test, and the staged-for-approval invariant is untouched.
package discoverrefresh

import (
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/trakt"
)

// Cached accumulation depth, per pagination ENGINE rather than per source —
// the two engines have genuinely different trigger shapes and one number does
// not fit both.
//
// These are MINIMUM ACCUMULATION DEPTHS for the write path, NOT served-page
// counts, and they must never appear on the read path. The number of pages a
// cached row actually serves is ceil(len(payload)/page_size), which may EXCEED
// these constants once the US-release filter over-delivers: a run that consumed
// 5 raw TMDB pages can hold ~70 survivors, i.e. 4 logical pages against
// carouselCachedPages = 3. A read path that gated on the constant instead of
// the row's own width would skip the cache for page 4 and re-fetch an upstream
// page whose survivors are already cached — deterministic duplication at the
// window boundary. They are unexported precisely so that gate cannot compile
// from internal/api; that is a deliberate guard, not an accident of naming.
const (
	// carouselCachedPages covers the SCROLL-driven rows: the six TMDB
	// categories and every custom slider, all rendered by
	// frontend/src/components/Carousel.tsx, which fires onLoadMore on scroll
	// within LOAD_MORE_THRESHOLD_PX of the trailing edge. Three pages is ~60
	// cards of deliberate horizontal scrolling and is cheap at a 24h cadence,
	// so the window is widened rather than argued about.
	carouselCachedPages = 3

	// stripCachedPages covers the CLICK-driven rows: the five stash-box
	// catalog rows, rendered by PaginatedStrip, whose page 2 needs an explicit
	// "Show more" click. That is exactly the trigger shape CLAUDE.md's
	// 2026-07-30 correction already blessed for the Prowlarr carve-out, so
	// matching it costs nothing and inherits its reasoning.
	//
	// Review if: any stash-box row gains PaginatedStrip's infiniteScroll prop
	// (none passes it today) — it then becomes scroll-driven and should take
	// carouselCachedPages instead.
	stripCachedPages = 1
)

// Upstream page widths. Both are the fixed 20 their callers already hardcode:
// TMDB's list endpoints, and the frontend's own &perPage=20 at all three
// stash-box call sites (which also matches stashbox.defaultBrowsePerPage).
//
// Neither is a slider's page size. A mixed-target slider that reaches
// fetchFixedFeed serves 40 items per LOGICAL page (a movie page concatenated
// with a TV page); see sliders.go's sliderPageSize, which is filter-type aware
// because a mixed studio/network slider degrades to a single 20-item catalog.
// That per-row value is what gets written to the page_size column.
const (
	tmdbPageSize    = 20
	stashBoxPerPage = 20
)

// maxUnreleasedFilterRetriesMirror mirrors internal/api's unexported
// maxUnreleasedFilterRetries (internal/api/discover.go), which cannot be
// imported here without a cycle.
//
// KEEP IN STEP with that constant — the same explicit-replication convention
// internal/adultnewest's normalizeAdultQuery and optionalConnAPI already use.
// It exists only to size maxRawPages: the scheduler deliberately does NOT
// reproduce filterReleasedMovies' retry-page logic (that exists solely to
// satisfy the frontend's one-click-equals-one-page contract, and reproducing it
// is what would freeze the duplicate-item defect discover.go's ACCEPTED
// LIMITATION note records).
const maxUnreleasedFilterRetriesMirror = 3

// maxRawPages hard-bounds how many RAW upstream pages one key's accumulation
// may consume, so a pathological filter run cannot walk an upstream
// indefinitely. Sized as the depth target plus the same slack the live filter
// path allows itself.
const maxRawPages = carouselCachedPages + maxUnreleasedFilterRetriesMirror

// outboundTimeout bounds every call a refresh cycle makes — this package's own
// copy of cmd/sakms/main.go's outboundTimeout, matching internal/recheck's and
// internal/adultnewest's identical local copies so this package shares no
// import with them for one duration.
const outboundTimeout = 15 * time.Second

// Deps bundles everything the refresh paths need, so Run, RefreshAll,
// TriggerOnce, RefreshSlider and RefreshTrakt all take one struct rather than
// seven positional parameters.
//
// Note there is no *tmdb.Client field: each path builds its own from ConnStore
// with tmdb.Config.BypassCache set (see the package doc), because a client
// carrying a bypass flag must never be confused with one built by mode.Build.
type Deps struct {
	HTTPClient    *http.Client
	ConnStore     *connections.Store
	SettingsStore *settings.Store
	SlidersStore  *discoversliders.Store
	TraktStore    *trakt.Store
	// TraktBaseURL is trakt.DefaultBaseURL in production and an httptest URL
	// in tests — this package never reaches Trakt through a global.
	TraktBaseURL string
	Cache        *Store
}
