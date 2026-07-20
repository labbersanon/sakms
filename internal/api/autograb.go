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
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
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
func autoGrabHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager, grabsStore *grabs.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

		releases, runtimeSeconds, err := autoGrabSearch(ctx, sess, m, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// A real per-episode runtime (Series single-episode grab) mustn't be
		// applied to season packs the indexer returned for the episode query —
		// neutralize those so they can't over-qualify (see buildAutoGrabCandidates).
		neutralizeSeasonPacks := m == mode.Series && runtimeSeconds > 0
		candidates := buildAutoGrabCandidates(releases, runtimeSeconds, neutralizeSeasonPacks)
		sel := autograb.Select(candidates, autoGrabTier(ctx, settingsStore, m), minSeedersFor(m))

		// Fallback: nothing cleared the floor → hand back the ranked pick list
		// (best bitrate score first, the same score that rejected them all).
		if sel.Fallback {
			writeAutoGrabJSON(w, apidto.AutoGrabResponse{
				Fallback:   true,
				Message:    "nothing cleared the quality floor automatically — pick one below",
				Candidates: rankedAutoGrabCandidates(sel, releases),
			})
			return
		}

		// Qualified: send exactly the one top-scored release to the download
		// client and record it. Root folder is resolved server-side — a true
		// one-click grab supplies only the title.
		rootFolder, err := autoGrabRootFolder(ctx, settingsStore, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		picked := releases[sel.PickIndex]

		downloadClient, gid, status, err := dispatchToDownloadClient(ctx, sess, m, nzb, string(picked.Protocol), picked.DownloadURL, picked.Title)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		created, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: m, Title: req.Title, TMDBID: req.TMDBID,
			SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber, SeasonSpecified: req.SeasonSpecified,
			Indexer: picked.Indexer, Protocol: string(picked.Protocol),
			DownloadClient: downloadClient, RootFolderPath: rootFolder,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if gid != "" {
			if err := grabsStore.SetDownloadGID(ctx, created.ID, gid); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			created.DownloadGID = gid
		}

		dto := toDTOGrab(created)
		writeAutoGrabJSON(w, apidto.AutoGrabResponse{
			Grabbed: true,
			Message: "auto-grabbed " + picked.Title,
			Grab:    &dto,
		})
	}
}

// autoGrabSearch runs the per-mode Prowlarr search and resolves the known
// pre-grab runtime (seconds) the bitrate scorer needs. Movies/Series probe
// id-scoped (mirroring availability.CheckMovie/CheckSeries); Adult uses a
// free-text query over the XXX category — the raw first-matched release
// title when available (req.ReleaseTitle), else a studio+title
// reconstruction (mirroring availability.CheckAdultScene). Runtime: Movies
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
		rawQuery := strings.TrimSpace(req.ReleaseTitle)
		if rawQuery == "" {
			rawQuery = strings.TrimSpace(strings.TrimSpace(req.Studio) + " " + strings.TrimSpace(req.Title))
		}
		query := normalizeAdultQuery(rawQuery)
		log.Printf("autoGrabSearch: mode=adult rawQuery=%q query=%q category=%d studio=%q title=%q releaseTitle=%q", rawQuery, query, adultAutoGrabCategory, req.Studio, req.Title, req.ReleaseTitle)
		releases, err := sess.Prowlarr.Search(ctx, query, []int{adultAutoGrabCategory})
		return releases, float64(req.DurationSeconds), err
	case mode.Series:
		tvdbID, err := sess.TMDB.ExternalIDs(ctx, req.TMDBID)
		if err != nil {
			return nil, 0, err
		}
		releases, err := sess.Prowlarr.SearchByID(ctx, prowlarr.SearchByIDParams{
			Query:  req.Title,
			TVDBID: tvdbID, Season: req.SeasonNumber, Episode: req.EpisodeNumber,
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
