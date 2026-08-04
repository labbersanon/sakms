package adultnewest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/rssfeed"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
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

// genderBackfillFailureThreshold is the gender backfill's consecutive-failure
// circuit breaker (see backfillPerformerGenders), mirroring the now-deleted
// adultmergecache/precompute.go's identical pattern: after this many
// back-to-back PerformerGender errors, the backfill aborts for the rest of the
// cycle rather than grinding through the whole NULL queue against boxes that
// are clearly down. Every remaining row stays NULL (safe/resumable — retried
// next boot poll), so a boot-time box outage becomes "do nothing this cycle,"
// not "burn the whole queue to ''." A single success anywhere resets the count.
const genderBackfillFailureThreshold = 20

// FeedIntervalSettingKey holds the FEED pass's cadence, in whole seconds — a
// deliberate new tunable, distinct from IntervalSettingKey (the 24h browse
// pass). The feed pass runs on its own ticker with its own budget, which is what
// makes browse-vs-feed identify-budget starvation (M2) structurally impossible.
const FeedIntervalSettingKey = "adult_newest_feed_interval_seconds"

// defaultFeedIntervalSeconds is the feed pass's cadence when
// FeedIntervalSettingKey has never been set — 30 min (D7). Under the health+TTL
// gate this governs new-content latency, feed-health-change detection, and the
// last_confirmed_seen refresh cadence (which must stay ≪ feedAvailabilityTTL —
// 30 min ≪ 14 days, with enormous margin), NOT the availability staleness bound.
const defaultFeedIntervalSeconds = 1800

// feedMaxNewPerCycle bounds how many newly-seen FEED items get run through the
// identify pipeline per feed cycle — the feed pass's own budget, never shared
// with the browse pass's maxNewPerCycle (M2). Feeds are small (tens of items),
// so this drains a feed's backlog in a cycle or two.
const feedMaxNewPerCycle = 25

// LoadFeedInterval reads FeedIntervalSettingKey and returns it as a Duration,
// defaulting to defaultFeedIntervalSeconds when genuinely unset (same
// unset-vs-explicit-0 distinction as LoadInterval): a fresh install polls feeds
// out of the box (harmless when no adult feeds are configured — runFeedCycle
// returns early), an explicit "0" turns it off.
func LoadFeedInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	v, err := settingsStore.Get(ctx, FeedIntervalSettingKey)
	if errors.Is(err, settings.ErrNotFound) {
		return defaultFeedIntervalSeconds * time.Second
	}
	if err != nil {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

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
func Run(ctx context.Context, interval time.Duration, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *ReleaseStore, entityStore parseentity.EntityStore, rssFeedsStore *rssfeeds.Store, feedHealth *FeedHealth) {
	feedInterval := LoadFeedInterval(ctx, settingsStore)
	if interval <= 0 && feedInterval <= 0 {
		return // opt-in gate: both passes off, honoring "manual first"
	}

	httpClient := &http.Client{Timeout: outboundTimeout}

	// Two independent tickers, each with its own enable check and cadence, so
	// disabling one pass never stops the other (M2: feeds and browse never share
	// a ticker or a budget). A nil channel parks its case in the select.
	var browseTicker *time.Ticker
	var browseC <-chan time.Time
	if interval > 0 {
		browseTicker = time.NewTicker(interval)
		defer browseTicker.Stop()
		browseC = browseTicker.C
		log.Printf("adultnewest: background newest-releases (browse) scan enabled (every %s) — a deliberate opt-in exception to manual-by-default, immediate boot poll", interval)
		// Immediate boot poll: fire once before the first interval. The browse
		// pass feeds (and depends on) parseentity's entity cache, and both jobs
		// use a 24h ticker; because this deployment redeploys several times a
		// day, a no-initial-tick browse pass effectively never fired — the
		// original "manual-first, no boot poll" shape produced a real bug (the
		// pass never ran, degrading every Adult identify's match quality).
		// Mirrors the feed pass's boot poll below.
		runCycle(ctx, httpClient, connStore, scStore, settingsStore, releaseStore, entityStore)
	}

	var feedTicker *time.Ticker
	var feedC <-chan time.Time
	if feedInterval > 0 {
		feedTicker = time.NewTicker(feedInterval)
		defer feedTicker.Stop()
		feedC = feedTicker.C
		log.Printf("adultnewest: background feed pass enabled (every %s) — dedicated ticker/budget, immediate boot poll", feedInterval)
		// Immediate boot poll (D7 / Low finding): fire once before the first
		// interval so feed-only Adult content isn't invisible for up to one
		// interval after every restart (the cold-start health map gates all feed
		// sources until the first successful poll). The browse pass now boot-polls
		// too (see its block above) — both passes fire once at startup. The
		// earlier "browse keeps its no-initial-tick shape — do NOT unify them"
		// note was reversed on 2026-07-25: that shape produced a real bug in this
		// fast-redeploy deployment (the browse pass never actually ran, since its
		// 24h ticker never survived to fire between redeploys).
		runFeedCycle(ctx, httpClient, connStore, scStore, settingsStore, releaseStore, entityStore, rssFeedsStore, feedHealth)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-browseC:
			cur := LoadInterval(ctx, settingsStore)
			if cur <= 0 {
				log.Printf("adultnewest: browse interval set to 0 — stopping browse pass (restart to re-enable)")
				browseTicker.Stop()
				browseC = nil
				if feedC == nil {
					return
				}
				continue
			}
			if cur != interval {
				interval = cur
				browseTicker.Reset(cur)
			}
			runCycle(ctx, httpClient, connStore, scStore, settingsStore, releaseStore, entityStore)
		case <-feedC:
			cur := LoadFeedInterval(ctx, settingsStore)
			if cur <= 0 {
				log.Printf("adultnewest: feed interval set to 0 — stopping feed pass (restart to re-enable)")
				feedTicker.Stop()
				feedC = nil
				if browseC == nil {
					return
				}
				continue
			}
			if cur != feedInterval {
				feedInterval = cur
				feedTicker.Reset(cur)
			}
			runFeedCycle(ctx, httpClient, connStore, scStore, settingsStore, releaseStore, entityStore, rssFeedsStore, feedHealth)
		}
	}
}

// runCycle performs exactly one scan pass and returns — extracted from Run's
// ticker loop so tests exercise it directly rather than waiting on a wall
// clock, same convention as recheck.runCycle. Fault isolation matches the
// rest of the codebase: a missing Prowlarr/Identify config skips the whole
// pass (nothing to scan with/against), and a single release's processing
// failure is logged and skipped without aborting the others.
func runCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *ReleaseStore, entityStore parseentity.EntityStore) {
	// Purged independent of whether Prowlarr/Identify are configured right
	// now — cleaning up months-old cache entries shouldn't depend on the
	// feature being actively scannable at this exact moment (e.g. a
	// connection was temporarily removed), and it's cheap regardless.
	if n, err := releaseStore.PurgeStale(ctx, time.Now().AddDate(0, -staleAfterMonths, 0)); err != nil {
		log.Printf("adultnewest: purging stale entries: %v", err)
	} else if n > 0 {
		log.Printf("adultnewest: purged %d stale matched entities (older than %d months)", n, staleAfterMonths)
	}

	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil {
		log.Printf("adultnewest: building adult session: %v", err)
		return
	}
	if sess.Prowlarr == nil {
		return // prowlarr not configured — nothing to scan
	}
	// Inject the entity store so the DB-first parser is available. Identify is
	// always non-nil for Adult now; the real gate is whether it has any parsing
	// capability (EntityStore or AI).
	if sess.Identify != nil {
		sess.Identify.EntityStore = entityStore
	}
	if sess.Identify == nil || (sess.Identify.EntityStore == nil && sess.Identify.AI == nil) {
		return // no parsing backend configured (entity store or BYOAI) — nothing to identify against
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

	// Gender backfill — the LAST step of the cycle, after the browse pass, reusing
	// THIS cycle's sess.Identify (no new Identifier/Throttle is constructed). That
	// reuse is load-bearing: it shares the browse lane's single Throttle by
	// construction, adding no independently-rate-limited concurrent lane to the
	// external boxes. A separately-built Identifier here would double box load and
	// recreate the exact failure shape this design exists to avoid.
	if err := backfillPerformerGenders(ctx, sess.Identify, releaseStore); err != nil {
		log.Printf("adultnewest: backfilling performer genders: %v", err)
	}
}

// backfillPerformerGenders drains the NULL-gender performer work queue
// (ReleaseStore.UngenderedPerformers) using the distinguishable
// identify.Identifier.PerformerGender resolver. Its whole reason to exist is
// the error-vs-no-match distinction PerformerImage cannot make:
//
//   - err != nil  → the boxes could not answer (throttle-abort/box error/all
//     unreachable). LEAVE THE ROW NULL (never write '') so it retries next
//     cycle, and bump the consecutive-failure counter.
//   - err == nil  → a box was reached. Reset the counter and UpdateGender with
//     g, which MAY be '' — a genuine "reached, no gender on file" negative that
//     is safe to persist (such a performer is simply excluded from every
//     gender-split view, matching the legacy merged-card behavior).
//
// The consecutive-failure circuit breaker (genderBackfillFailureThreshold)
// aborts the whole pass once the boxes are clearly down, leaving every
// remaining row NULL (safe/resumable) rather than churning the entire queue
// against a dead box. A DB write failure is logged but does NOT trip the box
// breaker (it isn't a box outage) and does NOT reset it either — the counter
// tracks box reachability alone, and the row simply stays NULL to retry.
func backfillPerformerGenders(ctx context.Context, id *identify.Identifier, releaseStore *ReleaseStore) error {
	queue, err := releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		return err
	}
	consecutiveFailures := 0
	for _, row := range queue {
		g, err := id.PerformerGender(ctx, row.Name)
		if err != nil {
			consecutiveFailures++
			log.Printf("adultnewest: resolving gender for performer %q (id=%d): %v — left unresolved, will retry next cycle", row.Name, row.ID, err)
			if consecutiveFailures >= genderBackfillFailureThreshold {
				log.Printf("adultnewest: gender backfill aborted after %d consecutive failures — remaining performers stay unresolved for next cycle", consecutiveFailures)
				return nil
			}
			continue
		}
		consecutiveFailures = 0
		if err := releaseStore.UpdateGender(ctx, row.ID, g); err != nil {
			log.Printf("adultnewest: updating gender for performer %q (id=%d): %v", row.Name, row.ID, err)
		}
	}
	return nil
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
// id.StudioImage/PerformerImage).
//
// US-1 orphan prevention (2026-07-30): those Studio/Performer rows are appended
// ONLY inside the branch that actually persisted a scene/movie row to out — i.e.
// only when confirmAvailable succeeded in the detail.Scene case, or in the
// movie-catalog fallback — never unconditionally after the switch. Previously
// they were appended regardless, which produced orphaned Studio/Performer cards
// with zero linked scenes whenever the scene/movie itself wasn't cached (the root
// cause the deep-interview spec was scoped against). A studio/performer identity a
// release can confidently derive is worthless as a Discover card if no scene of
// theirs was actually surfaced alongside it.
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
			// Studio/Performer side-entities are appended ONLY here, inside the
			// branch that actually persisted a scene/movie row — never
			// unconditionally after the switch (US-1 orphan prevention). A
			// confirmable studio/performer name whose triggering scene never
			// became a row must not leave an orphaned card behind.
			out = append(out, identifyStudioPerformers(ctx, id, *detail)...)
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
				// Same US-1 gate as the scene branch: only append the
				// studio/performer rows now that a movie row was persisted.
				out = append(out, identifyStudioPerformers(ctx, id, *detail)...)
			}
		}
	}

	return out, nil
}

// identifyStudioPerformers extracts the Studio and Performer side-entities a
// scene identification already derived (see IdentifyDetailed's doc comment),
// each as its own confirmed cache row — shared by the browse pass (matchRelease)
// and the feed pass (processFeedItem), which capture them identically.
//
// A Studio/Performer row is only cached when StudioImage/PerformerImage actually
// confirms the name against a real StashDB/FansDB/TPDB entity (source != ""),
// the same "decline rather than fabricate" bar found live during this feature's
// deploy verification (verifyStudio/verifyPerformers fall back to the AI's raw
// guess when nothing matches — fine for building a search term, wrong for a
// user-visible Discover card; a live scan once produced cards for "And",
// "Clouds", and a mis-parsed scene title, none of which resolved to any image).
//
// These entities carry NO feed enclosure and are never feed-gated, so they are
// always BrowseConfirmed=true — i.e. always visible via FeedHealth.Available,
// regardless of which pass identified them (matching the 0044 migration's
// backfill of every pre-existing row to browse_confirmed = true).
func identifyStudioPerformers(ctx context.Context, id *identify.Identifier, detail identify.DetailedMatch) []MatchedRelease {
	var out []MatchedRelease
	if detail.StudioName != "" {
		image, source := id.StudioImage(ctx, detail.StudioName)
		if source != "" {
			out = append(out, MatchedRelease{
				RowType: RowStudio,
				// TPDB/StashDB/FansDB catalog ids aren't available from
				// verifyStudio's corrected-name-only result, so the corrected
				// name itself is used as the entity id — stable enough for
				// display/dedup even though it isn't a real opaque catalog id.
				EntityID:        detail.StudioName,
				EntitySource:    source,
				EntityTitle:     detail.StudioName,
				EntityImage:     image,
				BrowseConfirmed: true,
			})
		}
	}
	for _, performer := range detail.Performers {
		if performer == "" {
			continue
		}
		image, source, gender := id.PerformerImage(ctx, performer)
		if source == "" {
			continue
		}
		out = append(out, MatchedRelease{
			RowType:         RowPerformer,
			EntityID:        performer,
			EntitySource:    source,
			EntityTitle:     performer,
			EntityImage:     image,
			Gender:          gender,
			BrowseConfirmed: true,
		})
	}
	return out
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
	// Clean the raw scene-release title the same way autoGrabSearch does before
	// querying Prowlarr, so this really does run the SAME search a later Grab
	// click would (see identify.CleanReleaseTitleForSearch): a verbatim noisy
	// release title matches a different, wrong set of releases than its cleaned
	// form, so confirming availability on the noisy query would confirm a
	// release the actual grab path can no longer find.
	query := normalizeAdultQuery(identify.CleanReleaseTitleForSearch(strings.TrimSpace(releaseTitle)))
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
	var performers []string
	if m.Performers != "" {
		performers = strings.Split(m.Performers, ",")
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
		Performers:            performers,
		// Scene/Movie rows never carry gender (only RowPerformer rows do — see
		// identifyStudioPerformers) — left explicit rather than relying on the
		// zero value, since gender is otherwise easy to mistake as "not yet
		// wired" here.
		Gender: "",
		// The browse pass is the explicit provenance signal D5's visibility gate
		// depends on: every entity the cat-6000 browse pass identifies+confirms
		// is browse-confirmed, so it stays visible regardless of any feed's
		// health. Empty feed fields (feed_id=0, download_url='') — the feed pass
		// builds those via toFeedMatchedRelease instead.
		BrowseConfirmed: true,
	}
}

// toFeedMatchedRelease builds a FEED-sourced scene/movie cache row: the same
// identified metadata as the browse pass's toMatchedRelease, but with
// BrowseConfirmed=false (the feed pass never asserts browse confirmation — the
// M1 upsert MERGEs it, so a browse-then-feed entity keeps its browse
// confirmation) and the feed's own enclosure (download_url/protocol/size),
// feed_id, feed_item_key (D6), and last_confirmed_seen=now attached. No
// confirmAvailable Prowlarr search runs for a feed item — the enclosure IS the
// availability proof (D4).
func toFeedMatchedRelease(rowType RowType, m identify.MatchResult, releaseTitle string, feedID int64, downloadURL, protocol, feedItemKey string, sizeBytes, nowUnix int64) MatchedRelease {
	mr := toMatchedRelease(rowType, m, releaseTitle)
	mr.BrowseConfirmed = false
	mr.DownloadURL = downloadURL
	mr.DownloadProtocol = protocol
	mr.SizeBytes = sizeBytes
	mr.FeedID = feedID
	mr.FeedItemKey = feedItemKey
	mr.LastConfirmedSeen = nowUnix
	return mr
}

// feedItemKey derives a stable per-item key (D6): the enclosure URL when
// present, else the item link, else a hash of feed id + title. The SAME
// derivation produces the adult_newest_seen dedup key, the cache row's
// feed_item_key, and the per-poll presence key set the last-seen bulk refresh
// keys on — so a persisted row and the poll that confirms it always agree.
func feedItemKey(feedID int, it rssfeed.Item) string {
	if it.EnclosureURL != "" {
		return it.EnclosureURL
	}
	if it.Link != "" {
		return it.Link
	}
	sum := sha256.Sum256([]byte(strconv.Itoa(feedID) + "\x00" + it.Title))
	return "feed:" + strconv.Itoa(feedID) + ":" + hex.EncodeToString(sum[:])
}

// runFeedCycle performs exactly one FEED pass and returns — the feed-sourced
// sibling of runCycle, run on its own ticker/budget (D7). It lists enabled
// Adult feeds, prunes stale health entries, and for each reachable feed marks it
// healthy, refreshes last_confirmed_seen across its FULL current window, then
// identifies up to feedMaxNewPerCycle newly-seen items and upserts them with the
// feed's enclosure. Per-feed fault isolation: a fetch error marks only that feed
// unhealthy and skips it, never aborting the others. Strictly sequential (no
// concurrency), preserving the "Discover never queries Prowlarr / no fan-out"
// safety shape. No confirmAvailable call for feed items (D4).
func runFeedCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *ReleaseStore, entityStore parseentity.EntityStore, rssFeedsStore *rssfeeds.Store, feedHealth *FeedHealth) {
	feeds, err := rssFeedsStore.List(ctx)
	if err != nil {
		log.Printf("adultnewest: listing rss feeds for feed pass: %v", err)
		return
	}
	var adultFeeds []rssfeeds.Feed
	enabledIDs := make([]int64, 0, len(feeds))
	for _, f := range feeds {
		if f.Target == rssfeeds.TargetAdult && f.Enabled {
			adultFeeds = append(adultFeeds, f)
			enabledIDs = append(enabledIDs, int64(f.ID))
		}
	}
	// Drop health entries for feeds disabled/deleted since the last cycle so a
	// removed feed stops reading healthy (a missing entry is unhealthy).
	feedHealth.PruneToEnabled(enabledIDs)
	if len(adultFeeds) == 0 {
		return
	}

	// Build one Adult session for the identify pipeline (no Prowlarr needed — a
	// feed item's enclosure is its own availability proof). Same entity-store
	// injection + parsing-backend gate as runCycle.
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil {
		log.Printf("adultnewest: building adult session for feed pass: %v", err)
		return
	}
	if sess.Identify != nil {
		sess.Identify.EntityStore = entityStore
	}
	if sess.Identify == nil || (sess.Identify.EntityStore == nil && sess.Identify.AI == nil) {
		return // no parsing backend configured — nothing to identify against
	}

	for _, f := range adultFeeds {
		items, err := rssfeed.FetchItems(ctx, httpClient, f.FeedURL)
		if err != nil {
			feedHealth.MarkUnhealthy(int64(f.ID))
			log.Printf("adultnewest: fetching feed %q (id=%d): %v — feed marked unhealthy this cycle", f.Title, f.ID, err)
			continue
		}
		now := time.Now()
		feedHealth.SetHealthy(int64(f.ID), now)

		// Full-window presence keys — the durable TTL-clock refresh (writer (ii),
		// D3): advance last_confirmed_seen for EVERY already-cached row still
		// present in this poll, independent of the identify budget below, so a
		// within-window over-budget or upsert-no-op row never TTL-expires while
		// its feed is healthy.
		keys := make([]string, 0, len(items))
		for _, it := range items {
			keys = append(keys, feedItemKey(f.ID, it))
		}
		if err := releaseStore.RefreshLastConfirmedSeen(ctx, int64(f.ID), keys, now.Unix()); err != nil {
			log.Printf("adultnewest: refreshing last_confirmed_seen for feed %q: %v", f.Title, err)
		}

		seen, err := releaseStore.SeenGUIDs(ctx, keys)
		if err != nil {
			log.Printf("adultnewest: checking seen feed items for %q: %v", f.Title, err)
			continue
		}

		processed := 0
		for _, it := range items {
			if processed >= feedMaxNewPerCycle {
				break
			}
			key := feedItemKey(f.ID, it)
			if key == "" || seen[key] {
				continue
			}
			processed++
			if err := processFeedItem(ctx, sess.Identify, releaseStore, f, it, key, now.Unix()); err != nil {
				log.Printf("adultnewest: processing feed item %q: %v", it.Title, err)
			}
			// Marked seen regardless of match outcome (same as the browse pass)
			// so an unmatched feed item isn't re-identified every cycle.
			if err := releaseStore.MarkSeen(ctx, key); err != nil {
				log.Printf("adultnewest: marking feed item %q seen: %v", it.Title, err)
			}
		}
	}
}

// processFeedItem identifies one feed item and upserts every entity it resolved
// to — a scene/movie with the feed's enclosure attached (browse_confirmed = false),
// plus its studio/performers (browse-confirmed, no enclosure, shared with the
// browse pass). The D3 upsert merges browse_confirmed, so if the browse pass
// already confirmed this entity that fact is preserved. No confirmAvailable.
//
// US-1 orphan prevention (2026-07-30): as in matchRelease, the studio/performer
// rows are appended ONLY inside the branch that actually persisted a scene/movie
// row (here that's simply "a scene or a movie was found," since the feed pass has
// no confirmAvailable gate), never unconditionally after the switch — so a feed
// item that resolves to a confirmable studio/performer but no scene/movie leaves
// no orphaned card behind.
func processFeedItem(ctx context.Context, id *identify.Identifier, releaseStore *ReleaseStore, f rssfeeds.Feed, it rssfeed.Item, key string, nowUnix int64) error {
	detail, err := id.IdentifyDetailed(ctx, it.Title, "")
	if err != nil {
		return err
	}
	// Enclosure URL, falling back to the item link when absent — matching
	// resolveRssFeedHandler's own fallback.
	downloadURL := it.EnclosureURL
	if downloadURL == "" {
		downloadURL = it.Link
	}

	var out []MatchedRelease
	switch {
	case detail.Scene != nil:
		rowType := RowScene
		if detail.Scene.Type == "movie" {
			rowType = RowMovie
		}
		out = append(out, toFeedMatchedRelease(rowType, *detail.Scene, it.Title, int64(f.ID), downloadURL, string(f.Protocol), key, it.EnclosureLength, nowUnix))
		// US-1 orphan prevention: studio/performer side-entities are appended
		// ONLY inside the branch that actually persisted a scene/movie row (the
		// feed pass has no confirmAvailable gate, so "a scene/movie was found" is
		// the whole condition here), never unconditionally after the switch.
		out = append(out, identifyStudioPerformers(ctx, id, *detail)...)
	default:
		// No scene match — try TPDB's movie catalog directly, same lighter-weight
		// fallback the browse pass uses.
		if movie, err := id.Boxes.SearchTPDBMovies(ctx, it.Title); err == nil && movie != nil {
			out = append(out, toFeedMatchedRelease(RowMovie, *movie, it.Title, int64(f.ID), downloadURL, string(f.Protocol), key, it.EnclosureLength, nowUnix))
			// Same US-1 gate as the scene branch above.
			out = append(out, identifyStudioPerformers(ctx, id, *detail)...)
		}
	}

	for _, m := range out {
		if err := releaseStore.Insert(ctx, m); err != nil {
			return err
		}
	}
	return nil
}
