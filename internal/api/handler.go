package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/allowlist"
	"github.com/labbersanon/sakms/internal/anthropic"
	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/gemini"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/openai"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/sysinfo"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tpdbrest"
	"github.com/labbersanon/sakms/internal/trakt"
	"github.com/labbersanon/sakms/internal/tvdb"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// NewMux returns an http.ServeMux with SAK's API routes mounted.
// httpClient is shared across every outbound call the API makes (Test,
// Scan, Apply), so its timeout and transport settings apply uniformly.
// connStore persists what's actually configured — Test and Save are
// deliberately separate actions, matching Settings' own "Test connection"
// then "Save" flow. propStore backs every workflow's review queue (Rename,
// Purge, Dedup); allowStore backs Purge's per-mode tag allowlist; prober
// backs Dedup's direct ffprobe reads (a real *mediainfo.Prober in
// production, anything satisfying dedup.Prober in tests); hasher backs Movies
// Dedup's perceptual-hash refinement the same way (a real *phash.Hasher in
// production, a fake satisfying dedup.PHasher in tests, so the end-to-end
// dedup test never shells out to ffmpeg); settingsStore
// backs the setup wizard's dismissed flag; grabsStore backs Search's grab
// tracking (a separate concept from propStore's Scan-stage-Apply queue —
// see internal/grabs' package doc for why); libStore backs Movies' own
// library (root folder contents, tags) now that Radarr no longer does —
// every Rename/Purge/Dedup/Tag handler below dispatches to a Movies-library
// code path or the existing *arr-backed one depending on {mode}. videoHasher
// backs Adult Rename's phash-first identification (a real *videophash.Hasher
// in production, a fake satisfying rename.PHasher in tests) — a SEPARATE,
// StashDB-compatible hasher from `hasher` above (internal/phash's Movies/Series
// Dedup algorithm is a different, incompatible scheme; the two must not be
// blurred). slidersStore backs the admin-defined custom Discover slider
// CRUD + resolve routes (see discover_sliders.go) — a separate concept from
// Discover's fixed trending/popular/upcoming/genre/studio/network
// categories above it. traktStore backs the Trakt watchlist connection
// (settings-save, connection-summary, OAuth device flow, watchlist row —
// see trakt.go, a self-contained file task #9 wrote independently of this
// one; NewMux just registers its handlers). rssFeedsStore backs the
// admin-defined raw RSS feed rows (see rss_feeds.go) — a per-row RSS 2.0
// feed URL fetched and parsed server-side, a separate concept from
// slidersStore (TMDB-backed) and adultNewestRowStore (Prowlarr-scan-cache-
// backed) even though its CRUD+reorder shape mirrors both.
func NewMux(httpClient *http.Client, connStore *connections.Store, propStore *proposals.Store, allowStore *allowlist.Store, prober dedup.Prober, hasher dedup.PHasher, videoHasher rename.PHasher, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store, slidersStore *discoversliders.Store, traktStore *trakt.Store, adultNewestRowStore *adultnewest.Store, adultNewestReleaseStore *adultnewest.ReleaseStore, rssFeedsStore *rssfeeds.Store, entityStore parseentity.EntityStore, whStore *webhooks.Store, dl *downloader.Manager, nzb *usenet.Manager, hub *dedupscan.Hub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/connections/test", connectionsTestHandler(httpClient))
	// test-stored tests an ALREADY-SAVED connection using its stored secret,
	// which the client never holds (see connections.Summary) — so it can't be
	// tested via the stateless /test endpoint without round-tripping the real
	// key to the browser. See connectionsTestStoredHandler's doc for the
	// deliberate no-detail error contract.
	mux.HandleFunc("POST /api/connections/{service}/test-stored", connectionsTestStoredHandler(httpClient, connStore))
	mux.HandleFunc("GET /api/connections", listConnectionsHandler(connStore))
	mux.HandleFunc("PUT /api/connections/{service}", upsertConnectionHandler(connStore))
	mux.HandleFunc("DELETE /api/connections/{service}", deleteConnectionHandler(connStore))

	// LAN service probing for the setup wizard (see netscan.go) — an
	// authenticated-operator convenience that pre-fills a connection's URL.
	// Auth-gated like every other route on this mux; every result is a hint
	// to verify, never a trusted fact. The general probes never return a
	// credential — Prowlarr's key is fetched only via the dedicated, explicit
	// prowlarr-key route.
	mux.HandleFunc("GET /api/netscan/known", netscanKnownHandler(httpClient))
	mux.HandleFunc("POST /api/netscan/host", netscanHostHandler(httpClient))
	mux.HandleFunc("POST /api/netscan/prowlarr-key", netscanProwlarrKeyHandler(httpClient))

	// Live-fetches the model names installed on an operator-supplied Ollama
	// instance (see ai_models.go) — deliberately NOT /api/settings/ai-models,
	// which would collide with the existing singular GET/PUT
	// /api/settings/ai-model pair below.
	mux.HandleFunc("GET /api/ollama/models", ollamaModelsHandler(httpClient))

	mux.HandleFunc("GET /api/modes/{mode}/tracked", listTrackedHandler(httpClient, connStore, settingsStore, libStore))
	mux.HandleFunc("GET /api/modes/{mode}/collections", collectionsHandler(libStore))
	mux.HandleFunc("GET /api/modes/{mode}/library/root-folder", getLibraryRootFolderHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/library/root-folder", putLibraryRootFolderHandler(settingsStore))
	// Validates that a candidate root folder both exists and is writable (SAK
	// writes into it for rename/dedup) — deliberately NOT confined to
	// browse.go's browsableRoots, which scope only the autocomplete helper.
	mux.HandleFunc("POST /api/modes/{mode}/library/root-folder/test", testLibraryRootFolderHandler())

	// Server-side directory browser for the Settings root-folder pickers +
	// their as-you-type autocomplete — restricted to the mounted roots (see
	// browse.go). Session-protected like every other route on this mux.
	mux.HandleFunc("GET /api/browse", browseHandler())
	mux.HandleFunc("GET /api/modes/{mode}/quality-prefs", getQualityPrefsHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/quality-prefs", putQualityPrefsHandler(settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/naming-preset", getNamingPresetHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/naming-preset", putNamingPresetHandler(settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/phash-threshold", getPHashThresholdHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/phash-threshold", putPHashThresholdHandler(settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/match-confidence-threshold", getConfidenceThresholdHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/match-confidence-threshold", putConfidenceThresholdHandler(settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/identify-enabled", getIdentifyEnabledHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/identify-enabled", putIdentifyEnabledHandler(settingsStore))

	mux.HandleFunc("POST /api/modes/{mode}/rename/scan", renameScanHandler(httpClient, connStore, settingsStore, propStore, libStore, prober, videoHasher, entityStore))
	mux.HandleFunc("GET /api/modes/{mode}/rename/proposals", listProposalsHandler(propStore, proposals.Rename))
	mux.HandleFunc("GET /api/modes/{mode}/rename/kids-root-path", getKidsRootPathHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/rename/kids-root-path", putKidsRootPathHandler(settingsStore))

	mux.HandleFunc("POST /api/modes/{mode}/purge/scan", purgeScanHandler(httpClient, connStore, settingsStore, propStore, allowStore, libStore))
	mux.HandleFunc("GET /api/modes/{mode}/purge/proposals", listProposalsHandler(propStore, proposals.Purge))
	mux.HandleFunc("GET /api/modes/{mode}/purge/allowlist", listAllowlistHandler(allowStore))
	mux.HandleFunc("POST /api/modes/{mode}/purge/allowlist", addAllowlistTagHandler(allowStore))
	mux.HandleFunc("DELETE /api/modes/{mode}/purge/allowlist/{tag}", removeAllowlistTagHandler(allowStore))

	mux.HandleFunc("POST /api/modes/{mode}/dedup/scan", dedupScanHandler(httpClient, connStore, settingsStore, propStore, prober, hasher, libStore, hub))
	mux.HandleFunc("GET /api/modes/{mode}/dedup/proposals", listProposalsHandler(propStore, proposals.Dedup))
	// Live per-file progress stream + a status backstop for a running Dedup
	// scan (POST .../dedup/scan now returns 202 and does the work in the
	// background — see dedupScanHandler). Registered on this same authenticated
	// mux, so both are session-gated like the download/notification streams.
	mux.HandleFunc("GET /api/modes/{mode}/dedup/scan/stream", dedupScanStreamHandler(hub))
	mux.HandleFunc("GET /api/modes/{mode}/dedup/scan/status", dedupScanStatusHandler(hub))
	// On-demand VMAF perceptual-quality score for one Dedup candidate measured
	// against the group's chosen reference/primary (star topology, AC1). Async
	// + poll-shaped like the dedup scan above it: a cache miss kicks off a
	// background computation and returns "computing"; the cache (vmaf_scores)
	// serves repeat views without recomputing (AC2). See vmaf.go.
	mux.HandleFunc("GET /api/modes/{mode}/dedup/proposals/{id}/vmaf", vmafHandler(propStore, libStore))
	// Raw video bytes of one Dedup candidate, for the card view's click-to-play
	// preview. Resolves the file server-side by {id}+candidateIndex (bounds-
	// checked) — never a client-supplied path — and streams it via
	// http.ServeContent (range/seek support, no in-memory buffering). The trust
	// boundary is provenance (SAK's own recorded scan path), NOT lexical-root
	// confinement — see dedupVideoHandler's doc comment and the plan's
	// pre-mortem #2 for the full history of why no root check is applied.
	mux.HandleFunc("GET /api/modes/{mode}/dedup/proposals/{id}/video", dedupVideoHandler(propStore))

	// Discover is a read-only proxy against TMDB (trending/popular titles,
	// poster art) — the browse entry point into Search. Search itself is a
	// read-only proxy+score against Prowlarr — nothing staged or persisted
	// (see searchHandler's doc comment). Grab is the one mutating action,
	// tracked in grabsStore rather than propStore (see internal/grabs'
	// package doc for why this isn't a proposals.Kind).
	mux.HandleFunc("GET /api/modes/{mode}/discover", discoverHandler(httpClient, connStore, settingsStore))
	// Discover detail popup: on-demand, per-click availability preview (a
	// single user-triggered Prowlarr search — same trigger shape/cost as the
	// manual Search screen, NOT a reintroduction of the removed automatic
	// per-card probe; see CLAUDE.md's "Discover never queries Prowlarr"
	// note). Graded 32 ways (4 resolutions x 4 tiers x 2 protocols) via
	// internal/autograb — see discover_availability.go.
	mux.HandleFunc("GET /api/modes/{mode}/discover/availability", discoverAvailabilityHandler(httpClient, connStore, settingsStore))
	// Discover detail popup's "Watch Trailer" link — one-shot per popup open,
	// Movies/Series only. See discover_trailer.go.
	mux.HandleFunc("GET /api/modes/{mode}/discover/trailer", discoverTrailerHandler(httpClient, connStore, settingsStore))
	// Discover detail popup's richer per-title enrichment (cast/crew/keywords/
	// watch-providers/recommendations/extended metadata) — one on-demand,
	// per-click TMDB fan-out, soft-failing each section independently.
	// Movies/Series only. See discover_detail.go.
	mux.HandleFunc("GET /api/modes/{mode}/discover/detail", discoverDetailHandler(httpClient, connStore, settingsStore))
	// Calendar / upcoming month view — a TMDB release-date-range browse for the
	// visible month. Movies (release date) / Series (first_air_date premieres)
	// only; never routes through the trending/popular unreleased-hiding filter.
	// See discover_detail.go's discoverCalendarHandler.
	mux.HandleFunc("GET /api/modes/{mode}/discover/calendar", discoverCalendarHandler(httpClient, connStore, settingsStore))
	// Adult Discover is TPDB-backed (browse + search-by-term), not TMDB — the
	// concrete path wins over the {mode} wildcard above for Adult (see
	// adultDiscoverHandler).
	mux.HandleFunc("GET /api/modes/adult/discover", adultDiscoverHandler(httpClient, connStore))
	// Merged, deduped "Recently Released" — always TPDB, plus StashDB's
	// exclusive scenes when StashDB is configured (TPDB-only otherwise, fully
	// backward compatible). See adultDiscoverMergedRecentHandler.
	mux.HandleFunc("GET /api/modes/adult/discover/recent-merged", adultDiscoverMergedRecentHandler(httpClient, connStore))
	// Optional StashDB/FansDB Adult Discover sources — scene (recent/trending),
	// studio, and performer browse rows per box. Unlike TPDB (required, 400 when
	// absent) these are optional: an unconfigured box returns [] (200), never a
	// setup prompt, so the frontend simply hides the row. See
	// adultdiscover_stashbox.go.
	for _, box := range []string{"stashdb", "fansdb"} {
		mux.HandleFunc("GET /api/modes/adult/discover/"+box+"/recent", adultStashBoxRecentHandler(httpClient, connStore, box))
		mux.HandleFunc("GET /api/modes/adult/discover/"+box+"/trending", adultStashBoxTrendingHandler(httpClient, connStore, box))
		mux.HandleFunc("GET /api/modes/adult/discover/"+box+"/studios", adultStashBoxStudiosHandler(httpClient, connStore, box))
		mux.HandleFunc("GET /api/modes/adult/discover/"+box+"/performers", adultStashBoxPerformersHandler(httpClient, connStore, box))
	}
	// discoverHandler's category query param now also accepts upcoming/genre/
	// studio/network (see discover.go) alongside trending/popular — this route
	// is unchanged, just a richer dispatch behind it.
	mux.HandleFunc("GET /api/modes/{mode}/discover/genres", discoverGenresHandler(httpClient, connStore, settingsStore))
	// Studio/network/keyword reference lists are global, not mode-scoped — a
	// TMDB company/network/keyword id means the same thing regardless of
	// which mode's Discover screen or admin slider editor is asking.
	mux.HandleFunc("GET /api/discover/studios", discoverStudiosHandler())
	mux.HandleFunc("GET /api/discover/networks", discoverNetworksHandler())
	mux.HandleFunc("GET /api/discover/keywords", discoverKeywordsHandler(httpClient, connStore, settingsStore))
	// Admin-defined custom Discover sliders (Seerr's CreateSlider/
	// DiscoverSliderEdit equivalent) — CRUD + reorder on the stored config,
	// plus resolve to fetch a slider's actual TMDB items (see
	// discover_sliders.go).
	mux.HandleFunc("GET /api/discover/sliders", listSlidersHandler(slidersStore))
	mux.HandleFunc("POST /api/discover/sliders", createSliderHandler(slidersStore))
	mux.HandleFunc("PUT /api/discover/sliders/{id}", updateSliderHandler(slidersStore))
	mux.HandleFunc("DELETE /api/discover/sliders/{id}", deleteSliderHandler(slidersStore))
	mux.HandleFunc("POST /api/discover/sliders/reorder", reorderSlidersHandler(slidersStore))
	mux.HandleFunc("GET /api/discover/sliders/{id}/resolve", resolveSliderHandler(httpClient, connStore, settingsStore, slidersStore))
	// Admin-defined raw RSS 2.0 feed rows (NZBGeek saved-search style) — CRUD +
	// reorder on the stored config, plus resolve to fetch+parse the feed's
	// live items (see rss_feeds.go). A separate concept from the TMDB-backed
	// sliders above: a feed's items are already fully-resolved releases
	// (a real downloadUrl+protocol in hand), not catalog titles to search for.
	mux.HandleFunc("GET /api/discover/rss-feeds", listRssFeedsHandler(rssFeedsStore))
	mux.HandleFunc("POST /api/discover/rss-feeds", createRssFeedHandler(rssFeedsStore))
	mux.HandleFunc("PUT /api/discover/rss-feeds/{id}", updateRssFeedHandler(rssFeedsStore))
	mux.HandleFunc("DELETE /api/discover/rss-feeds/{id}", deleteRssFeedHandler(rssFeedsStore))
	mux.HandleFunc("POST /api/discover/rss-feeds/reorder", reorderRssFeedsHandler(rssFeedsStore))
	mux.HandleFunc("GET /api/discover/rss-feeds/{id}/resolve", resolveRssFeedHandler(httpClient, rssFeedsStore))
	// Discover row display order — a best-effort hint over the FULL merged
	// row set (built-in rows + every dynamic row type), one per screen (see
	// discover_row_order.go). NOT the same invariant as the reorder routes
	// above (which require the full existing id set exactly once) — this is
	// deliberately loose (see discoverRowOrderSettingKey's doc comment).
	mux.HandleFunc("GET /api/discover/row-order/{screen}", getRowOrderHandler(settingsStore))
	mux.HandleFunc("PUT /api/discover/row-order/{screen}", putRowOrderHandler(settingsStore))
	// Trakt: watchlist connection (Settings) + Discover watchlist row. See
	// trakt.go for the handlers and the full route-table rationale — this is
	// just the one-line mux registration. traktFlow is one shared in-memory
	// device-flow state for the whole mux (single-operator app, at most one
	// device authorization in progress at a time — see traktDeviceFlow's doc
	// comment).
	traktFlow := newTraktDeviceFlow()
	mux.HandleFunc("GET /api/trakt/status", traktStatusHandler(traktStore))
	mux.HandleFunc("PUT /api/trakt/credentials", traktSaveCredentialsHandler(traktStore))
	mux.HandleFunc("POST /api/trakt/device/start", traktDeviceStartHandler(traktStore, traktFlow, httpClient, trakt.DefaultBaseURL))
	mux.HandleFunc("POST /api/trakt/device/poll", traktDevicePollHandler(traktStore, traktFlow, httpClient, trakt.DefaultBaseURL))
	mux.HandleFunc("POST /api/trakt/disconnect", traktDisconnectHandler(traktStore))
	mux.HandleFunc("GET /api/trakt/watchlist", traktWatchlistHandler(traktStore, httpClient, trakt.DefaultBaseURL))
	// Adult Discover's row-based surface (parallel to Mainstream's rows): a
	// Studios row and a Performers row (plain TPDB browse), each with a
	// drill-down showing just that studio's/performer's scenes. All TPDB-backed
	// and registered on concrete "adult" paths, same as the discover route above.
	mux.HandleFunc("GET /api/modes/adult/studios", adultStudiosHandler(httpClient, connStore))
	mux.HandleFunc("GET /api/modes/adult/studios/{id}/scenes", adultStudioScenesHandler(httpClient, connStore))
	mux.HandleFunc("GET /api/modes/adult/performers", adultPerformersHandler(httpClient, connStore))
	mux.HandleFunc("GET /api/modes/adult/performers/{id}/scenes", adultPerformerScenesHandler(httpClient, connStore))
	// Adult "newest" rows (internal/adultnewest) — admin-defined rows backed
	// by a Prowlarr "newest releases" background scan matched to TPDB/
	// StashDB/FansDB entities, cached and read-only at request time (see
	// adult_newest_rows.go's package doc — this is NOT a live Prowlarr call
	// per resolve, unlike the TMDB-backed sliders above).
	mux.HandleFunc("GET /api/modes/adult/newest-rows", listAdultNewestRowsHandler(adultNewestRowStore))
	mux.HandleFunc("POST /api/modes/adult/newest-rows", createAdultNewestRowHandler(adultNewestRowStore))
	mux.HandleFunc("PUT /api/modes/adult/newest-rows/{id}", updateAdultNewestRowHandler(adultNewestRowStore))
	mux.HandleFunc("DELETE /api/modes/adult/newest-rows/{id}", deleteAdultNewestRowHandler(adultNewestRowStore))
	mux.HandleFunc("POST /api/modes/adult/newest-rows/reorder", reorderAdultNewestRowsHandler(adultNewestRowStore))
	mux.HandleFunc("GET /api/modes/adult/newest-rows/{id}/resolve", resolveAdultNewestRowHandler(adultNewestRowStore, adultNewestReleaseStore))
	mux.HandleFunc("GET /api/modes/adult/newest-rows/genres", adultNewestGenresHandler(adultNewestReleaseStore))
	// Image proxy: server-side-fetch + cache poster/thumbnail art from the
	// allowlisted TMDB/TPDB image hosts so the browser never hot-links them
	// (see images.go / internal/imageproxy). Read-only, auth-gated like every
	// route here.
	mux.HandleFunc("GET /api/images/proxy", imageProxyHandler(httpClient))
	mux.HandleFunc("GET /api/modes/{mode}/discover/tvdb-id", resolveTVDBIDHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/tmdb-search", tmdbSearchHandler(httpClient, connStore, settingsStore))
	// poster resolves a library card's TMDB poster art lazily, per card (the
	// library caches no poster path) — see posterHandler.
	mux.HandleFunc("GET /api/modes/{mode}/poster", posterHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/search", searchHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("POST /api/modes/{mode}/search/grab", grabHandler(httpClient, connStore, settingsStore, dl, nzb, grabsStore, whStore))
	// Auto-grab is Discover's one-click unattended grab (Stage 2): search +
	// bitrate-quality-floor scoring, then either grab the top qualifier or
	// return the ranked manual pick list. Exactly one release per call.
	mux.HandleFunc("POST /api/modes/{mode}/autograb", autoGrabHandler(httpClient, connStore, settingsStore, dl, nzb, grabsStore))
	mux.HandleFunc("GET /api/modes/{mode}/grabs", listGrabsHandler(grabsStore))
	mux.HandleFunc("POST /api/grabs/{id}/check-import", checkImportHandler(httpClient, connStore, settingsStore, dl, nzb, grabsStore, libStore, prober))
	// Request-status worklist: a cross-mode (NOT mode-scoped) rollup aggregated
	// live from the tracked library + in-flight grabs, plus Series missing-
	// episode counts. Pure read aggregation, no new table. Distinct from the
	// per-mode /grabs log and the /downloads client status — see requests.go.
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore))

	// Download queue: torrent (anacrolix) + usenet (NNTP) merged into one
	// stream. GID routing: "nzb-" prefix → usenet engine, otherwise torrent.
	// See downloads.go.
	mux.HandleFunc("GET /api/downloads", listDownloadsHandler(dl, nzb))
	mux.HandleFunc("GET /api/downloads/stream", downloadsStreamHandler(dl, nzb))
	// Live browser-notification stream. Registered on this same authenticated
	// mux (never the public setup/login mux). This relies on whStore being the
	// single shared singleton passed to every handler (see handler.go:62,247,
	// 334,335) — the same instance Dispatch publishes to. A future refactor that
	// constructs a per-request/per-handler Store would silently disconnect
	// subscribers from publishers without breaking anything else.
	mux.HandleFunc("GET /api/notifications/stream", notificationsStreamHandler(whStore))
	mux.HandleFunc("DELETE /api/downloads/{gid}", cancelDownloadHandler(dl, nzb))
	mux.HandleFunc("POST /api/downloads/{gid}/pause", pauseDownloadHandler(dl, nzb))
	mux.HandleFunc("POST /api/downloads/{gid}/resume", resumeDownloadHandler(dl, nzb))

	// Unified downloader config (staging dir + concurrency knobs). The RPC
	// token is auto-generated and stored via internal/secrets, never here.
	mux.HandleFunc("GET /api/downloader/config", getDownloaderConfigHandler(settingsStore))
	mux.HandleFunc("PUT /api/downloader/config", putDownloaderConfigHandler(settingsStore))

	mux.HandleFunc("GET /api/modes/{mode}/tags", listTagsHandler(libStore))
	mux.HandleFunc("POST /api/modes/{mode}/items/{itemId}/tags", addItemTagHandler(libStore))
	mux.HandleFunc("DELETE /api/modes/{mode}/items/{itemId}/tags/{tagId}", removeItemTagHandler(libStore))

	// Adult scene tags — a parallel, fully library-backed path (see tag.go).
	// Adult-only and hardcoded in the path (scenes exist only for Adult). The
	// generic {mode}/items and {mode}/tags routes above 400 for Adult (see
	// tag.go); Adult tags live entirely on these scene routes.
	mux.HandleFunc("GET /api/modes/adult/scenes/tags", sceneTagVocabularyHandler(libStore))
	mux.HandleFunc("GET /api/modes/adult/scenes/{sceneId}/tags", listSceneTagsHandler(libStore))
	mux.HandleFunc("POST /api/modes/adult/scenes/{sceneId}/tags", addSceneTagHandler(libStore))
	mux.HandleFunc("DELETE /api/modes/adult/scenes/{sceneId}/tags/{tagId}", removeSceneTagHandler(libStore))

	mux.HandleFunc("GET /api/setup/status", setupStatusHandler(connStore, allowStore, settingsStore))
	mux.HandleFunc("PUT /api/setup/dismissed", dismissSetupHandler(settingsStore))

	// One shared AI provider+model pair for every AI-assisted feature (Adult
	// identification AND Movies/Series Rename's AI fallback) — see
	// mode.AIModelKey's doc comment for why this isn't split per mode.
	mux.HandleFunc("GET /api/settings/ai-provider", getAIProviderHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/ai-provider", putAIProviderHandler(settingsStore))
	mux.HandleFunc("GET /api/settings/ai-model", getOllamaModelHandler(settingsStore, mode.AIModelKey))
	mux.HandleFunc("PUT /api/settings/ai-model", putOllamaModelHandler(settingsStore, mode.AIModelKey))
	// AI fallback opt-in toggle — off by default; DB-first parsing runs alone
	// until the operator explicitly enables it here.
	mux.HandleFunc("GET /api/settings/ai-fallback-enabled", getAIFallbackEnabledHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/ai-fallback-enabled", putAIFallbackEnabledHandler(settingsStore))
	// Browser (desktop) notifications opt-in toggle — off by default; the
	// browser's own Notification permission is tracked separately client-side.
	mux.HandleFunc("GET /api/settings/browser-notifications-enabled", getBrowserNotificationsEnabledHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/browser-notifications-enabled", putBrowserNotificationsEnabledHandler(settingsStore))
	// Adult mode visibility switch — a pure frontend UI-hiding toggle, never
	// a backend access boundary; see adult_mode.go's package doc.
	mux.HandleFunc("GET /api/settings/adult-mode-enabled", getAdultModeEnabledHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/adult-mode-enabled", putAdultModeEnabledHandler(settingsStore))
	// Entity cache admin — counts, per-source sync state, on-demand sync triggers
	mux.HandleFunc("GET /api/admin/entity-sync", entitySyncStatusHandler(entityStore))
	mux.HandleFunc("POST /api/admin/entity-sync/{source}", triggerEntitySyncHandler(entityStore, connStore, settingsStore, httpClient))
	// Live container/host resource metrics streamed as server-sent events for
	// the System Dashboard (internal/sysinfo reads cgroups v2 + /proc).
	mux.HandleFunc("GET /api/admin/sysinfo/stream", sysinfoStreamHandler(
		sysinfo.Sample,
		func(ctx context.Context) []sysinfo.MountSpec {
			return buildMountsFromSettings(ctx, settingsStore)
		},
	))
	// Shared background sync cadence for all four entity sources combined (see
	// internal/parseentity's Run/LoadInterval) — 0/off by default, additive to
	// the manual per-source triggers directly above.
	mux.HandleFunc("GET /api/settings/entity-sync-interval", getEntitySyncIntervalHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/entity-sync-interval", putEntitySyncIntervalHandler(settingsStore))

	// Interval for the opt-in background availability recheck job (see
	// internal/recheck) — 0/off by default. Just a settings scalar here; the
	// job itself lives in its own package, started once from main.
	mux.HandleFunc("GET /api/settings/recheck-interval", getRecheckIntervalHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/recheck-interval", putRecheckIntervalHandler(settingsStore))

	// Watch-folders toggle — opt-in, off by default. The background goroutine
	// (RunWatchFolders, started from main) polls this setting every
	// defaultWatchPollInterval seconds (or the configured
	// watch-folders-poll-interval, if set) and starts/stops watching
	// accordingly.
	mux.HandleFunc("GET /api/admin/watch-folders", getWatchFoldersHandler(settingsStore))
	mux.HandleFunc("PUT /api/admin/watch-folders/enabled", putWatchFoldersEnabledHandler(settingsStore))
	mux.HandleFunc("GET /api/settings/watch-folders-poll-interval", getWatchFoldersPollIntervalHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/watch-folders-poll-interval", putWatchFoldersPollIntervalHandler(settingsStore))
	mux.HandleFunc("GET /api/settings/adult-newest-scan-interval", getAdultNewestScanIntervalHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/adult-newest-scan-interval", putAdultNewestScanIntervalHandler(settingsStore))

	// General Rename/Purge/Dedup scan scheduler settings (see
	// internal/scanschedule) — one interval per workflow plus the Dedup
	// eager-VMAF toggle, all 0/off by default. Settings scalars only; the
	// scheduler goroutines live in their own package, started once from main.
	mux.HandleFunc("GET /api/settings/rename-scan-interval", getScanIntervalHandler(settingsStore, renameScanIntervalKey))
	mux.HandleFunc("PUT /api/settings/rename-scan-interval", putScanIntervalHandler(settingsStore, renameScanIntervalKey))
	mux.HandleFunc("GET /api/settings/purge-scan-interval", getScanIntervalHandler(settingsStore, purgeScanIntervalKey))
	mux.HandleFunc("PUT /api/settings/purge-scan-interval", putScanIntervalHandler(settingsStore, purgeScanIntervalKey))
	mux.HandleFunc("GET /api/settings/dedup-scan-interval", getScanIntervalHandler(settingsStore, dedupScanIntervalKey))
	mux.HandleFunc("PUT /api/settings/dedup-scan-interval", putScanIntervalHandler(settingsStore, dedupScanIntervalKey))
	mux.HandleFunc("GET /api/settings/dedup-vmaf-scan-enabled", getDedupVMAFScanEnabledHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/dedup-vmaf-scan-enabled", putDedupVMAFScanEnabledHandler(settingsStore))

	// Webhook subscriptions — CRUD plus a fire-once test delivery.
	mux.HandleFunc("GET /api/webhooks", listWebhooksHandler(whStore))
	mux.HandleFunc("POST /api/webhooks", createWebhookHandler(whStore))
	mux.HandleFunc("PUT /api/webhooks/{id}", updateWebhookHandler(whStore))
	mux.HandleFunc("DELETE /api/webhooks/{id}", deleteWebhookHandler(whStore))
	mux.HandleFunc("POST /api/webhooks/{id}/test", testWebhookHandler(whStore))

	mux.HandleFunc("POST /api/proposals/{id}/apply", applyProposalHandler(httpClient, connStore, settingsStore, propStore, libStore, whStore))
	mux.HandleFunc("POST /api/proposals/apply-batch", applyBatchHandler(httpClient, connStore, settingsStore, propStore, libStore, whStore))
	mux.HandleFunc("POST /api/proposals/{id}/submit-draft", submitDraftHandler(httpClient, connStore, settingsStore, propStore))
	mux.HandleFunc("POST /api/proposals/{id}/dismiss", dismissProposalHandler(propStore))
	mux.HandleFunc("POST /api/proposals/{id}/repick", repickProposalHandler(propStore))

	return mux
}

func connectionsTestHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ConnectionTestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		result := TestConnection(r.Context(), httpClient, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// connectionsTestStoredHandler tests an already-configured connection using
// the secret held in the store — the client never has the real key (see
// connections.Summary), so it can't drive the stateless /test endpoint for a
// saved connection without the key round-tripping to the browser, which must
// never happen.
//
// Security contract: on failure the raw downstream error is NEVER propagated.
// A Go http-client error echoes the target URL (e.g. `dial tcp ... connection
// refused` includes host:port), and some clients put the key in a query param
// — either would leak stored config the client isn't allowed to see. So any
// non-OK result (and any internal error) is reported with a fixed, detail-free
// message. The response is exactly ConnectionTestResult, same as /test.
func connectionsTestStoredHandler(httpClient *http.Client, store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		conn, err := store.Get(r.Context(), service)
		if err != nil {
			if errors.Is(err, connections.ErrNotFound) {
				http.Error(w, "no connection configured for that service", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load stored connection", http.StatusInternalServerError)
			return
		}

		result := TestConnection(r.Context(), httpClient, ConnectionTestRequest{
			Service:  service,
			URL:      conn.URL,
			Username: conn.Username,
			APIKey:   conn.APIKey,
		})
		if !result.OK {
			result.Error = "connection test failed"
		}
		writeJSON(w, result)
	}
}

func listConnectionsHandler(store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range list {
			list[i].FixedURL = fixedURLValues[list[i].Service]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

type upsertConnectionRequest struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"` // only qbittorrent/nzbget use this
	// APIKey is a pointer so the handler can distinguish three states the UI
	// needs to express (json.Decode sets it accordingly; omitempty only affects
	// marshaling, not decoding): nil = field absent from the JSON entirely →
	// preserve the stored secret (the client is never sent the real key back —
	// see connections.Store.List/Get — so a blank untouched key field must not
	// wipe it); non-nil pointing at "" → explicitly clear it (e.g. Ollama needs
	// none); non-nil non-empty → set/replace it.
	APIKey *string `json:"apiKey,omitempty"`
}

// fixedURLServices are the connections whose outbound base URL is a hardcoded
// package constant (internal/tmdb, internal/tvdb, internal/stashbox, internal/tpdbrest,
// internal/openai, internal/gemini, internal/anthropic, internal/bravesearch), not a
// user-supplied field — so the UI collects no URL for them and `url` is
// optional in their upsert requests. Every other service still requires a URL.
// tmdb/tvdb/stashdb/fansdb/tpdb never collected a URL in the first place;
// openai/gemini/anthropic/brave are different — they used to accept a
// user-supplied URL, and this map now means any previously-stored value for
// them simply stops being read (see buildAIClient/buildIdentifier in
// internal/mode/mode.go), not that they never had one.
// Mirrors SERVICES_WITH_FIXED_URL in the frontend (frontend/src/api/settings.ts).
var fixedURLServices = map[string]bool{
	"tmdb": true, "tvdb": true, "stashdb": true, "fansdb": true, "tpdb": true,
	"openai": true, "gemini": true, "anthropic": true, "brave": true,
}

// fixedURLValues maps each fixedURLServices entry to the real base-URL constant
// from its client package, so listConnectionsHandler can report the actual
// in-use URL over the API instead of the frontend hardcoding (and drifting
// from) these Go values. Keyed identically to fixedURLServices above.
var fixedURLValues = map[string]string{
	"tmdb":      tmdb.DefaultBaseURL,
	"tvdb":      tvdb.DefaultBaseURL,
	"stashdb":   stashbox.StashDBURL,
	"fansdb":    stashbox.FansDBURL,
	"tpdb":      tpdbrest.DefaultBaseURL,
	"openai":    openai.DefaultBaseURL,
	"gemini":    gemini.DefaultBaseURL,
	"anthropic": anthropic.DefaultBaseURL,
	"brave":     bravesearch.DefaultBaseURL,
}

func upsertConnectionHandler(store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		var req upsertConnectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !fixedURLServices[service] && req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if err := store.UpsertPreservingSecret(r.Context(), service, req.URL, req.Username, req.APIKey); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteConnectionHandler(store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		if err := store.Delete(r.Context(), service); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
