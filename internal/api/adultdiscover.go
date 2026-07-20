package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// adultScene is the Discover response shape for one TPDB scene — a stable,
// lowercase-json DTO mirroring search.go's searchResult, so the frontend reads
// item.id/item.title/item.studio/item.date the same way it reads TMDB's
// lowercase-tagged tmdb.Item. tpdbrest.Scene itself carries NO json tags, so
// encoding it raw would emit capitalized keys (ID/Title/Site/Date) the frontend
// doesn't read — hence this explicit mapping (note Site → studio).
//
// Image is the scene thumbnail URL (TPDB's flat "image" field, served from
// cdn.theporndb.net — already covered by internal/imageproxy's allowlist). It
// is often empty (many scenes carry no art), so the frontend must render a
// text-only card when it's blank and route non-empty values through the image
// proxy, never hot-link TPDB directly (plan Decision #7).
//
// DurationSeconds is the scene's pre-grab runtime in seconds (see
// tpdbrest.Scene.Duration for sourcing/confidence — documented-shape,
// corroborated by two independent sources, not live-confirmed). May be 0
// (unknown) — the auto-grab bitrate scorer (Stage 2) must treat 0 as
// "unknown, skip the pre-grab bitrate check," never as a real zero-length
// runtime or a divide-by-zero input, per the plan's missing-input handling.
//
// Rating is the scene's own numeric rating (TPDB's "rating" field). It backs
// the "Highest Rated" Discover row (category=top-rated), which the handler
// produces by re-sorting one browse page by this value descending — a
// page-local ordering, honestly NOT a true global popularity ranking (TPDB has
// no server-side popularity sort; see tpdbrest.BrowseScenes' doc). May be 0.
//
// Source names the upstream catalog this scene came from ("tpdb", "stashdb",
// or "fansdb") so the card can show a provenance label — see
// adultdiscover_stashbox.go for the optional stash-box sources and the merged
// "Recently Released" feed. TPDB's own handlers here always set "tpdb".
//
// Slug is TPDB's URL-friendly scene identifier (see tpdbrest.Scene.Slug for
// sourcing), used by the Discover detail popup's "More on TPDB" external
// link — theporndb.net/scenes/{slug}, NOT {id}. Always empty for a
// stash-box ("stashdb"/"fansdb") scene: those sites' own detail pages are
// UUID-path (stashdb.org/scenes/{id}), so the popup links via ID for them
// instead and never reads Slug in that branch.
// Genres/Performers back the Discover detail popup's tags/performers list —
// populated for TPDB-sourced scenes (tpdbrest.Scene.Tags/Performers, both
// confirmed present on SceneResource in TPDB's live OpenAPI schema); left
// unset ("omitempty") for stash-box ("stashdb"/"fansdb") scenes constructed
// elsewhere (adultdiscover_stashbox.go) — that schema's shape isn't verified
// against a live instance yet.
type adultScene struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Studio          string   `json:"studio"`
	Date            string   `json:"date"`
	Image           string   `json:"image"`
	DurationSeconds int      `json:"durationSeconds"`
	Rating          float64  `json:"rating"`
	Source          string   `json:"source"`
	Slug            string   `json:"slug"`
	Genres          []string `json:"genres,omitempty"`
	Performers      []string `json:"performers,omitempty"`
}

// adultStudio mirrors apidto.StudioSummary — one TPDB site (studio) reduced to
// a browse card's fields (id/name/image). See tpdbrest.Site for how Image is
// chosen from TPDB's several nullable site image fields.
type adultStudio struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Source string `json:"source"`
}

// adultPerformer mirrors apidto.PerformerSummary — one TPDB performer reduced
// to a browse card's fields (id/name/image). See tpdbrest.Performer for Image.
type adultPerformer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Source string `json:"source"`
}

// adultTPDBClient builds a standalone tpdbrest client from the stored "tpdb"
// connection, writing the appropriate HTTP error and returning ok=false when
// it isn't configured (or can't be loaded). Every Adult Discover handler needs
// exactly this and nothing a mode.Session carries, so it's factored out here
// rather than repeated per handler (same construction mode.go's buildIdentifier
// uses — mode.Session doesn't expose the raw REST client, it's wrapped inside
// sess.Identify).
func adultTPDBClient(w http.ResponseWriter, r *http.Request, httpClient *http.Client, connStore *connections.Store) (*tpdbrest.Client, bool) {
	conn, err := connStore.Get(r.Context(), "tpdb")
	if err != nil {
		if errors.Is(err, connections.ErrNotFound) {
			http.Error(w, "tpdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return nil, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	// TPDB's REST base is a fixed public endpoint — hardcoded, never conn.URL.
	return tpdbrest.New(tpdbrest.DefaultBaseURL, conn.APIKey, httpClient), true
}

// adultPagination reads the page/perPage query params the four browse/drill-down
// routes share. Both are best-effort: an absent/blank/invalid value passes
// through as 0, which every tpdbrest browse/drill-down method clamps to its own
// sane default (page → 1, perPage → defaultBrowsePerPage), so a bad client value
// never produces a malformed query — the same lenient contract discoverHandler
// documents for its own page param.
func adultPagination(r *http.Request) (page, perPage int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ = strconv.Atoi(r.URL.Query().Get("perPage"))
	return page, perPage
}

// tpdbSceneToAdultScene converts one tpdbrest.Scene into the adultScene wire
// shape — shared by writeAdultScenes (every plain TPDB response) and
// adultDiscoverMergedRecentHandler (adultdiscover_stashbox.go), which needs
// the same conversion applied per-item while it accumulates a merged slice
// rather than encoding scenes straight off a single tpdbrest call.
func tpdbSceneToAdultScene(s tpdbrest.Scene) adultScene {
	tagNames := make([]string, len(s.Tags))
	for i, t := range s.Tags {
		tagNames[i] = t.Name
	}
	return adultScene{ID: s.ID, Title: s.Title, Studio: s.Site, Date: s.Date, Image: s.Image, DurationSeconds: s.Duration, Rating: s.Rating, Source: "tpdb", Slug: s.Slug, Genres: tagNames, Performers: s.Performers}
}

// writeAdultScenes converts a tpdbrest scene slice into the adultScene wire
// shape and encodes it — the one internal→JSON scene conversion every Adult
// Discover scene response (browse, category rows, and both drill-downs) shares.
func writeAdultScenes(w http.ResponseWriter, scenes []tpdbrest.Scene) {
	out := make([]adultScene, len(scenes))
	for i, s := range scenes {
		out[i] = tpdbSceneToAdultScene(s)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// adultDiscoverHandler backs Adult's Discover screen against ThePornDB's REST
// catalog. When q is set it's a title text-search (SearchByTitle). Otherwise
// it's a plain paginated browse, whose ordering is chosen by the category
// param (mirroring discoverHandler's category convention for Movies/Series):
//
//   - category=recent    → BrowseScenes ordered "recently_released" (TPDB's
//     server-side recency sort — the one real ordered feed the spec offers).
//   - category=top-rated → a plain BrowseScenes (no server order) whose one
//     returned page is then re-sorted by each scene's own Rating, descending,
//     server-side in Go. This is honestly a same-page re-sort, NOT a true
//     global popularity ranking — TPDB's SearchOrderEnum has no popularity/
//     rating sort, so there is no server-side "top rated" to ask for (see
//     tpdbrest.BrowseScenes' doc).
//   - absent/unrecognized → the historical default: a plain unordered browse
//     (BrowseScenes with orderBy ""), preserving pre-category behavior exactly.
//
// Adult-only: its identity space is a stash-box/TPDB scene, not a TMDB id, so it
// can't share discoverHandler's TMDB path — this route is registered on the
// concrete /api/modes/adult/discover pattern (a literal "adult", not a {mode}
// wildcard), which ServeMux prefers over the wildcard {mode}/discover one.
func adultDiscoverHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}

		var scenes []tpdbrest.Scene
		var err error
		if q := r.URL.Query().Get("q"); q != "" {
			// Search-by-term entry point — no studio narrowing here (the browse
			// screen searches by free title text; identify.SearchTPDB is what
			// narrows by studio during identification).
			scenes, err = client.SearchByTitle(ctx, q, "")
		} else {
			page, perPage := adultPagination(r)
			switch r.URL.Query().Get("category") {
			case "recent":
				scenes, err = client.BrowseScenes(ctx, page, perPage, "recently_released")
			case "top-rated":
				scenes, err = client.BrowseScenes(ctx, page, perPage, "")
				// Same-page re-sort by the scene's own rating, descending — NOT
				// a global popularity ranking (TPDB has no such server sort).
				// Stable so equal-rated scenes keep TPDB's returned order.
				sort.SliceStable(scenes, func(i, j int) bool {
					return scenes[i].Rating > scenes[j].Rating
				})
			default:
				// No/unknown category → the historical plain unordered browse.
				scenes, err = client.BrowseScenes(ctx, page, perPage, "")
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeAdultScenes(w, scenes)
	}
}

// adultStudiosHandler backs Adult Discover's Studios row — a plain paginated
// browse of TPDB's site (studio) catalog (BrowseSites), each reduced to
// {id, name, image}. The id doubles as the {id} path segment of the studio
// drill-down route below.
func adultStudiosHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		page, perPage := adultPagination(r)
		sites, err := client.BrowseSites(r.Context(), page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]adultStudio, len(sites))
		for i, s := range sites {
			out[i] = adultStudio{ID: s.ID, Name: s.Name, Image: s.Image, Source: "tpdb"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// adultPerformersHandler backs Adult Discover's Performers row — a plain
// paginated browse of TPDB's performer catalog (BrowsePerformers), each reduced
// to {id, name, image}. The id doubles as the {id} path segment of the
// performer drill-down route below.
func adultPerformersHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		page, perPage := adultPagination(r)
		performers, err := client.BrowsePerformers(r.Context(), page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]adultPerformer, len(performers))
		for i, p := range performers {
			out[i] = adultPerformer{ID: p.ID, Name: p.Name, Image: p.Image, Source: "tpdb"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// adultStudioScenesHandler is the studio drill-down — clicking a Studios-row
// card shows just that studio's scenes (ScenesBySite via TPDB's dedicated
// /sites/{id}/scenes endpoint). {id} is the opaque TPDB site id from the
// Studios row. Returns the same adultScene array shape as adultDiscoverHandler.
func adultStudioScenesHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		page, perPage := adultPagination(r)
		scenes, err := client.ScenesBySite(r.Context(), r.PathValue("id"), page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeAdultScenes(w, scenes)
	}
}

// adultPerformerScenesHandler is the performer drill-down — clicking a
// Performers-row card shows just that performer's scenes (ScenesByPerformer via
// TPDB's dedicated /performers/{id}/scenes endpoint). {id} is the opaque TPDB
// performer id. Returns the same adultScene array shape as adultDiscoverHandler.
func adultPerformerScenesHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		page, perPage := adultPagination(r)
		scenes, err := client.ScenesByPerformer(r.Context(), r.PathValue("id"), page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeAdultScenes(w, scenes)
	}
}
