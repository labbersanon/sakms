package adultnewest

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// IntervalSettingKey is the settings key holding the scan cadence, in whole
// seconds. internal/api mirrors this string in its own GET/PUT
// /api/settings/adult-newest-scan-interval handler rather than importing
// this package, same import-avoidance rationale as recheck.IntervalSettingKey.
//
// Unlike recheck (off by default, opt-in), this job defaults ON at
// defaultIntervalHours when the key has never been set — an explicit
// operator directive (2026-07-15), a deliberate deviation from this
// project's usual "manual by default" convention for this one feature.
// Setting the key to 0 explicitly still means off; only the genuinely-unset
// case (a fresh install, or an install from before this default existed)
// gets the new default (see LoadInterval).
const IntervalSettingKey = "adult_newest_scan_interval_seconds"

// defaultIntervalHours is the scan cadence used when IntervalSettingKey has
// never been set — 24 hours per operator directive (2026-07-15).
const defaultIntervalHours = 24

// staleAfterMonths is how long a matched entity or seen-release record can
// sit before the periodic purge (see purgeStale) removes it — 6 months per
// the same operator directive. A purged entity isn't gone forever: if a
// future release still resolves to it, it's simply re-matched and re-cached
// (a soft refresh, since re-matching re-fetches current image/tags rather
// than reusing months-old data). A purged "seen" release guid is likewise
// just eligible to be reprocessed if it somehow still appears in Prowlarr's
// feed — extremely unlikely for a 6-month-old release, but harmless if it
// does.
const staleAfterMonths = 6

// outboundTimeout bounds every call this cycle makes — its own copy of
// cmd/sakms/main.go's outboundTimeout, matching internal/recheck's
// self-contained convention.
const outboundTimeout = 15 * time.Second

// adultCategory is the Newznab/Torznab XXX category code — mirrors
// internal/api/autograb.go's adultAutoGrabCategory (kept as an independent
// copy rather than an import, since internal/api doesn't export it and this
// package shouldn't depend on internal/api anyway).
const adultCategory = 6000

// maxNewPerCycle bounds how many newly-seen releases get run through the
// per-release identify pipeline (an AI call plus several StashDB/FansDB/TPDB
// lookups) in a single cycle. internal/recheck has no equivalent cap because
// its per-item probe is one cheap HTTP call; this job's per-item cost is far
// higher, so an unbounded "process everything new" loop could turn a single
// tick — e.g. after the feature is first enabled, when every current release
// is "new" — into dozens of AI calls back to back. Processing is also
// strictly sequential (see runCycle below), never concurrent: that, plus this
// cap, is what keeps this job on the safe side of the failure shape CLAUDE.md's
// "Discover never queries Prowlarr" rule exists to prevent (see this
// package's doc comment).
const maxNewPerCycle = 25

// LoadInterval reads IntervalSettingKey and returns it as a Duration.
// Genuinely unset (never saved — a fresh install, or an existing install
// from before this default existed) returns defaultIntervalHours, not off —
// see IntervalSettingKey's doc comment for why this deliberately differs
// from recheck.LoadInterval's off-by-default convention. A key that WAS
// explicitly saved as "0" (an operator turning the feature off via Settings)
// still means off — that distinction is exactly what separates the
// settings.ErrNotFound branch below from the "stored but non-positive"
// branch. A blank/non-integer stored value degrades to off, not the
// default, since that shape only happens via direct DB tampering, not
// through the Settings UI (which only ever sends an integer) — treating it
// as "explicitly disabled" is the safer read of a value that shouldn't
// exist.
func LoadInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	v, err := settingsStore.Get(ctx, IntervalSettingKey)
	if errors.Is(err, settings.ErrNotFound) {
		return defaultIntervalHours * time.Hour
	}
	if err != nil {
		return 0 // a real store error degrades to off, not a guessed default
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// Run drives the background scan loop until ctx is cancelled — mirrors
// recheck.Run's shape (ticker, live-retune via settings, context-cancellation
// shutdown), with one deliberate difference: interval <= 0 stops/skips the
// loop the same way it does for recheck, but here that only happens when an
// operator has explicitly saved "0" via Settings — a fresh install's
// interval defaults to defaultIntervalHours, not off (see
// IntervalSettingKey's doc comment), so this job runs out of the box unlike
// every other background job in this codebase.
func Run(ctx context.Context, interval time.Duration, connStore *connections.Store, settingsStore *settings.Store, releaseStore *ReleaseStore) {
	if interval <= 0 {
		return // opt-in gate: off by default, honoring "manual first"
	}

	httpClient := &http.Client{Timeout: outboundTimeout}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("adultnewest: background newest-releases scan enabled (every %s) — a deliberate opt-in exception to manual-by-default", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := LoadInterval(ctx, settingsStore)
			if cur <= 0 {
				log.Printf("adultnewest: interval set to 0 — stopping background scan (restart to re-enable)")
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}
			runCycle(ctx, httpClient, connStore, settingsStore, releaseStore)
		}
	}
}

// runCycle performs exactly one scan pass and returns — extracted from Run's
// ticker loop so tests exercise it directly rather than waiting on a wall
// clock, same convention as recheck.runCycle. Fault isolation matches the
// rest of the codebase: a missing Prowlarr/Identify config skips the whole
// pass (nothing to scan with/against), and a single release's processing
// failure is logged and skipped without aborting the others.
func runCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, releaseStore *ReleaseStore) {
	// Purged independent of whether Prowlarr/Identify are configured right
	// now — cleaning up months-old cache entries shouldn't depend on the
	// feature being actively scannable at this exact moment (e.g. a
	// connection was temporarily removed), and it's cheap regardless.
	if n, err := releaseStore.PurgeStale(ctx, time.Now().AddDate(0, -staleAfterMonths, 0)); err != nil {
		log.Printf("adultnewest: purging stale entries: %v", err)
	} else if n > 0 {
		log.Printf("adultnewest: purged %d stale matched entities (older than %d months)", n, staleAfterMonths)
	}

	sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, mode.Adult)
	if err != nil {
		log.Printf("adultnewest: building adult session: %v", err)
		return
	}
	if sess.Prowlarr == nil {
		return // prowlarr not configured — nothing to scan
	}
	if sess.Identify == nil {
		return // AI identify pipeline not configured (needs an AI provider + at least one of StashDB/FansDB/TPDB) — nothing to match against
	}

	// Bare category browse, no query term — Prowlarr/Torznab's native
	// "recent releases in this category" behavior (verified live against a
	// real Prowlarr instance this session: 271 results, sorted by recency,
	// via categories=6000 with no query param).
	releases, err := sess.Prowlarr.Search(ctx, "", []int{adultCategory})
	if err != nil {
		log.Printf("adultnewest: searching prowlarr for newest releases: %v", err)
		return
	}
	if len(releases) == 0 {
		return
	}

	guids := make([]string, len(releases))
	for i, r := range releases {
		guids[i] = r.GUID
	}
	seen, err := releaseStore.SeenGUIDs(ctx, guids)
	if err != nil {
		log.Printf("adultnewest: checking seen release guids: %v", err)
		return
	}

	processed := 0
	for _, r := range releases {
		if processed >= maxNewPerCycle {
			break
		}
		if seen[r.GUID] || r.GUID == "" {
			continue
		}
		processed++
		if err := processRelease(ctx, sess.Identify, sess.Prowlarr, releaseStore, r); err != nil {
			log.Printf("adultnewest: processing release %q: %v", r.Title, err)
		}
		// Marked seen regardless of match outcome (including a processing
		// error) — an unmatched or failed release must not be retried every
		// cycle just because it produced no cache row; see MarkSeen's doc.
		if err := releaseStore.MarkSeen(ctx, r.GUID); err != nil {
			log.Printf("adultnewest: marking release %q seen: %v", r.Title, err)
		}
	}
}

// processRelease identifies one Prowlarr release and writes every entity it
// resolved to (a scene or movie, plus its studio and any performers) to
// releaseStore. A release that resolves to nothing is simply not written —
// it never appears on Discover, only through the existing manual Search
// screen (see this package's doc comment and the operator's explicit
// instruction this feature was scoped against).
func processRelease(ctx context.Context, id *identify.Identifier, prowlarrClient *prowlarr.Client, releaseStore *ReleaseStore, r prowlarr.Release) error {
	matches, err := matchRelease(ctx, id, prowlarrClient, r)
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := releaseStore.Insert(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// matchRelease tries a scene match first (the existing, most-verified
// pipeline: AI parse + studio/performer correction + StashDB/FansDB/TPDB
// scene lookup), then a TPDB movie-catalog match if no scene matched. Studio
// and Performer identities the scene pipeline already derived (but discarded
// after using them to build the scene search term) are captured too — see
// identify.Identifier.IdentifyDetailed's doc comment — each getting its own
// Studio/Performer cache entry (with its own poster art, fetched via
// id.StudioImage/PerformerImage) independent of whether the scene match
// itself succeeded, since a release's studio/performer identity can be
// confidently derived even when the specific scene isn't in any configured
// database yet.
//
// Scene/Movie rows additionally require confirmAvailable to pass before
// being cached — found live in production, 2026-07-15: this pipeline
// deliberately dedups by ENTITY, not by the specific release that triggered
// the match (see the migration's doc comment — several releases can
// resolve to the same scene), so only the FIRST matching release's identity
// is retained (MatchedRelease.FirstSeenReleaseTitle). Before that field
// existed, a later Grab click had no choice but to re-search Prowlarr from
// scratch using the matched entity's CANONICAL TPDB title+studio — a
// fundamentally different, much stricter query than the raw release title
// IdentifyDetailed's AI-assisted fuzzy pipeline actually matched against,
// and one that could legitimately find zero raw Prowlarr results even when
// the release that triggered the match was real (confirmed live: a studio
// whose content is only ever released as multi-scene compilation packs,
// never as the single scene TPDB catalogs separately). Now that the raw
// release title itself is what both this check and the eventual Grab query
// use, confirmAvailable mainly guards against edge cases (indexer content
// churn between scan and Grab, encoding/category quirks) rather than a
// query-shape mismatch — running the SAME search a later Grab would run,
// right now, still means: if it fails here, don't cache a card Grab can
// never fulfill. Studio/Performer rows are deliberately NOT gated this way:
// EntityCard has no Grab button for this pipeline's matched entities, so
// there's no "will Grab find something" expectation to protect there.
func matchRelease(ctx context.Context, id *identify.Identifier, prowlarrClient *prowlarr.Client, r prowlarr.Release) ([]MatchedRelease, error) {
	detail, err := id.IdentifyDetailed(ctx, r.Title, "")
	if err != nil {
		return nil, err
	}

	var out []MatchedRelease

	switch {
	case detail.Scene != nil:
		rowType := RowScene
		if detail.Scene.Type == "movie" {
			rowType = RowMovie
		}
		if confirmAvailable(ctx, prowlarrClient, r.Title) {
			out = append(out, toMatchedRelease(rowType, *detail.Scene, r.Title))
		}
	default:
		// No scene match — try TPDB's movie catalog directly before giving
		// up on a title/movie identity entirely. This is a lighter-weight
		// match than the scene path (fuzzy title search only, no AI parse/
		// studio correction) — see SearchTPDBMovies' doc comment for why
		// that's an acceptable, honestly-scoped difference from the scene
		// path's full pipeline.
		if movie, err := id.Boxes.SearchTPDBMovies(ctx, r.Title); err == nil && movie != nil {
			if confirmAvailable(ctx, prowlarrClient, r.Title) {
				out = append(out, toMatchedRelease(RowMovie, *movie, r.Title))
			}
		}
	}

	// Studio/Performer rows are only cached when StudioImage/PerformerImage
	// actually confirms the name against a real StashDB/FansDB/TPDB entity
	// (source != ""). This was found live, during this feature's own deploy
	// verification: verifyStudio/verifyPerformers (identify/entityverify.go)
	// fall back to the AI's raw/cleaned guess when nothing matches — a
	// deliberate choice there, since that fallback value was only ever used
	// to build a scene-search term before this feature existed (a wrong
	// guess just made that one search slightly less precise, no visible
	// harm). Using the same fallback to create a user-visible Discover CARD
	// is a different bar: a live scan produced cards for "And", "Clouds",
	// and a full raw scene title mis-parsed as a "studio" — all guesses the
	// AI invented with no real entity behind them, easy to tell apart from
	// genuine matches because none of them resolved to any image anywhere.
	// Skipping the unconfirmed case is the same "decline rather than
	// fabricate" principle rejectNonStudioGuess already applies one layer
	// down — applied here too, one layer up.
	if detail.StudioName != "" {
		image, source := id.StudioImage(ctx, detail.StudioName)
		if source != "" {
			out = append(out, MatchedRelease{
				RowType: RowStudio,
				// TPDB/StashDB/FansDB catalog ids aren't available from
				// verifyStudio's corrected-name-only result, so the
				// corrected name itself is used as the entity id — stable
				// enough for display/dedup purposes even though it isn't a
				// real opaque catalog id (see StudioImage's doc comment for
				// the same name-only lookup convention).
				EntityID:     detail.StudioName,
				EntitySource: source,
				EntityTitle:  detail.StudioName,
				EntityImage:  image,
			})
		}
	}
	for _, performer := range detail.Performers {
		if performer == "" {
			continue
		}
		image, source := id.PerformerImage(ctx, performer)
		if source == "" {
			continue
		}
		out = append(out, MatchedRelease{
			RowType:      RowPerformer,
			EntityID:     performer,
			EntitySource: source,
			EntityTitle:  performer,
			EntityImage:  image,
		})
	}

	return out, nil
}

// adultQueryApostrophe/adultQueryNonAlnum/normalizeAdultQuery are an
// independent local copy of internal/api/autograb.go's identically-named
// normalizeAdultQuery — same convention as adultCategory's copies across
// this package/internal/api/internal/availability (each package's own
// comment explains why: avoiding an import coupling for one small shared
// constant/function). Kept byte-for-byte identical on purpose: this
// package's confirmAvailable search MUST normalize the same way
// autoGrabSearch does, or the two searches aren't really asking the same
// question (see confirmAvailable's doc comment) — if autograb.go's version
// ever changes, mirror the change here too.
var adultQueryApostrophe = regexp.MustCompile(`['’]`)
var adultQueryNonAlnum = regexp.MustCompile(`[^a-zA-Z0-9\s]+`)

func normalizeAdultQuery(s string) string {
	s = adultQueryApostrophe.ReplaceAllString(s, "")
	s = adultQueryNonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// confirmAvailable runs the SAME search a later Grab click would run
// (internal/api/autograb.go's autoGrabSearch: normalized releaseTitle query
// against Prowlarr's Adult category — see MatchedRelease.FirstSeenReleaseTitle's
// doc comment for why the raw release title, not reconstructed studio+title
// metadata, is what both searches use) and reports whether it finds at
// least one raw release — the same permissive "available" bar
// internal/availability.CheckAdultScene uses elsewhere in this codebase
// (any release at all, no seeder/bitrate filtering; DetailPopup's manual
// fallback pick list already handles "found something, but nothing
// auto-qualifies"). Best-effort: a search error reports unavailable rather
// than propagating, since "couldn't confirm" and "confirmed unavailable"
// should both mean "don't cache this" — see matchRelease's doc comment for
// why this check exists at all.
func confirmAvailable(ctx context.Context, prowlarrClient *prowlarr.Client, releaseTitle string) bool {
	query := normalizeAdultQuery(strings.TrimSpace(releaseTitle))
	releases, err := prowlarrClient.Search(ctx, query, []int{adultCategory})
	if err != nil {
		return false
	}
	return len(releases) > 0
}

// toMatchedRelease maps a MatchResult onto the cache row shape, splitting
// MatchResult's comma-joined Tags string back into a slice (see
// identify.MatchResult's doc comment for why it's stored as a single string
// there — this is the one place that string gets parsed back apart).
// releaseTitle is the raw Prowlarr release title that triggered this match
// (r.Title in matchRelease) — see MatchedRelease.FirstSeenReleaseTitle's doc
// comment for why it's captured here rather than reconstructed later.
func toMatchedRelease(rowType RowType, m identify.MatchResult, releaseTitle string) MatchedRelease {
	var genres []string
	if m.Tags != "" {
		genres = strings.Split(m.Tags, ",")
	}
	return MatchedRelease{
		RowType:               rowType,
		EntityID:              m.SceneID,
		EntitySource:          m.Box,
		EntityTitle:           m.Title,
		EntityStudio:          m.Studio,
		EntityImage:           m.Image,
		EntityDate:            m.Date,
		EntityDurationSeconds: m.RuntimeSeconds,
		FirstSeenReleaseTitle: releaseTitle,
		Genres:                genres,
	}
}
