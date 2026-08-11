package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// adultAutoGrabCategory is the XXX (6000-range) Newznab category Adult
// releases live in — the same value availability.CheckAdultScene probes, and
// (since 2026-07-15) the same value categoriesForSearch(mode.Adult) now
// returns too — categoriesForSearch previously had no Adult case and
// silently fell through to the Movies category (2000), a real confirmed bug
// in the manual Search screen, now fixed to share this constant instead of
// hand-rolling its own 6000.
const adultAutoGrabCategory = 6000

// adultQueryApostrophe/adultQueryNonAlnum back normalizeAdultQuery.
// Apostrophes are dropped entirely (so "Don't" -> "Dont", matching how
// scene-release naming conventions usually handle contractions) rather than
// becoming a space, which would split one word into two ("Don t"). Every
// other run of characters that isn't a letter, digit, or whitespace
// (colons, commas, periods, asterisks, parens...) collapses to a single
// space instead.
var (
	adultQueryApostrophe = regexp.MustCompile(`['’]`)
	adultQueryNonAlnum   = regexp.MustCompile(`[^a-zA-Z0-9\s]+`)
)

// normalizeAdultQuery strips punctuation from a studio+title string before
// it becomes a Prowlarr free-text query — see autoGrabSearch's Adult case
// for why: a real production report found the raw, unnormalized text
// (colons, commas, asterisks, apostrophes and all) almost never appears
// verbatim in how trackers actually name Adult releases, so nearly every
// search was returning 0 raw releases. Collapses repeated/leading/trailing
// whitespace too (strings.Fields + Join), so an empty Studio or Title still
// produces a clean single-spaced result.
func normalizeAdultQuery(s string) string {
	s = adultQueryApostrophe.ReplaceAllString(s, "")
	s = adultQueryNonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// adultMinSeeders is Adult's own minimum-seeder auto-grab floor — lower than
// Movies/Series' shared autograb.DefaultMinSeeders (5). Found via a real
// report: a genuine, otherwise-qualifying Adult torrent release (correct
// resolution, comfortably above its bitrate floor) was rejected outright
// because it permanently sits at 1 seeder — niche/older Adult content
// routinely doesn't attract the seeder counts mainstream Movies/TV do.
// Explicitly Adult-only: Movies/Series keep the shared default, since
// lowering it there would be a real reliability regression for content that
// generally IS well-seeded, not a fix for anything reported.
const adultMinSeeders = 3

// minSeedersFor returns the minimum-seeder auto-grab floor for m — shared by
// both the popup's discoverAvailabilityHandler and the real one-click
// autoGrabHandler, so a release that shows as grabbable in the popup grades
// the same way when actually grabbed.
func minSeedersFor(m mode.Mode) int {
	if m == mode.Adult {
		return adultMinSeeders
	}
	return autograb.DefaultMinSeeders
}

// autoGrabHandler is Discover's one-click unattended auto-grab (Stage 2). It
// searches Prowlarr for the requested title/scene, grades every release with
// internal/autograb's bitrate-quality-floor scorer, and either
//
//   - sends the single highest-scored qualifying release straight to the
//     download client (no human release-pick — that IS auto-grab), recording
//     it in grabsStore exactly like grabHandler; or
//   - when nothing clears the floor, returns the ranked candidate list for the
//     frontend's manual pick fallback (never "grab the least-bad option").
//
// Exactly one release is ever grabbed per call: no bulk action, the same
// staged-single-mutation invariant every other SAK workflow keeps.
//
// AMENDED 2026-07-24: "no bulk action" is now scoped to THIS single-item route.
// A bounded, user-approved multi-select sibling — POST /api/autograb-batch
// (autoGrabBatchHandler in autograb_batch.go) — grabs an operator-selected set
// SEQUENTIALLY (at most one Prowlarr search in flight, capped at
// MaxBatchGrabItems). It reuses this exact pipeline (grabOneBatchItem) per item;
// each item is still a single one-release grab, just looped under one request.
// This handler and its "exactly one add" tests are unchanged.
//
// Since the RunAutoGrab extraction it is a thin wrapper around that shared
// mechanism, called with TriggerOperator — the one UNGATED trigger. The
// usenet_autograb_enabled toggle must never apply here: this feature already
// ships, and the operator's click is the approval.
func autoGrabHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		var req apidto.AutoGrabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Direct-grab path (C1/D4): a request carrying its own enclosure URL (an
		// Adult feed item) is dispatched straight to the download client — no
		// Prowlarr search, no candidate scoring, and crucially no Prowlarr/TMDB
		// dependency, so it works on a Prowlarr-less install. Returning here is
		// what relaxes the sess.Prowlarr==nil guard below for this case (the guard
		// stays as-is and only ever runs for the search path). The same enclosure
		// rides the bulk path identically via grabOneBatchItem — one code path.
		if strings.TrimSpace(req.DownloadURL) != "" {
			dto, alreadyGrabbing, status, err := grabDirectEnclosure(ctx, sess, m, settingsStore, nzb, grabsStore, req)
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			if alreadyGrabbing {
				writeAutoGrabJSON(w, apidto.AutoGrabResponse{Grabbed: false, Message: "already grabbing this release", Grab: dto})
				return
			}
			writeAutoGrabJSON(w, apidto.AutoGrabResponse{Grabbed: true, Message: "grabbed " + req.Title, Grab: dto})
			return
		}

		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}
		// Movies/Series both resolve ids/runtime through TMDB; Adult never does.
		if m != mode.Adult && sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		out, err := RunAutoGrab(ctx, AutoGrabDeps{SettingsStore: settingsStore, NZB: nzb, GrabsStore: grabsStore}, sess, AutoGrabRequest{
			Mode: m, Title: req.Title, TMDBID: req.TMDBID,
			Season: req.SeasonNumber, Episode: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
			Studio: req.Studio, ReleaseTitle: req.ReleaseTitle, DurationSeconds: req.DurationSeconds,
			Trigger: TriggerOperator,
		})
		if err != nil {
			http.Error(w, err.Error(), out.Status)
			return
		}

		// Fallback: nothing cleared the floor → hand back the ranked pick list
		// (best bitrate score first, the same score that rejected them all).
		// TriggerOperator deliberately parks NO pending_retry row here: a human
		// is looking at this list and picks one.
		if out.NoMatch {
			writeAutoGrabJSON(w, apidto.AutoGrabResponse{
				Fallback:   true,
				Message:    "nothing cleared the quality floor automatically — pick one below",
				Candidates: rankedAutoGrabCandidates(out.Selection, out.Releases),
			})
			return
		}

		if out.AlreadyGrabbing {
			writeAutoGrabJSON(w, apidto.AutoGrabResponse{Grabbed: false, Message: "already grabbing this release", Grab: out.Grab})
			return
		}

		writeAutoGrabJSON(w, apidto.AutoGrabResponse{
			Grabbed: true,
			Message: "auto-grabbed " + out.Releases[out.Selection.PickIndex].Title,
			Grab:    out.Grab,
		})
	}
}

// grabDirectEnclosure dispatches a request's own feed enclosure URL straight to
// the download client and records the grab — the shared core of BOTH the single
// (autoGrabHandler) and bulk (grabOneBatchItem) direct-grab paths, so a card
// grabbed singly or in bulk takes the identical Prowlarr-free path (C1/D4). No
// Prowlarr search, no candidate scoring; the root folder is resolved
// server-side (a true one-click grab supplies only the enclosure + title), and
// Indexer is stamped "feed" (there is no indexer search to name). Returns the
// recorded grab DTO plus the HTTP status a caller should surface on error.
func grabDirectEnclosure(ctx context.Context, sess *mode.Session, m mode.Mode, settingsStore *settings.Store, nzb *usenet.Manager, grabsStore *grabs.Store, req apidto.AutoGrabRequest) (dto *apidto.Grab, alreadyGrabbing bool, status int, err error) {
	rootFolder, err := autoGrabRootFolder(ctx, settingsStore, m)
	if err != nil {
		return nil, false, http.StatusBadRequest, err
	}
	downloadClient, gid, status, err := dispatchToDownloadClient(ctx, settingsStore, sess, m, nzb, req.DownloadProtocol, req.DownloadURL, req.Title)
	if err != nil {
		return nil, false, status, err
	}
	if existing, dup, status, err := activeGrabForGID(ctx, grabsStore, m, gid); dup || err != nil {
		return existing, dup, status, err
	}
	created, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: m, Title: req.Title, TMDBID: req.TMDBID,
		SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
		Indexer: "feed", Protocol: req.DownloadProtocol,
		DownloadClient: downloadClient, RootFolderPath: rootFolder,
		// DownloadURL: a feed item has no TMDB/Prowlarr identity to re-search
		// from — its enclosure URL is its ONLY provenance (see grabs.Grab.
		// DownloadURL's doc comment). Without it, an async retrieval failure
		// parks a pending_retry row (internal/api/search.go's checkImportHandler
		// -> parkGrabForRetry) with nothing for the retry scheduler to resubmit.
		DownloadURL: req.DownloadURL,
	})
	if err != nil {
		return nil, false, http.StatusInternalServerError, err
	}
	if gid != "" {
		if err := grabsStore.SetDownloadGID(ctx, created.ID, gid); err != nil {
			return nil, false, http.StatusInternalServerError, err
		}
		created.DownloadGID = gid
	}
	out := toDTOGrab(created)
	return &out, false, http.StatusOK, nil
}

// activeGrabForGID is the shared idempotency guard every grab-recording path
// runs after dispatch but before grabsStore.Create: a download client dedupes a
// torrent by its infohash, so a repeat grab of the same release returns the SAME
// gid, and recording a second grabs row for it would strand a duplicate at
// 'queued' (grabs.Store.GetByDownloadGID is first-row-only, so only one row ever
// gets marked imported on completion). When an in-flight grab already holds gid
// it returns that grab's DTO with dup=true so the caller can report "already
// grabbing this release" instead of creating a duplicate; a truly fresh gid
// returns dup=false and the caller proceeds to Create. A prior grab that already
// reached a terminal state (imported/failed) does NOT count — a legitimate
// re-grab after a completed or failed download is allowed (see
// grabs.Store.ActiveByDownloadGID).
func activeGrabForGID(ctx context.Context, grabsStore *grabs.Store, m mode.Mode, gid string) (dto *apidto.Grab, dup bool, status int, err error) {
	existing, err := grabsStore.ActiveByDownloadGID(ctx, m, gid)
	if err != nil {
		if errors.Is(err, grabs.ErrNotFound) {
			return nil, false, http.StatusOK, nil
		}
		return nil, false, http.StatusInternalServerError, err
	}
	out := toDTOGrab(*existing)
	return &out, true, http.StatusOK, nil
}

// autoGrabSearch runs the per-mode Prowlarr search and resolves the known
// pre-grab runtime (seconds) the bitrate scorer needs. Movies/Series probe
// id-scoped (mirroring availability.CheckMovie/CheckSeries); Adult uses a
// free-text query over the XXX category — the raw first-matched release
// title when available (req.ReleaseTitle), else a studio+title
// reconstruction (mirroring availability.CheckAdultScene); that same
// studio+title query is also retried when a releaseTitle with no recognizable
// scene structure returns 0 releases (see the inline note at that branch).
// Runtime: Movies
// from TMDB MovieDetails;
// Adult from the request's DurationSeconds; Series from the picked episode's
// TMDB runtime (seriesEpisodeRuntimeSeconds) for a single-episode grab, or 0
// (unknown → manual pick list) for a whole-season grab, whose runtime is
// ambiguous (see seriesEpisodeRuntimeSeconds). Callers must have already
// confirmed sess.TMDB != nil for Movies/Series.
func autoGrabSearch(ctx context.Context, sess *mode.Session, m mode.Mode, req apidto.AutoGrabRequest) ([]prowlarr.Release, float64, error) {
	switch m {
	case mode.Adult:
		// Prefer the raw Prowlarr release title that first matched this
		// entity (req.ReleaseTitle, see adultnewest.MatchedRelease.
		// FirstSeenReleaseTitle's doc comment) — it's real indexer
		// vocabulary that already matched once, unlike a query reconstructed
		// from TPDB's own Studio+Title metadata, which includes tokens (e.g.
		// TPDB's "S6:E10" episode notation) real release filenames never
		// contain. Found live, 2026-07-15: a "Adult downloads never
		// resolve" report traced to Prowlarr returning 0 raw releases for
		// nearly every scene tried, with adult indexers confirmed
		// configured — the studio+title query (colons, commas, asterisks,
		// apostrophes and all — e.g. "Private Classics Franky Knight: Curvy
		// And Horny, Looking For A Stallion") almost never appears verbatim
		// in how trackers actually name Adult releases. Falls back to
		// Studio+Title when ReleaseTitle is empty — entities matched before
		// this field existed, or a plain TPDB/StashDB/FansDB catalog browse
		// item with no associated Prowlarr release to remember.
		// The pooled ReleaseTitle is a raw scene-release title (studio glued,
		// embedded release date, trailing quality/codec/group suffix) — used
		// verbatim it made Prowlarr return 18 unrelated Cory Chase/Taboo Heat
		// scenes and never the target (confirmed live 2026-07-31), which is
		// what left the Discover availability grid empty. CleanReleaseTitleForSearch
		// strips that noise (see its doc); the Studio+Title fallback is already
		// clean metadata, so it keeps its existing construction untouched.
		studioTitle := strings.TrimSpace(strings.TrimSpace(req.Studio) + " " + strings.TrimSpace(req.Title))
		var rawQuery string
		unverifiableReleaseTitle := false
		if rt := strings.TrimSpace(req.ReleaseTitle); rt != "" {
			rawQuery = identify.CleanReleaseTitleForSearch(rt)
			unverifiableReleaseTitle = identify.ReleaseTitleLacksSceneStructure(rt)
		} else {
			rawQuery = studioTitle
		}
		query := normalizeAdultQuery(rawQuery)
		log.Printf("autoGrabSearch: mode=adult rawQuery=%q query=%q category=%d studio=%q title=%q releaseTitle=%q", rawQuery, query, adultAutoGrabCategory, req.Studio, req.Title, req.ReleaseTitle)
		releases, err := sess.Prowlarr.Search(ctx, query, []int{adultAutoGrabCategory})
		usedStudioTitleQuery := rawQuery == studioTitle
		// Claude 2026-08-11: retry a 0-result releaseTitle query with the
		// Studio+Title query this branch already falls back to when
		// releaseTitle is absent — but ONLY when the stored releaseTitle
		// carried no recognizable scene structure to clean.
		// Reason: the gate is not optional and is not conservatism. The two
		// pinned regression tests above this behaviour
		// (TestDiscoverAvailabilityHandler_Adult_ReleaseTitleQueryCleaned and
		// ..._ReleaseTitlePreferredOverStudioTitle) both serve an EMPTY
		// Prowlarr result set and then assert the query that went out was the
		// cleaned releaseTitle — an ungated 0-result fallback fires a second
		// search in both and breaks them. Restricting the retry to titles
		// identify.ReleaseTitleLacksSceneStructure flags keeps a well-formed
		// title's honest "no releases" answer honest, and only re-asks when
		// the first query was built from something unverifiable.
		// Troubleshooting: Adult Discover's availability grid came back empty
		// for a scene whose pooled releaseTitle was already truncated mid-word
		// with a space-separated date ("CathysCraving 26 02 08 Scene 1000
		// Pixie Smalls Teasing Cheerlea") — nothing in it is a truncation or
		// removal point, so it reached Prowlarr verbatim and matched nothing,
		// while properly-named releases of that scene existed in the indexer.
		// Review if: CleanReleaseTitleForSearch learns to recognize
		// space-separated dates or mid-word truncation directly, or the
		// adultnewest scan pipeline starts storing a non-truncated title
		// (prd.json's out-of-scope item 2) — either makes this gate's
		// population shrink toward empty.
		if err == nil && len(releases) == 0 && unverifiableReleaseTitle {
			if fallback := normalizeAdultQuery(studioTitle); fallback != "" && fallback != query {
				log.Printf("autoGrabSearch: mode=adult releaseTitle query %q returned 0 releases and carries no scene structure — retrying with studio+title query %q", query, fallback)
				releases, err = sess.Prowlarr.Search(ctx, fallback, []int{adultAutoGrabCategory})
				usedStudioTitleQuery = true
			}
		}
		// Claude 2026-08-11 (revised — see CHANGELOG.md for the superseded
		// first attempt): when the effective query came from Studio+Title
		// (directly, or via the 0-result retry above), also search with the
		// studio name DROPPED ENTIRELY (title only), and merge any
		// additional releases found into the result set.
		// Reason: confirmed live against NZBGeek (a real configured Usenet
		// indexer) for a "Cathy's Craving" scene. The first fix attempted
		// here stripped only the studio name's internal whitespace
		// ("Cathys Craving" -> "CathysCraving Scene 1000 ..."), deployed,
		// then re-verified against production logs — it found nothing new:
		// NZBGeek didn't match that query either, only the SAME 2 torrent
		// releases a different, more lenient tracker matched for both the
		// spaced and no-space-studio forms. NZBGeek only matched a query
		// with NO studio token at all ("Scene 1000: teasing cheerleader",
		// confirmed directly in Prowlarr's own search UI) — title-only is
		// the query that actually reproduces that live result. This is
		// ADDITIVE, never a replacement: dropping the studio loses the
		// precision Studio+Title exists for in the first place, so a
		// same-titled release from an unrelated studio can appear in the
		// merged set — but it still has to clear FilterReleases' own
		// title-similarity match downstream before it can reach the grid or
		// a grab, the same safety net every other broad query on this path
		// already relies on. Skipped when title-only is empty or identical
		// to a query already sent (nothing to gain) or when err != nil
		// (nothing to merge into). Reuses dedupeReleases (searchdedup.go),
		// the same downloadURL/normalizedTitle merge already used to
		// collapse cross-indexer duplicates elsewhere in this file.
		if err == nil && usedStudioTitleQuery {
			titleOnlyQuery := normalizeAdultQuery(strings.TrimSpace(req.Title))
			if titleOnlyQuery != "" && titleOnlyQuery != normalizeAdultQuery(studioTitle) && titleOnlyQuery != query {
				extra, extraErr := sess.Prowlarr.Search(ctx, titleOnlyQuery, []int{adultAutoGrabCategory})
				if extraErr != nil {
					log.Printf("autoGrabSearch: mode=adult title-only variant query %q failed: %v", titleOnlyQuery, extraErr)
				} else {
					log.Printf("autoGrabSearch: mode=adult title-only variant query %q returned %d releases (base query returned %d)", titleOnlyQuery, len(extra), len(releases))
					releases = dedupeReleases(append(releases, extra...), func(rel prowlarr.Release) releaseDedupKey {
						return releaseDedupKey{downloadURL: rel.DownloadURL, normalizedTitle: normalizeAdultQuery(rel.Title), seeders: rel.Seeders}
					})
				}
			}
		}
		return releases, float64(req.DurationSeconds), err
	case mode.Series:
		tvdbID, err := sess.TMDB.ExternalIDs(ctx, req.TMDBID)
		if err != nil {
			return nil, 0, err
		}
		releases, err := sess.Prowlarr.SearchByID(ctx, prowlarr.SearchByIDParams{
			Query:  req.Title,
			TVDBID: tvdbID, Season: req.SeasonNumber, SeasonSpecified: req.SeasonSpecified, Episode: req.EpisodeNumber,
			Categories: categoriesForSearch(mode.Series),
		})
		return releases, seriesEpisodeRuntimeSeconds(ctx, sess, req.TMDBID, req.SeasonNumber, req.EpisodeNumber), err
	default: // Movies
		details, err := sess.TMDB.MovieDetails(ctx, req.TMDBID)
		if err != nil {
			return nil, 0, err
		}
		releases, err := sess.Prowlarr.SearchByID(ctx, prowlarr.SearchByIDParams{
			Query:  req.Title,
			TMDBID: req.TMDBID, IMDBID: details.IMDBID,
			Categories: categoriesForSearch(mode.Movies),
		})
		return releases, float64(details.Runtime) * 60, err
	}
}

// seriesEpisodeRuntimeSeconds resolves the per-episode runtime (seconds) for a
// Series episode, shared by the pre-grab bitrate scorer (autoGrabSearch) and
// the post-grab mislabel check (postGrabRuntimeReview). Only a single-episode
// grab (episodeNumber > 0) gets a real runtime: it fetches the whole season
// once (SeasonDetails) and returns the picked episode's runtime × 60. A
// whole-season grab (episodeNumber == 0) returns 0 (unknown) on purpose — a
// season pack's implied bitrate is ambiguous (its size spans many episodes
// muxed into one release), and the scorer applies one runtime scalar to the
// whole candidate list, so no single value grades a mixed pack-and-single
// result list correctly; unknown → the safe neutral (manual pick list / no
// post-grab flag). Any failure (SeasonDetails error, the episode absent from
// TMDB's list, or a null/zero runtime) also degrades to 0 rather than failing
// the caller: the season lookup only enriches runtime, it is never required.
func seriesEpisodeRuntimeSeconds(ctx context.Context, sess *mode.Session, tmdbID, seasonNumber, episodeNumber int) float64 {
	if episodeNumber <= 0 {
		return 0
	}
	episodes, err := sess.TMDB.SeasonDetails(ctx, tmdbID, seasonNumber)
	if err != nil {
		return 0
	}
	for _, e := range episodes {
		if e.EpisodeNumber == episodeNumber {
			return float64(e.Runtime) * 60
		}
	}
	return 0
}

// These patterns classify a release title as a season pack / multi-episode
// release rather than the single episode a single-episode grab asked for.
// Indexers routinely return season packs among the matches for an
// episode-scoped (ep=) Torznab/Newznab query, so a single-episode runtime
// applied to a pack's whole-season size yields an implausibly high implied
// bitrate that auto-qualifies (the scorer's mislabel check only catches
// bitrates that are too LOW). isSeasonPackTitle neutralizes those packs back
// to unknown — erring toward "treat as a pack" so a single-episode grab never
// silently auto-grabs a whole season.
var (
	// singleEpisodeMarker is a lone SxxEyy / NxNN episode tag (a single episode).
	singleEpisodeMarker = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,4}\b|\b\d{1,2}x\d{1,3}\b`)
	// multiEpisodeMarker is an episode range or list — S01E01E02, S01E01-E05,
	// S01E01-05 — i.e. more than one episode in one release.
	multiEpisodeMarker = regexp.MustCompile(`(?i)S\d{1,2}(E\d{1,4}){2,}|S\d{1,2}E\d{1,4}\s*[-\x{2013}]\s*E?\d{1,4}`)
)

// isSeasonPackTitle reports whether title looks like a season pack / multi-
// episode release rather than a single episode. It errs toward true (safe:
// a false positive only sends a real single episode to the manual pick list;
// a false negative would let a pack auto-grab under a single episode's
// runtime). Only a title carrying a clean single SxxEyy/NxNN marker and no
// multi-episode marker is treated as a single episode; everything else
// (season-only tags, "Complete", or no recognizable marker at all — none of
// which can be confirmed as the requested single episode) is a pack.
func isSeasonPackTitle(title string) bool {
	if multiEpisodeMarker.MatchString(title) {
		return true
	}
	return !singleEpisodeMarker.MatchString(title)
}

// buildAutoGrabCandidates turns Prowlarr releases into autograb.Candidates by
// combining release.Parse's title-derived Resolution/Codec/Source with each
// release's Prowlarr-reported Size/Seeders/Protocol and the shared known
// runtime. Pure and order-preserving: candidates[i] corresponds to
// releases[i], so a Selection's indices map straight back to the originating
// release for grabbing. When neutralizeSeasonPacks is set (a single-episode
// Series grab with a real per-episode runtime), a candidate whose title is a
// season pack keeps RuntimeSeconds 0 — the single-episode runtime is wrong for
// a whole-season file, so it grades as unknown-bitrate (manual review) instead
// of being over-graded into a false auto-grab.
func buildAutoGrabCandidates(releases []prowlarr.Release, runtimeSeconds float64, neutralizeSeasonPacks bool) []autograb.Candidate {
	candidates := make([]autograb.Candidate, len(releases))
	for i, rel := range releases {
		info := release.Parse(rel.Title)
		rt := runtimeSeconds
		if neutralizeSeasonPacks && isSeasonPackTitle(rel.Title) {
			rt = 0
		}
		candidates[i] = autograb.Candidate{
			Title:          rel.Title,
			Protocol:       string(rel.Protocol),
			Seeders:        rel.Seeders,
			SizeBytes:      rel.Size,
			RuntimeSeconds: rt,
			Resolution:     info.Resolution,
			Codec:          info.Codec,
			Source:         info.Source,
		}
	}
	return candidates
}

// rankedAutoGrabCandidates flattens a fallback Selection into the wire pick
// list, ordered by Selection.Ranked (best bitrate score first). Each row pairs
// the grade (status/score/why) with the originating release's grab identity.
func rankedAutoGrabCandidates(sel autograb.Selection, releases []prowlarr.Release) []apidto.AutoGrabCandidate {
	out := make([]apidto.AutoGrabCandidate, 0, len(sel.Ranked))
	for _, idx := range sel.Ranked {
		g := sel.Grades[idx]
		rel := releases[idx]
		out = append(out, apidto.AutoGrabCandidate{
			Title:       rel.Title,
			Indexer:     rel.Indexer,
			Protocol:    string(rel.Protocol),
			DownloadURL: rel.DownloadURL,
			Size:        rel.Size,
			Seeders:     rel.Seeders,
			Status:      string(g.Status),
			Score:       g.Score,
			ImpliedMbps: g.ImpliedMbps,
			FloorMbps:   g.FloorMbps,
			Qualified:   g.Qualified,
		})
	}
	return out
}

// autoGrabTier reads {mode}'s configured quality tier (the SAME per-mode
// setting Search uses — see qualityTierKey), defaulting to quality.Default
// when unset. Adult has no tier key, so it always grades against the default.
func autoGrabTier(ctx context.Context, settingsStore *settings.Store, m mode.Mode) quality.Tier {
	tierStr, err := settingsStore.Get(ctx, qualityTierKey(m))
	if err != nil || tierStr == "" {
		return quality.Default
	}
	return quality.Tier(tierStr)
}

// autoGrabRootFolder resolves {mode}'s configured library root folder — where
// an auto-grabbed download is imported (checkImportHandler relocates into it).
// A missing root folder is a 400, the same guard the old frontend enforced
// client-side before grabbing.
func autoGrabRootFolder(ctx context.Context, settingsStore *settings.Store, m mode.Mode) (string, error) {
	key, ok := libraryRootFolderKey(m)
	if !ok {
		return "", fmt.Errorf("no library root folder applies to %s", m)
	}
	path, err := settingsStore.Get(ctx, key)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("no root folder configured for %s — set one in Settings first", m)
	}
	return path, nil
}

// toDTOGrab maps an internal grabs.Grab onto the exported apidto.Grab wire DTO
// (field-for-field, since apidto.Grab mirrors grabs.Grab's JSON tags exactly)
// so the auto-grab response and the Grabs view share one generated TypeScript
// type.
func toDTOGrab(g grabs.Grab) apidto.Grab {
	return apidto.Grab{
		ID: g.ID, Mode: string(g.Mode), Title: g.Title, TMDBID: g.TMDBID, TVDBID: g.TVDBID,
		SeasonNumber: g.SeasonNumber, EpisodeNumber: g.EpisodeNumber, SeasonSpecified: g.SeasonSpecified,
		QualityProfileID: g.QualityProfileID, Indexer: g.Indexer, Protocol: g.Protocol,
		DownloadClient: g.DownloadClient, ClientRef: g.ClientRef, Status: string(g.Status),
		RootFolderPath: g.RootFolderPath, FlaggedForReview: g.FlaggedForReview, FlagReason: g.FlagReason,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

func writeAutoGrabJSON(w http.ResponseWriter, resp apidto.AutoGrabResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
