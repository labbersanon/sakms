// Package apidto is sakms's curated, exported request/response DTO
// boundary for the future frontend's generated TypeScript API client (see
// .omc/plans/frontend-redesign-seerr.md, Stage 0 / Guardrail #4).
//
// Why this package exists: today's handlers decode/encode JSON using
// unexported request structs (e.g. internal/api's upsertConnectionRequest)
// and raw internal/domain structs (e.g. tmdb.Item, connections.Summary)
// encoded directly. A source-parsing codegen tool CAN see unexported types
// if pointed at internal/api directly, but that would emit a TypeScript
// type for every internal handler struct across the whole package — far
// more than a frontend client should ever import, and it would silently
// change shape every time an unrelated handler's internal request struct
// changed. This package is the deliberate alternative: a small, hand-picked,
// EXPORTED set of types that mirror only what a frontend actually needs to
// send/receive, kept in one place a codegen tool can be pointed at in
// isolation (see internal/apidto/gen).
//
// Scope (Stage 0 / Stage 1 only — see README.md "Scope grows per stage"):
// auth boot (setup/login/status/mode/OIDC config/API key management) and
// Discover's read-only surface (poster items + availability badges), the
// exact set Stage 1's toolchain slice consumes. Stages 2-4 add their own
// DTOs here as their frontend work lands — this file is a starting point,
// not a final inventory, per Guardrail #4.
//
// These types are currently PARALLEL COPIES of the shapes already produced
// by internal/api's handlers (authStatusResponse, oidcStatusResponse,
// tmdb.Item, etc.) and internal/auth's APIKeyStatus. Stage 0 defines them
// but does not wire any handler to use them — no frontend exists yet to
// prove a wiring change against, and touching the auth handlers here would
// add lockout risk for zero benefit (see README.md). Stage 1 is expected to
// converge the real handlers onto these exact types, at which point the
// parallel definitions in internal/api collapse into a single source of
// truth here.
//
// Field names, JSON tags, and types below match the current wire format
// exactly (same lowerCamelCase JSON keys the existing handlers already
// emit) so that a future Stage-1 handler swap is a type substitution, not a
// wire-format change.
//
// IMPORTANT — three-state optional-secret fields (Guardrail #5): see
// ConnectionUpsertRequest.APIKey's doc comment and README.md's "Three-state
// secret mapping rule" section before generating or consuming a TypeScript
// client for any *string field in this package.
package apidto

// --- Auth boot: setup, login, status --------------------------------------

// SetupRequest is the body of POST /api/auth/setup — SAK's one-time,
// first-run login bootstrap. Mode selects the auth strategy ("password" is
// the default when Mode is omitted, "oidc", or "none").
// AcknowledgeInsecure must be true to select Mode "none". The four
// OIDC* fields are required together, and only meaningful, when
// Mode == "oidc".
type SetupRequest struct {
	Username            string `json:"username"`
	Password            string `json:"password"`
	Mode                string `json:"mode"`
	AcknowledgeInsecure bool   `json:"acknowledgeInsecure"`
	OIDCIssuerURL       string `json:"oidcIssuerUrl,omitempty"`
	OIDCClientID        string `json:"oidcClientId,omitempty"`
	OIDCClientSecret    string `json:"oidcClientSecret,omitempty"`
	OIDCRedirectURL     string `json:"oidcRedirectUrl,omitempty"`
}

// SetupResponse is returned by POST /api/auth/setup only for "oidc"-mode
// setup (empty body / 204 for "password"/"none"). Exactly one of APIKey or
// APIKeyNote is populated: APIKey is a one-time break-glass credential
// revealed ONCE, never retrievable again; APIKeyNote is present instead
// when SAKMS_API_KEY is set via environment (no settings-managed key is
// minted in that case — the env value IS the break-glass credential).
type SetupResponse struct {
	APIKey     string `json:"apiKey,omitempty"`
	APIKeyNote string `json:"apiKeyNote,omitempty"`
}

// LoginRequest is the body of POST /api/auth/login — only meaningful when
// the active auth mode is "password" (checked server-side; "oidc" logs in
// via a full-page redirect to /api/auth/oidc/login instead, and "none" has
// no login step).
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthStatusResponse is GET /api/auth/status's response — the one call the
// frontend's boot sequence makes before it knows anything else about the
// instance, deciding between the setup wizard, the login screen, and the
// app (see Guardrail #2's 3-way boot branch). Mode is one of "password",
// "oidc", or "none".
type AuthStatusResponse struct {
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	Mode          string `json:"mode"`
}

// AuthModeResponse is GET /api/auth/mode's response.
type AuthModeResponse struct {
	Mode string `json:"mode"`
}

// AuthModeRequest is PUT /api/auth/mode's body — switches the ALREADY
// authenticated operator's active auth mode. AcknowledgeInsecure must be
// true to switch into "none" (mirrors SetupRequest's same field for the
// first-run case).
type AuthModeRequest struct {
	Mode                string `json:"mode"`
	AcknowledgeInsecure bool   `json:"acknowledgeInsecure"`
}

// --- OIDC config (post-first-run Settings switch) --------------------------

// OIDCStatusResponse is GET /api/auth/oidc's response. HasSecret reports
// whether a client secret is currently stored; the secret itself is NEVER
// returned (mirrors ConnectionSummary's HasAPIKey/KeySuffix pattern for the
// same reason — see README.md).
type OIDCStatusResponse struct {
	IssuerURL   string `json:"issuerUrl"`
	ClientID    string `json:"clientId"`
	RedirectURL string `json:"redirectUrl"`
	HasSecret   bool   `json:"hasSecret"`
}

// OIDCConfigRequest is PUT /api/auth/oidc's body — sets/replaces the
// OIDC provider config for an already-configured instance. Unlike
// ConnectionUpsertRequest.APIKey, ClientSecret here is a plain (non-pointer)
// required field: every PUT to this endpoint must supply the full config,
// there is no "leave secret unchanged" partial-update mode for OIDC config
// today.
type OIDCConfigRequest struct {
	IssuerURL    string `json:"issuerUrl"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RedirectURL  string `json:"redirectUrl"`
}

// --- API key management -----------------------------------------------------

// APIKeyStatusResponse is GET /api/apikey's response. Source is one of
// "env" (SAKMS_API_KEY active), "settings" (a regenerated key is stored),
// or "none" (no key exists). The key itself is never returned here — only
// KeySuffix, matching ConnectionSummary's masked-display convention.
type APIKeyStatusResponse struct {
	HasKey    bool   `json:"hasKey"`
	KeySuffix string `json:"keySuffix,omitempty"`
	Source    string `json:"source"`
}

// APIKeyRegenerateResponse is POST /api/apikey/regenerate's response — the
// one place the full API key crosses the API boundary. Shown once, never
// retrievable again afterward (same one-shot-reveal contract as
// SetupResponse.APIKey).
type APIKeyRegenerateResponse struct {
	APIKey    string `json:"apiKey"`
	KeySuffix string `json:"keySuffix"`
}

// --- Setup wizard status -----------------------------------------------------

// ModeStatus reports what's configured for one mode (Mode is one of
// "movies", "series", "adult") — enough for the setup wizard to know which
// steps are already done and skip past them.
type ModeStatus struct {
	Mode          string `json:"mode"`
	Available     bool   `json:"available"`
	ArrConfigured bool   `json:"arrConfigured"`
}

// SetupStatusResponse is GET /api/setup/status's response — a pure read
// model over what's already configured, driving whether the setup wizard
// shows itself at all and which of its steps it can skip.
type SetupStatusResponse struct {
	Modes []ModeStatus `json:"modes"`
	// JellyfinConfigured keeps its JSON field name for the frontend, but as
	// of the service-connections registry (internal/serviceconn) it means "at
	// least one media player of any provider (Jellyfin/Emby/Plex) is
	// registered" — not literally Jellyfin only. Renaming the wire field is
	// unnecessary churn for a purely additive meaning broadening.
	JellyfinConfigured bool `json:"jellyfinConfigured"`
	OllamaConfigured   bool `json:"ollamaConfigured"`
	Dismissed          bool `json:"dismissed"`
	AnyConfigured      bool `json:"anyConfigured"`
}

// DismissSetupRequest is PUT /api/setup/dismissed's body.
type DismissSetupRequest struct {
	Dismissed bool `json:"dismissed"`
}

// --- Discover (read-only) ----------------------------------------------------

// DiscoverItem is one TMDB trending/popular result for Movies/Series
// Discover (GET /api/modes/{mode}/discover) — mirrors tmdb.Item's exact
// wire shape. MediaType is "movie" or "tv".
type DiscoverItem struct {
	ID          int     `json:"id"`
	Title       string  `json:"title"`
	PosterPath  string  `json:"posterPath"`
	Overview    string  `json:"overview"`
	ReleaseDate string  `json:"releaseDate"`
	VoteAverage float64 `json:"voteAverage"`
	MediaType   string  `json:"mediaType"`
}

// SeriesSearchItem is one Rename SearchTakeover hit from TVDB-backed series
// search (GET /api/modes/series/tvdb-search). TmdbID is resolved via TMDB's
// tvdb_id cross-reference for repick/move commit. For kind=episode, Title is
// the episode name and SeriesTitle is the parent show; SeasonNumber and
// EpisodeNumber carry the slot for a one-click commit without step 2.
type SeriesSearchItem struct {
	TmdbID        int    `json:"tmdbId"`
	Title         string `json:"title"`
	SeriesTitle   string `json:"seriesTitle,omitempty"`
	ReleaseDate   string `json:"releaseDate,omitempty"`
	SeasonNumber  *int   `json:"seasonNumber,omitempty"`
	EpisodeNumber *int   `json:"episodeNumber,omitempty"`
}

// AdultDiscoverItem is one TPDB scene result for Adult Discover
// (GET /api/modes/adult/discover) — scene-shaped, not title-shaped (Studio
// substitutes for a studio/site name). Date is TPDB's release date string,
// unparsed. Image is the scene thumbnail URL served from TPDB's own image CDN
// (cdn.theporndb.net); it is frequently empty (many scenes have no art), so
// the client must render a text-only card when blank and route non-empty
// values through the image proxy (GET /api/images/proxy?url=), never
// hot-linking TPDB directly (plan Decision #7). DurationSeconds is the
// scene's pre-grab runtime in seconds (see internal/tpdbrest.Scene.Duration
// for sourcing/confidence: documented-shape + corroborated by two
// independent sources, not live-confirmed against a real TPDB instance); it
// may be 0 (unknown), which the auto-grab bitrate scorer (Stage 2) must
// treat as "skip the pre-grab bitrate check," never a real zero-length
// runtime or a divide-by-zero input.
//
// Rating is the scene's own numeric rating (TPDB's "rating" field; the spec's
// example value is the integer 5). It backs Adult Discover's "Highest Rated"
// row, which the backend produces by re-sorting ONE browse page by this field
// descending — a page-local ordering, NOT a true global popularity ranking (see
// internal/tpdbrest.BrowseScenes' doc). May be 0 (absent/unrated).
//
// Source names which upstream catalog the scene came from: "tpdb", "stashdb",
// or "fansdb". TPDB's own rows and the merged "Recently Released" feed set it
// so the card can show a provenance label; stash-box has no numeric rating, so
// a "stashdb"/"fansdb" scene's Rating is always 0.
//
// Slug is TPDB's URL-friendly scene identifier, used by the Discover detail
// popup's "More on TPDB" external link (theporndb.net/scenes/{slug}, NOT
// {id} — see internal/tpdbrest.Scene.Slug for sourcing). Always empty for a
// "stashdb"/"fansdb" scene: those sites' own detail pages are UUID-path
// (stashdb.org/scenes/{id}), so the popup links via ID for them instead.
//
// ReleaseTitle is only populated for a scene sourced from the newest-rows
// pipeline (see AdultNewestReleaseItem.ReleaseTitle) — the popup/Grab dialog
// thread it through as AutoGrabRequest.ReleaseTitle when present. Always ""
// for a plain TPDB/StashDB/FansDB catalog browse item (no associated
// Prowlarr release to remember), which falls back to the Studio+Title
// query, same as before this field existed.
//
// Genres/Performers back the Discover detail popup's tags/performers list.
// The two have DIFFERENT source coverage — don't read them as one field.
//
// Genres is populated for TPDB-sourced items (catalog browse and newest-rows
// alike — see internal/tpdbrest.Scene.Tags and
// adultnewest.MatchedRelease.Genres for sourcing) AND, since 2026-08-02, for
// StashDB/FansDB items: stashbox.Scene.Tags is live-verified present on both
// stashdb.org and fansdb.cc, and the browse query requests tags { name }.
//
// Performers has no stash-box source and is not gaining one: stashbox.Scene
// carries no performers field at all, so no stash-box path
// (identify/boxlookup.go, identify/enrichnewest.go's stashboxSceneToMatch)
// supplies performer names — only the TPDB ones do. Stated as a fact about
// the SOURCE rather than about every item, because a pooled row carries
// whatever its own identification recorded.
//
// Both omitempty since plenty of items (a pre-existing cached entity, a
// stash-box scene with no tags) legitimately have neither.
type AdultDiscoverItem struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Studio          string   `json:"studio"`
	Date            string   `json:"date"`
	Image           string   `json:"image"`
	DurationSeconds int      `json:"durationSeconds"`
	Rating          float64  `json:"rating"`
	Source          string   `json:"source"`
	Slug            string   `json:"slug"`
	ReleaseTitle    string   `json:"releaseTitle,omitempty"`
	Genres          []string `json:"genres,omitempty"`
	Performers      []string `json:"performers,omitempty"`
	// DownloadURL/Protocol/SizeBytes are the feed enclosure for a pooled,
	// feed-sourced entity — populated ONLY when the item's feed is currently
	// fresh (the backend builds them via FeedHealth.DirectGrabURL). When present
	// the Grab dialog threads them into AutoGrabRequest.DownloadURL/
	// DownloadProtocol for a direct grab; when empty (browse-only, or a feed not
	// currently fresh) the card falls back to the Prowlarr search path (D4/D5).
	DownloadURL string `json:"downloadUrl,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	// Seeders is the Prowlarr seeder count for a Show More (page>1) result,
	// used server-side for duplicate winner-selection before the item is
	// returned. Pool-sourced (page-1) items leave it 0 (no Prowlarr seeder
	// data). omitempty to match this struct's optional-field cluster.
	Seeders int `json:"seeders,omitempty"`
}

// AdultNewestScenesPage is the paginated envelope for the Adult "newest"
// Performers/Studios drill-down
// (GET /api/modes/adult/discover/newest/entity-scenes?kind=&name=&page=N),
// matching PaginatedStrip's {items, hasMore} load(page) contract. page=1 (the
// initial drill-down open) returns ONLY pool-matched items and fires NO Prowlarr
// call, with HasMore always true — Show More is always offered at least once
// (even for an entity the pool has zero items for right now, since Prowlarr
// hasn't been tried yet). page>1 (an explicit Show More click) fires exactly one
// Prowlarr search, returns its confidently-matched (freshly-enriched or cached)
// results with unmatched items dropped, and HasMore always false — Prowlarr
// doesn't paginate further, so there is no page 3.
type AdultNewestScenesPage struct {
	Items   []AdultDiscoverItem `json:"items"`
	HasMore bool                `json:"hasMore"`
}

// AdultDescription is the Adult Discover description/bio payload
// (GET /api/modes/adult/discover/description). Text is "" whenever no catalog has
// real data — the frontend renders NOTHING in that case (spec AC5/AC6: no
// placeholder, no AI fallback, no empty section). Source echoes the box actually
// consulted, or "" when none was — for a scene that strictly follows AC4 (the
// scene's own entity_source), but for a performer or studio it is ALWAYS "tpdb"
// under the shipped Option A design, regardless of the entity's own
// entity_source: live schema introspection (2026-08-02) proves neither stash-box
// endpoint exposes a performer bio or a studio description field, so a non-empty
// entity bio came from TPDB by construction (see EntityBio in
// internal/identify/entitydetail.go).
//
// There is deliberately NO Tags field: live schema introspection (2026-08-02)
// confirms neither stash-box nor TPDB exposes tags on a performer or a studio, so
// there is no source to populate one. See the implementation plan §0.3.
type AdultDescription struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// SearchReleaseResult is one scored release the manual Search release-picker
// offers a human to pick from (GET /api/modes/{mode}/search for Movies/Series;
// carried inline per scene by AdultSearchScene for Adult). It mirrors the wire
// shape internal/api's searchHandler has always emitted — nothing here is
// persisted; internal/grabs tracks a release once it's actually grabbed (see
// grabHandler). The four grab-bearing fields (Title/Protocol/Size/DownloadURL)
// are exactly what Prowlarr returned, never mutated post-fetch (grab-safety).
type SearchReleaseResult struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	Protocol    string `json:"protocol"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	DownloadURL string `json:"downloadUrl"`
	PublishDate string `json:"publishDate"`
	Score       int    `json:"score"`
}

// AdultSearchScene is one identified scene card in the Adult catalog-Search
// response — the scene identity (mirroring an Adult Discover card) plus its
// release variants carried INLINE, so clicking the card opens the release
// picker with zero further network calls (D4/D5 direct-grab enclosure for a
// fresh pooled scene, plus any Prowlarr releases whose raw title matched this
// scene). Quality variants of one scene are collapsed to a single card here,
// while Releases keeps each distinct variant individually selectable (the two
// deliberately-opposite dedup layers — see internal/api/searchdedup.go).
type AdultSearchScene struct {
	Scene    AdultDiscoverItem     `json:"scene"`
	Releases []SearchReleaseResult `json:"releases"`
}

// AdultSearchScenesPage is the Adult catalog-Search envelope
// (GET /api/modes/adult/search?q=&page=N), matching the {items, hasMore}
// load(page) contract the frontend's paginated strips use. page=1 (submit)
// fires exactly ONE bounded Prowlarr search and returns RSS-pool scenes plus
// Prowlarr-matched scenes; page>1 pages the RSS pool ONLY and fires ZERO
// further Prowlarr (one Prowlarr call per one explicit operator action — the
// action being search-submit for Adult).
type AdultSearchScenesPage struct {
	Items   []AdultSearchScene `json:"items"`
	HasMore bool               `json:"hasMore"`
}

// StudioSummary is one entry in Adult Discover's Studios row
// (GET /api/modes/adult/studios) — a TPDB site (studio) reduced to just what a
// browse card and its drill-down link need. ID is the opaque TPDB site id (used
// as the {id} path segment of GET /api/modes/adult/studios/{id}/scenes). Image
// is a single chosen studio image URL (first non-empty of TPDB's logo/poster/
// favicon), frequently empty (no art) — render a text-only card when blank and
// route non-empty values through the image proxy, never hot-link TPDB directly.
type StudioSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Source string `json:"source"`
}

// PerformerSummary is one entry in Adult Discover's Performers row
// (GET /api/modes/adult/performers) — a TPDB performer reduced to browse-card
// fields. ID is the opaque TPDB performer id (the {id} path segment of
// GET /api/modes/adult/performers/{id}/scenes). Image is a single chosen
// performer image URL (first non-empty of TPDB's image/thumbnail/face),
// frequently empty — same text-fallback + image-proxy rule as StudioSummary.
type PerformerSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Source string `json:"source"`
}

// PosterResponse is GET /api/modes/{mode}/poster's response — the lazily
// resolved TMDB poster path for one library card, keyed by tmdbId (Movies/
// Series only). PosterPath is a bare TMDB path (e.g. "/abc.jpg") the client
// turns into a proxied image URL, or "" when TMDB has no art on file (the
// card then renders its text fallback). Mirrors the availability probe's
// per-card, on-demand shape rather than an N+1 on the library list endpoint.
type PosterResponse struct {
	PosterPath string `json:"posterPath"`
}

// --- Connections (reference implementation of the three-state secret rule) -

// ConnectionSummary is one entry of GET /api/connections's response — what's
// safe to expose about a configured connection: never the secret itself,
// only whether one is set and its last 4 characters (masked display).
// Included here alongside ConnectionUpsertRequest even though the
// Connections/Settings UI itself isn't built until Stage 4 — see
// ConnectionUpsertRequest's doc comment for why. FixedURL carries the real
// package-constant base URL for fixed-URL services (empty for the rest), so
// the frontend can show it read-only instead of hardcoding those Go values.
type ConnectionSummary struct {
	Service   string `json:"service"`
	URL       string `json:"url"`
	Username  string `json:"username,omitempty"`
	HasAPIKey bool   `json:"hasApiKey"`
	KeySuffix string `json:"keySuffix,omitempty"`
	UpdatedAt string `json:"updatedAt"`
	FixedURL  string `json:"fixedUrl,omitempty"`
}

// ConnectionUpsertRequest is PUT /api/connections/{service}'s body —
// included in Stage 0's curated set (ahead of Stage 4, when Settings'
// actual frontend lands) specifically BECAUSE it carries the single most
// safety-critical mapping rule in this whole DTO boundary (Guardrail #5)
// and needed to be proven against the chosen codegen tool before that tool
// choice was finalized, not discovered as a surprise in Stage 4.
//
// APIKey is a pointer so the three states a client MUST be able to express
// survive the JSON round-trip (json.Decode sets it accordingly; omitempty
// only affects marshaling, never decoding):
//
//   - field ABSENT from the request body entirely (nil)  → preserve the
//     stored secret. The server never sends the real secret back to a
//     client (see ConnectionSummary above — only HasAPIKey/KeySuffix are
//     exposed), so an untouched, blank key input MUST be omitted from the
//     JSON body, never sent as "".
//   - field present as ""  (&"")                          → explicitly
//     clear the stored secret (e.g. switching to a service that needs
//     none, like Ollama).
//   - field present, non-empty  (&"sk-...")                → set/replace
//     the stored secret.
//
// TypeScript CANNOT express this three-way distinction as a type — both
// a field absent entirely and a field present with an empty-string value
// collapse to the same `string | undefined` a source-parsing generator
// emits for a Go *string.
// See README.md's "Three-state secret mapping rule" section for the
// generated TypeScript shape and the load-bearing prose rule a frontend
// MUST follow by convention (not by the type system) when building this
// request body.
type ConnectionUpsertRequest struct {
	URL      string  `json:"url"`
	Username string  `json:"username,omitempty"`
	APIKey   *string `json:"apiKey,omitempty"`
}

// --- Service connections registry (multi-connection: usenet + players) ----
//
// Mirrors internal/serviceconn's Connection/Summary — the registry that
// replaced the old singleton `connections` rows for the two services an
// operator can configure MORE THAN ONE of: Usenet (NNTP) subscriptions and
// media players (Jellyfin/Emby/Plex). See internal/serviceconn's package doc
// for why this is a split, not a replacement, of ConnectionSummary/
// ConnectionUpsertRequest above — those two stay exactly as they are and keep
// serving the surviving singleton services (TMDB, TVDB, StashDB, FansDB,
// TPDB, Trakt, Prowlarr, Ollama, Stash, ...).
//
// Every DTO in this block splits its shape fields by Kind, matching
// serviceconn.Connection's own convention: URL is player-shaped; Host/Port/
// TLS/MaxConns are usenet-shaped; Username applies to both; the field that
// doesn't apply to a given Kind/Provider is simply left zero.

// ServiceConnectionSummary is one registry row as exposed over the API — the
// secret is never round-tripped, only whether one is set and its last 4
// characters (HasSecret/SecretSuffix), mirroring ConnectionSummary's
// HasAPIKey/KeySuffix convention. GET /api/service-connections returns a
// list of these.
type ServiceConnectionSummary struct {
	ID           int64    `json:"id"`
	Kind         string   `json:"kind"`     // "usenet" | "player"
	Provider     string   `json:"provider"` // "nntp" | "jellyfin" | "emby" | "plex"
	Label        string   `json:"label"`
	Enabled      bool     `json:"enabled"`
	SortOrder    int      `json:"sortOrder"`
	URL          string   `json:"url,omitempty"`
	Host         string   `json:"host,omitempty"`
	Port         int      `json:"port,omitempty"`
	TLS          bool     `json:"tls,omitempty"`
	MaxConns     int      `json:"maxConns,omitempty"`
	Username     string   `json:"username,omitempty"`
	HasSecret    bool     `json:"hasSecret"`
	SecretSuffix string   `json:"secretSuffix,omitempty"`
	Modes        []string `json:"modes"` // player rows only; always empty for usenet
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

// ServiceConnectionCreateRequest is POST /api/service-connections's body.
// Secret is not three-state here (there is no stored secret to preserve on a
// brand-new row, so "absent" and "empty" both simply mean "none") but is
// still a pointer, matching the handler's actual decode target
// (serviceConnectionRequest.Secret in internal/api/serviceconns.go) and
// serviceConnectionRequest's own doc comment on why: json.Decode needs a
// pointer to tell "field absent" apart from "field present as empty string",
// even though on create both are handled identically. Modes only applies to
// player rows (serviceconn.Store.Create writes it via replaceModes); leave it
// empty/omitted for a usenet row.
type ServiceConnectionCreateRequest struct {
	Kind     string   `json:"kind"`
	Provider string   `json:"provider"`
	Label    string   `json:"label,omitempty"`
	Enabled  bool     `json:"enabled"`
	URL      string   `json:"url,omitempty"`
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	TLS      bool     `json:"tls,omitempty"`
	MaxConns int      `json:"maxConns,omitempty"`
	Username string   `json:"username,omitempty"`
	Secret   *string  `json:"secret,omitempty"`
	Modes    []string `json:"modes,omitempty"`
}

// ServiceConnectionUpdateRequest is PUT /api/service-connections/{id}'s body.
// sort_order and mode assignment are NOT editable here — sort_order has its
// own reorder endpoint precedent and modes are ServiceConnectionModesRequest's
// job, mirroring serviceconn.Store.Update's own division of labor (Update
// ignores incoming Modes; SetModes owns them).
//
// Secret follows the same three-state rule as ConnectionUpsertRequest.APIKey
// above (see that field's doc comment and README.md's "Three-state secret
// mapping rule" section): field ABSENT (nil) preserves the stored secret,
// present as "" clears it, present non-empty replaces it.
type ServiceConnectionUpdateRequest struct {
	Kind     string  `json:"kind"`
	Provider string  `json:"provider"`
	Label    string  `json:"label,omitempty"`
	Enabled  bool    `json:"enabled"`
	URL      string  `json:"url,omitempty"`
	Host     string  `json:"host,omitempty"`
	Port     int     `json:"port,omitempty"`
	TLS      bool    `json:"tls,omitempty"`
	MaxConns int     `json:"maxConns,omitempty"`
	Username string  `json:"username,omitempty"`
	Secret   *string `json:"secret,omitempty"`
}

// ServiceConnectionModesRequest is PUT /api/service-connections/{id}/modes's
// body — the sole way to change which modes a player row is assigned to
// (serviceconn.Store.SetModes replaces the assignment wholesale). Rejected by
// the Store for a usenet row (only player connections carry modes).
type ServiceConnectionModesRequest struct {
	Modes []string `json:"modes"`
}

// ServiceConnectionTestRequest is POST /api/service-connections/test's body —
// enough to construct a client and make one real, read-only call against it,
// the registry-row twin of ConnectionTestRequest (internal/api/connections.go)
// for the two multi-connection kinds. Nothing here is persisted. Like
// ServiceConnectionCreateRequest, URL is player-shaped and Host/Port/TLS are
// usenet-shaped — the caller populates whichever set matches Provider.
type ServiceConnectionTestRequest struct {
	Provider string `json:"provider"` // "nntp" | "jellyfin" | "emby" | "plex"
	URL      string `json:"url,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	TLS      bool   `json:"tls,omitempty"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

// ServiceConnectionTestResult reports whether the test call succeeded. A
// false OK with a populated Error is the normal, expected shape for "wrong
// URL" or "wrong key" — not a server-side failure. Mirrors
// ConnectionTestResult's shape exactly.
type ServiceConnectionTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// --- Stage 2: auto-grab (Discover becomes mutating) ------------------------

// Grab mirrors internal/grabs.Grab's exact wire shape — the record SAK keeps
// for one release it has sent to a download client. Exposed here so the
// frontend's Grabs view and the auto-grab response share one generated
// TypeScript type instead of hand-duplicating the shape.
//
// FlaggedForReview / FlagReason are the ADVISORY post-grab mislabel signal
// (set by checkImportHandler via internal/autograb.RuntimeMismatch, Movies
// only for now): they do NOT mean the import failed — the import already
// succeeded — only that the imported file's runtime looked inconsistent with
// its metadata and a human might want to eyeball it. The Grabs view must say
// so in its copy, never present the flag as an error.
type Grab struct {
	ID               int64  `json:"id"`
	Mode             string `json:"mode"`
	Title            string `json:"title"`
	TMDBID           int    `json:"tmdbId,omitempty"`
	TVDBID           int    `json:"tvdbId,omitempty"`
	SeasonNumber     int    `json:"seasonNumber,omitempty"`
	EpisodeNumber    int    `json:"episodeNumber,omitempty"`
	SeasonSpecified  bool   `json:"seasonSpecified,omitempty"`
	QualityProfileID int    `json:"qualityProfileId,omitempty"`
	Indexer          string `json:"indexer"`
	Protocol         string `json:"protocol"`
	DownloadClient   string `json:"downloadClient"`
	ClientRef        string `json:"clientRef,omitempty"`
	Status           string `json:"status"`
	RootFolderPath   string `json:"rootFolderPath"`
	FlaggedForReview bool   `json:"flaggedForReview,omitempty"`
	FlagReason       string `json:"flagReason,omitempty"`
	// RetryAfter/RetryCount/RetryReason are the PendingRetry state — set only
	// when Status == "pending_retry" (grabs.Grab.SetPendingRetry clears them
	// back to zero for every other status). RetryAfter is an RFC3339Nano UTC
	// timestamp string (grabs.FormatTime), not a Unix number, matching
	// CreatedAt/UpdatedAt's convention. Mirrors grabs.Grab's own JSON tags
	// exactly (internal/grabs/grabs.go) — added here so the Requests screen
	// (FE-5, a later wave) has a DTO to render against.
	RetryAfter  string `json:"retryAfter,omitempty"`
	RetryCount  int    `json:"retryCount,omitempty"`
	RetryReason string `json:"retryReason,omitempty"`
	// HoldUntil mirrors grabs.Grab.HoldUntil — a Calendar pre-release request's
	// hold timestamp, "" for every grab that did not originate as one.
	//
	// It is deliberately declared on BOTH structs, and neither copy is
	// redundant: the History wire payload carries grabs.Grab, encoded directly
	// by listGrabsHandler with no DTO mapping step, while the TypeScript Grab
	// type is generated from THIS struct. Dropping it here makes the field
	// invisible to TypeScript; dropping it on grabs.Grab makes it absent from
	// the payload. The two structs are already divergent by three other fields
	// (downloadGid/downloadStatus/downloadStagingPath exist only on
	// grabs.Grab), so they are two independent copies of "what a grab looks
	// like", not one type duplicated by accident.
	HoldUntil string `json:"holdUntil,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// AutoGrabRequest is POST /api/modes/{mode}/autograb's body — Discover's
// one-click unattended grab trigger for exactly one title/scene. Which fields
// matter is mode-specific:
//   - Movies:  TMDBID (drives the id-scoped Prowlarr search AND the TMDB
//     runtime lookup the bitrate scorer needs) + Title.
//   - Series:  TMDBID + Title + SeasonNumber/EpisodeNumber/SeasonSpecified —
//     the picker's selection ("one click PER season/episode selection", per
//     the plan's per-mode nuance). No per-episode runtime exists pre-grab
//     today, so Series candidates all grade as unknown-bitrate and the call
//     returns the manual fallback list rather than auto-grabbing (documented
//     behavior, not a bug). SeasonSpecified must be threaded through so a
//     deliberate Season-0/Specials grab isn't misread as "no season picked"
//     (see grabs.Grab.SeasonSpecified / checkImportHandler).
//   - Adult:   Title + Studio (the free-text Prowlarr query fallback) +
//     ReleaseTitle (preferred query when present — the raw Prowlarr release
//     title that first matched this entity, see
//     adultnewest.MatchedRelease.FirstSeenReleaseTitle's doc comment for why
//     it's more reliable than reconstructing a query from Title/Studio) +
//     DurationSeconds (TPDB's pre-grab runtime → the scorer's
//     RuntimeSeconds; 0 = unknown, handled neutrally).
//
// The same AutoGrabRequest is reused per item by the bounded multi-select
// sibling POST /api/autograb-batch (see AutoGrabBatchRequest below). That
// endpoint is the one documented, user-approved exception to the "one title per
// call" framing here; this single mode-scoped route is unchanged.
type AutoGrabRequest struct {
	Title           string `json:"title"`
	TMDBID          int    `json:"tmdbId,omitempty"`
	Studio          string `json:"studio,omitempty"`
	SeasonNumber    int    `json:"seasonNumber,omitempty"`
	EpisodeNumber   int    `json:"episodeNumber,omitempty"`
	SeasonSpecified bool   `json:"seasonSpecified,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	// ReleaseTitle is Adult-only — see this struct's doc comment above.
	ReleaseTitle string `json:"releaseTitle,omitempty"`
	// DownloadURL/DownloadProtocol are the direct-grab fields (Adult feed
	// entities): when DownloadURL is present the server dispatches it straight to
	// the download client, skipping the Prowlarr search entirely — identically
	// for the single (autoGrabHandler) and bulk (grabOneBatchItem) entrypoints,
	// so one code path serves both (D4/C1). Empty ⇒ the existing Prowlarr search
	// path runs, unchanged. The card only carries these while its feed is fresh
	// (see FeedHealth.DirectGrabURL); otherwise it falls back to the Prowlarr path.
	DownloadURL      string `json:"downloadUrl,omitempty"`
	DownloadProtocol string `json:"downloadProtocol,omitempty"`
	// Box/SceneID carry the catalog scene identity (stash-box source name + UUID)
	// for Adult requests. The resolver uses them to build a stable cache key
	// (box:sceneId) so the persisted-release cache can serve without a Prowlarr
	// re-search. Only sent when the item's source is a catalog source with a
	// non-empty id — never for prowlarr-sourced Show More items (see A2(c) in
	// .omc/plans/autopilot-impl-adult-release-persistence.md §0).
	Box     string `json:"box,omitempty"`
	SceneID string `json:"sceneId,omitempty"`
	// Performers is a SOFT identity signal for Adult unattended grabs — used by
	// adultIdentityWeak to decide whether to stage for approval rather than
	// dispatch silently. Never hard-rejects a release (see §7.1). omitempty.
	Performers []string `json:"performers,omitempty"`
	// Indexer names the real Prowlarr indexer for a cache-sourced grab. Empty on
	// the existing RSS-feed path (grabDirectEnclosure stamps "feed" when blank —
	// see §6.3 of the adult-release-persistence plan). omitempty so RSS grabs
	// never send the key at all.
	Indexer string `json:"indexer,omitempty"`
}

// AutoGrabCandidate is one graded release in an auto-grab manual-fallback list
// (AutoGrabResponse.Candidates). It pairs the grade (Status = why it did/didn't
// auto-qualify, Score = the bitrate ranking key) with the exact release
// identity the frontend needs to grab it manually via
// POST /api/modes/{mode}/search/grab — one release per click, never a batch.
// Status is one of internal/autograb.Status's values ("qualified",
// "below-floor", "mislabeled", "low-seeders", "unknown-bitrate",
// "unknown-resolution").
//
// "one release per click, never a batch" above describes the manual
// POST /search/grab affordance and stays true for it; AutoGrabBatchResult
// (below) reuses this same candidate type for a bulk grab's per-item fallback
// pick list.
type AutoGrabCandidate struct {
	Title       string  `json:"title"`
	Indexer     string  `json:"indexer"`
	Protocol    string  `json:"protocol"`
	DownloadURL string  `json:"downloadUrl"`
	Size        int64   `json:"size"`
	Seeders     int     `json:"seeders"`
	Status      string  `json:"status"`
	Score       float64 `json:"score"`
	ImpliedMbps float64 `json:"impliedMbps"`
	FloorMbps   float64 `json:"floorMbps"`
	Qualified   bool    `json:"qualified"`
}

// AutoGrabResponse is POST /api/modes/{mode}/autograb's result — exactly one
// of two outcomes:
//   - Grabbed == true:  a release cleared every gate and was sent to the
//     download client. Grab is the recorded grab (also visible in the Grabs
//     view); Candidates is empty.
//   - Grabbed == false (Fallback == true): nothing auto-qualified —
//     Candidates is the ranked manual pick list (best bitrate score first,
//     the SAME score that gated auto-grab), each row labeled with why it
//     didn't qualify. The operator picks exactly one to grab; Grab is nil.
//
// Message is a short human summary for the UI.
//
// AutoGrabBatchResult (below) models one item of the bounded multi-select
// POST /api/autograb-batch on this same three-state shape — the batch is the
// documented, user-approved exception to the single-grab "one release per call"
// invariant, not a rewrite of it.
type AutoGrabResponse struct {
	Grabbed    bool                `json:"grabbed"`
	Fallback   bool                `json:"fallback"`
	Message    string              `json:"message"`
	Grab       *Grab               `json:"grab,omitempty"`
	Candidates []AutoGrabCandidate `json:"candidates,omitempty"`
}

// SearchAutoGrabOutcome is the toggle-ON response shape for a gated Usenet
// search — GET /api/modes/{mode}/search (Movies/Series) and
// GET /api/modes/adult/search (Adult), both routed through the shared
// runToggleGatedSearch (§2.3.1 of the connections-elimination plan) — used
// in place of the toggle-OFF candidate-list shapes
// ([]SearchReleaseResult / AdultSearchScenesPage) whenever
// usenet_autograb_enabled is on. The SAME shape covers all three modes.
//
// A toggle-ON response is never a candidate list: the endpoint has already
// picked (or definitively failed to pick) on the caller's behalf, so there
// is nothing left to manually choose from.
//
//   - AutoGrabbed == true:  a candidate cleared the quality floor and was
//     dispatched through the same dispatchToDownloadClient path RunAutoGrab
//     always uses. Outcome is "grabbed".
//   - AutoGrabbed == false, Outcome == "pending_retry": nothing cleared the
//     floor; a pending_retry grabs row was created (or an existing GID-less
//     row for the same identity was updated — see FindPendingRetry/GRAB-1)
//     so the 24h retry sweep (BE-7) can pick it up later. Reason explains
//     why (e.g. "no candidate cleared the quality floor").
//   - AutoGrabbed == false, Outcome == "failed": the row was classified as
//     permanently ungradeable (e.g. a confirmed 451/DMCA takedown) rather
//     than parked for another retry — reported honestly as "failed", never
//     as a lingering "pending_retry". (2026-08-01: retries are no longer
//     capped, so a row is never marked "failed" merely for exhausting a
//     retry budget — only for a genuinely terminal classification.)
//
// GrabID is populated in every outcome — it identifies the grabs row created
// or updated by this call, whether that row ended up Downloading/Completed
// (grabbed) or PendingRetry/Failed.
type SearchAutoGrabOutcome struct {
	AutoGrabbed bool   `json:"autoGrabbed"`
	Outcome     string `json:"outcome"` // "grabbed" | "pending_retry" | "failed"
	GrabID      int64  `json:"grabId"`
	Title       string `json:"title"`
	Reason      string `json:"reason,omitempty"`
}

// --- Discover bulk auto-grab: bounded multi-select exception ------------------
//
// AMENDED 2026-07-24 — POST /api/autograb-batch is a deliberate, documented,
// user-approved exception to the single-grab "no bulk action / one release per
// click, never a batch" invariant the AutoGrabRequest/AutoGrabCandidate/
// AutoGrabResponse doc comments above describe. Those comments stay factually
// true for the single-item POST /api/modes/{mode}/autograb route — it still
// grabs exactly one release per call. This batch endpoint is a separate, bounded
// path: an operator explicitly multi-selects already-reviewed Discover cards and
// the server grabs them SEQUENTIALLY (never concurrently — at most one Prowlarr
// search in flight across the whole batch, the same architectural rule that
// keeps Discover off per-card indexer probes), capped at MaxBatchGrabItems
// submitted items, with per-item skip-and-continue semantics. It reuses the
// single-grab pipeline per item; each item is a real, independent one-release
// grab, just looped under one request. See internal/api/autograb_batch.go and
// .omc/plans/discover-depth-features.md Feature 3.

// AutoGrabBatchItem is one entry in an AutoGrabBatchRequest: a mode plus the
// same per-title AutoGrabRequest the single endpoint takes. Each item carries
// its own Mode because the batch is cross-mode (a mixed Discover selection can
// span Movies/Series/Adult), unlike the mode-scoped single route whose mode
// lives in the URL path.
type AutoGrabBatchItem struct {
	Mode    string          `json:"mode"`
	Request AutoGrabRequest `json:"request"`
}

// AutoGrabBatchResult is one item's outcome (modeled on AutoGrabResponse, NOT
// apply-batch's binary {OK,Error}). Exactly one state is set per item — the
// classification never silently folds one into another:
//   - Grabbed == true:         the item cleared the floor and was sent to the
//     download client; Grab holds the recorded grab, Candidates empty.
//   - Fallback == true:        nothing auto-qualified; Candidates is the ranked
//     manual pick list, no grab was attempted.
//   - AlreadyGrabbing == true: an in-flight grab for this exact download already
//     existed (the idempotency guard — same infohash/GID), so no duplicate row
//     was recorded; Grab holds the existing in-flight grab. NOT counted as a
//     fresh Grabbed (it is neither a new acquisition nor a failure).
//   - Error != "":             the item failed (unknown mode, unconfigured
//     service, search error, ...); it was skipped and the batch continued.
//
// Index is the item's position in the submitted Items slice (stable even when
// items are reordered for display); Label is a short human tag (the request
// Title) for the results UI.
type AutoGrabBatchResult struct {
	Index           int                 `json:"index"`
	Mode            string              `json:"mode"`
	Label           string              `json:"label"`
	Grabbed         bool                `json:"grabbed"`
	Fallback        bool                `json:"fallback"`
	AlreadyGrabbing bool                `json:"alreadyGrabbing,omitempty"`
	Message         string              `json:"message,omitempty"`
	Error           string              `json:"error,omitempty"`
	Grab            *Grab               `json:"grab,omitempty"`
	Candidates      []AutoGrabCandidate `json:"candidates,omitempty"`
}

// AutoGrabBatchRequest is POST /api/autograb-batch's body — a flattened list of
// per-mode grab items. The cap (MaxBatchGrabItems) counts these submitted items:
// a season-expanded series contributes one item per selected season, so the cap
// bounds live acquisitions fired, not Discover cards selected.
type AutoGrabBatchRequest struct {
	Items []AutoGrabBatchItem `json:"items"`
}

// AutoGrabBatchResponse is POST /api/autograb-batch's result: one
// AutoGrabBatchResult per submitted item, in submission order. The HTTP status
// is always 200 (except the pre-loop empty/over-cap 400s) — per-item success or
// failure lives in the results, never the status code.
type AutoGrabBatchResponse struct {
	Results []AutoGrabBatchResult `json:"results"`
}

// --- Discover detail popup: on-demand per-resolution/tier/protocol availability -
//
// GET /api/modes/{mode}/discover/availability's response — the popup's one
// upfront preview fetch (a single, user-click-triggered Prowlarr search,
// filtered and graded — see internal/api/discover_availability.go's doc
// comment for the full flow). Flat structs, not a Go map: every existing DTO
// in this file is a flat struct, and it's unconfirmed whether cmd/gendto's
// TS codegen handles map types, so this avoids that risk (see the plan).

// AvailabilityPreview is the full 4-resolution grid — one upfront fetch backs
// every selector combination the popup's UI offers, so switching any
// selector re-renders instantly against already-fetched data (no refetch per
// selection change).
type AvailabilityPreview struct {
	Res2160 ResolutionAvailability `json:"res2160"`
	Res1080 ResolutionAvailability `json:"res1080"`
	Res720  ResolutionAvailability `json:"res720"`
	Res480  ResolutionAvailability `json:"res480"`
	// Diagnostics explains an EMPTY grid. Always populated; only ever read
	// by the popup when no cell anywhere carries a candidate.
	Diagnostics AvailabilityDiagnostics `json:"diagnostics"`
}

// AvailabilityDiagnostics carries the counts and rejection statuses the
// availability handler already computes for its own logging, so the popup can
// tell an empty grid's two genuinely different causes apart: no release was
// found at all, versus releases were found and every one of them was rejected
// during grading. Before this existed both cases arrived as an identical
// all-nil grid and the popup could only render silently-disabled selectors
// with no explanation (a real operator report — see
// internal/api/discover_availability.go).
type AvailabilityDiagnostics struct {
	// RawReleaseCount is how many releases Prowlarr returned for the search,
	// before any title/language filtering or grading. 0 means "nothing found"
	// — a search problem, not a quality one.
	RawReleaseCount int `json:"rawReleaseCount"`
	// MatchedReleaseCount is how many of those survived releasematch's
	// title/language filter and were actually graded. Strictly informational:
	// RawReleaseCount > 0 with MatchedReleaseCount == 0 means everything found
	// was for something else, which yields no rejection statuses at all.
	MatchedReleaseCount int `json:"matchedReleaseCount"`
	// RejectionReasons is the distinct, sorted set of autograb.Status values
	// (e.g. "low-seeders", "below-floor", "unknown-resolution") observed while
	// grading — raw status codes, not display strings, mapped to operator-
	// facing wording in the frontend the same way tier/protocol labels are.
	// Empty (omitted) whenever nothing reached grading, so a consumer must
	// never present a rejection cause it wasn't given.
	RejectionReasons []string `json:"rejectionReasons,omitempty"`
}

// ResolutionAvailability is one resolution bucket's 4-tier grid.
type ResolutionAvailability struct {
	Low      TierAvailability `json:"low"`
	Medium   TierAvailability `json:"medium"`
	High     TierAvailability `json:"high"`
	Lossless TierAvailability `json:"lossless"`
}

// TierAvailability is one (resolution, tier) cell's 2-protocol leaf. Usenet/
// Torrent are nil when autograb.Select found no qualifying candidate for that
// exact (resolution, tier, protocol) combination — the popup's selector
// greys out that option.
type TierAvailability struct {
	Usenet  *AvailabilityCandidate `json:"usenet"`
	Torrent *AvailabilityCandidate `json:"torrent"`
}

// AvailabilityCandidate is the winning release for one (resolution, tier,
// protocol) combination — everything the popup's Grab button needs to call
// the EXISTING POST /api/modes/{mode}/search/grab (no new grab endpoint; see
// the plan's "Grab" section). Score is autograb.Grade.Score (the
// bitrate-based ranking key), deliberately NOT release.ScoreCandidate — the
// same distinct scorer auto-grab already uses for tier-floor gating.
type AvailabilityCandidate struct {
	GUID        string  `json:"guid"`
	Title       string  `json:"title"`
	Indexer     string  `json:"indexer"`
	Protocol    string  `json:"protocol"`
	Size        int64   `json:"size"`
	Seeders     int     `json:"seeders"`
	DownloadURL string  `json:"downloadUrl"`
	PublishDate string  `json:"publishDate"`
	Score       float64 `json:"score"`
}

// TrailerResponse is GET /api/modes/{mode}/discover/trailer's result — the
// Discover detail popup's "Watch Trailer" link target. URL is "" when TMDB
// has no matching YouTube trailer on file for this title (see
// tmdb.Client.TrailerURL) — the frontend simply omits the link in that case,
// never treating an empty result as an error.
type TrailerResponse struct {
	URL string `json:"url"`
}

// --- Discover detail popup: richer per-title detail (Seerr-parity) ----------
//
// GET /api/modes/{mode}/discover/detail?tmdbId=N's response (Movies/Series
// only — Adult has no TMDB id and keeps its existing performers/genres popup).
// The handler fans the independent TMDB sub-calls (extended details, full
// credits, keywords, watch providers, recommendations) out in parallel and
// soft-fails each: any one failing yields an empty section, never a popup-wide
// error (see internal/api/discover_detail.go). Flat structs, no maps — same
// cmd/gendto TS-codegen constraint every DTO in this file follows.

// CastMember is one cast entry in the detail popup's cast row. ProfilePath is
// a bare TMDB image path (proxied by the frontend via /api/images/proxy —
// never an absolute URL), "" when TMDB has no headshot.
type CastMember struct {
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profilePath"`
}

// CrewMember is one KEY crew entry (Director/Writer-Screenplay/Producer/
// Editor — filtered server-side, see tmdb.keyCrewJobs). ProfilePath is a bare
// TMDB image path, proxied by the frontend.
type CrewMember struct {
	Name        string `json:"name"`
	Job         string `json:"job"`
	ProfilePath string `json:"profilePath"`
}

// WatchProviderDTO is one US subscription (flatrate) streaming service the
// title is available on — JustWatch-powered TMDB data, so the UI rendering it
// MUST show a "Powered by JustWatch" attribution (TMDB terms). LogoPath is a
// bare TMDB image path, proxied by the frontend.
type WatchProviderDTO struct {
	Name     string `json:"name"`
	LogoPath string `json:"logoPath"`
}

// ReleaseDateEntry is one dated release in the metadata sidebar's full
// (US-scoped) release-date list. Type is a human label mapped from TMDB's
// release-type enum ("Premiere"/"Theatrical (limited)"/"Theatrical"/"Digital"/
// "Physical"/"TV"); Date is the raw release_date string.
type ReleaseDateEntry struct {
	Type string `json:"type"`
	Date string `json:"date"`
}

// SeasonSummary is one season of a Series for the Discover picker's season
// grid. PosterPath is a bare TMDB path, proxied by the frontend like every
// other image path in this DTO (never an absolute URL). Episodes is populated
// by the detail handler's eager all-seasons prefetch and is EMPTY — not
// absent — for a season whose per-season fetch soft-failed; the grid renders a
// season card either way.
type SeasonSummary struct {
	SeasonNumber int              `json:"seasonNumber"`
	Name         string           `json:"name"`
	AirDate      string           `json:"airDate"`
	EpisodeCount int              `json:"episodeCount"`
	PosterPath   string           `json:"posterPath"`
	Episodes     []EpisodeSummary `json:"episodes"`
}

// EpisodeSummary is one episode in the picker's episode grid. StillPath is ""
// when TMDB has no still for this episode — a normal, expected condition — and
// the grid falls back to a title/number-only card.
type EpisodeSummary struct {
	EpisodeNumber int    `json:"episodeNumber"`
	Name          string `json:"name"`
	AirDate       string `json:"airDate"`
	Runtime       int    `json:"runtime"`
	StillPath     string `json:"stillPath"`
}

// OfficialRating is one catalog score on the Discover/Library detail popup.
// Source is a stable id the frontend uses to pick an icon ("tmdb", "trakt",
// "imdb"). ScoreKind is "ten" (0–10 decimal, e.g. IMDb 8.1 / TMDB 8.2) or
// "percent" (0–100, e.g. Trakt 82%). Votes is 0 when the source does not
// report a count. Badge is optional flavor text and unused for the current
// IMDb/TMDB/Trakt set. Empty sources are omitted from TitleDetail.Ratings
// rather than sent as zeros — the same soft-fail-empty-section contract as
// Cast/WatchProviders.
type OfficialRating struct {
	Source    string  `json:"source"`
	Label     string  `json:"label"`
	Score     float64 `json:"score"`
	ScoreKind string  `json:"scoreKind"`
	Votes     int     `json:"votes"`
	Badge     string  `json:"badge"`
}

// TitleDetail is the full Discover detail popup payload for a Movie/Series
// title. Every section is independently soft-failed by the handler, so any
// slice may be empty (that section simply doesn't render) without the whole
// response failing. All image-path fields (ProfilePath, LogoPath, and the
// PosterPath/StillPath carried by Seasons and their Episodes) are bare TMDB
// paths — proxied by the frontend, never absolute URLs. Seasons is the
// season/episode picker's grid data and is always empty for a Movie.
// Deliberately carries NO Revenue/Budget. Ratings is the official-score
// icon row (TMDB + Trakt + IMDb when Trakt or OMDb is configured); it is
// empty when every source soft-fails or is unconfigured — never persisted
// onto GET /tracked.
type TitleDetail struct {
	Status                string             `json:"status"`
	OriginalLanguage      string             `json:"originalLanguage"`
	ProductionCountry     string             `json:"productionCountry"`
	ProductionCountryCode string             `json:"productionCountryCode"`
	CollectionName        string             `json:"collectionName"`
	CollectionID          int                `json:"collectionId"`
	Networks              []string           `json:"networks"`
	Studios               []string           `json:"studios"`
	Runtime               int                `json:"runtime"`
	ReleaseDates          []ReleaseDateEntry `json:"releaseDates"`
	Genres                []string           `json:"genres"`
	Keywords              []string           `json:"keywords"`
	Cast                  []CastMember       `json:"cast"`
	Crew                  []CrewMember       `json:"crew"`
	WatchProviders        []WatchProviderDTO `json:"watchProviders"`
	Recommendations       []DiscoverItem     `json:"recommendations"`
	Seasons               []SeasonSummary    `json:"seasons"`
	// Overview is the TMDB synopsis. The popup renders it under the keyword
	// chips (Library tracked rows do not carry overview on GET /tracked).
	Overview string `json:"overview"`
	// PosterPath is the TMDB poster path from the same details call Overview
	// comes from. Library tracked rows cache no poster art; the popup uses
	// this instead of a second /poster round-trip when the card passed "".
	PosterPath string `json:"posterPath"`
	// Ratings is the official catalog-score row (IMDb / TMDB / Trakt).
	// Never null — [] when every source is empty or unconfigured.
	Ratings []OfficialRating `json:"ratings"`
}

// --- Request-status worklist (Feature 4, derive-on-read) -------------------
//
// GET /api/requests's response — a cross-mode (NOT mode-scoped) status rollup
// aggregated live on read from the tracked library + grabs, with no new
// persisted table. See internal/api/requests.go for what this adds over the
// existing /grabs (raw per-mode grab log) and /downloads (download-client
// status) screens.

// RequestStatusItem is one title's cross-mode status. Status is "In Library"
// (a tracked item) or "Downloading" (an in-flight grab — "Requested" collapses
// into this in sakms's single-operator model: a grab IS the request, there is
// no separate approval queue). GrabID is set only for a Downloading row.
// MissingCount is an annotation, Series-only: the number of episodes TMDB
// knows about with no file on disk for an otherwise In-Library series (0 for
// Movies/Adult, which don't track not-owned titles).
type RequestStatusItem struct {
	Mode         string `json:"mode"`
	Title        string `json:"title"`
	TMDBID       int    `json:"tmdbId"`
	Status       string `json:"status"`
	GrabID       int64  `json:"grabId"`
	MissingCount int    `json:"missingCount"`
	// RetryAfter/RetryReason are set only when Status is "Pending Retry" —
	// mirroring the grab row's own grabs.Grab.RetryAfter/RetryReason (see
	// Grab above) so the Requests screen can show why a title has no
	// qualifying candidate yet and when the next re-search runs, instead of
	// the bare "Pending Retry" label alone.
	RetryAfter  string `json:"retryAfter,omitempty"`
	RetryReason string `json:"retryReason,omitempty"`
	// HoldUntil mirrors the grab row's grabs.Grab.HoldUntil (see Grab above for
	// why that field exists on two structs). Carried here so the Requests
	// screen can render a held pre-release request's scheduled release date
	// inline, without a second call to fetch the grab it was derived from.
	HoldUntil string `json:"holdUntil,omitempty"`
}

// RequestStatusResponse is GET /api/requests's response — one row per title
// across every mode.
type RequestStatusResponse struct {
	Items []RequestStatusItem `json:"items"`
}

// --- Requests: excluded titles (permanent "remove") ------------------------
//
// A Requests row has no persisted identity (the worklist is derived on read), so
// "remove" records a suppression keyed by mode + identity: TMDBID when present
// (Movies/Series), else the Title (Adult scenes carry no TMDB id). The backend
// derives the exact suppression key from these fields (see internal/excludes.Key)
// — the client sends the same fields it already holds for the row.

// ExcludeTitleRequest is POST /api/requests/exclude's body — permanently remove
// one title from the Requests worklist. TMDBID is preferred when the row has one;
// Title is required for an Adult scene (no TMDB id). At least one of TMDBID/Title
// must be set, alongside Mode ("movies"|"series"|"adult").
type ExcludeTitleRequest struct {
	Mode   string `json:"mode"`
	TMDBID int    `json:"tmdbId,omitempty"`
	Title  string `json:"title,omitempty"`
}

// ExcludeTitlesBatchRequest is POST /api/requests/exclude-batch's body — the
// bulk multi-select "remove selected" form. Each item is an independent
// ExcludeTitleRequest, applied per item with skip-and-continue semantics.
type ExcludeTitlesBatchRequest struct {
	Items []ExcludeTitleRequest `json:"items"`
}

// ExcludeTitleResult is one item's outcome in a bulk exclude. OK true means it
// was recorded (or was already excluded — the operation is idempotent); OK false
// means it was skipped and Error explains why (the batch never aborts on one
// failure). Index is the item's position in the submitted Items slice; Mode/Title
// echo the request for the results UI.
type ExcludeTitleResult struct {
	Index int    `json:"index"`
	Mode  string `json:"mode"`
	Title string `json:"title,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ExcludeTitlesBatchResponse is POST /api/requests/exclude-batch's response —
// always HTTP 200; per-item success/failure lives here in Results, one per
// submitted item in submission order.
type ExcludeTitlesBatchResponse struct {
	Results []ExcludeTitleResult `json:"results"`
}

// --- Review-queue proposals: Rename (Stage 3) -----------------------------
//
// The staged scan→propose→apply review queue backing the Rename workflow (and,
// in later Stage-3 waves, Purge/Dedup/Tag). Proposal below is a CURATED subset
// of internal/proposals.Proposal's wire shape — only the fields the Rename view
// actually reads (see internal/web/static/index.html's renderRename, ported to
// frontend/src/screens/Rename.tsx). It is deliberately NOT a full mirror of the
// domain struct: the Dedup-only Candidates slice belongs to the Purge/Dedup/Tag
// waves and is added here when those land, per Guardrail #4's "DTO set grows
// per stage." Studio/Date/PHash (Adult) and SeasonNumber/EpisodeNumber (Series)
// were added in Rename's per-mode-columns follow-up (Wade-approved, see
// .omc/handoffs/stage-3-rename.md) once the review table started surfacing
// them — before that they were deliberately omitted as unused by the view.
//
// Status mirrors proposals.Status exactly ("pending" | "unmatched" | "applied"
// | "dismissed"); the TS client narrows it to a string-literal union locally
// (frontend/src/api/rename.ts), the same pattern discover.ts uses for Mode.
// Wire keys match proposals.Proposal's json tags so a future handler swap onto
// this type is a substitution, not a wire-format change (see this package's doc).

// Candidate is one file in a Dedup proposal's duplicate group — the shape the
// Dedup view (frontend/src/screens/Dedup.tsx) renders one table row from. A
// CURATED subset of internal/proposals.Candidate: the fields the view
// actually displays (Label/Path/Resolution/Codec/BitRate/Size) plus Winner, the
// keeper-vs-duplicate flag. Winner==true is the "tracked copy" the group keeps;
// every other candidate is a duplicate the Apply removes (see
// internal/dedup.ApplyLibrary*). Size is on-disk bytes; Dedup VMAF scores others
// against the largest Size, which is independent of Winner. Wire order is
// load-bearing: DedupApplyRequest's KeepIndex is an ARRAY INDEX into this exact
// slice (proposals.Proposal.Candidates order), so the client MUST render
// candidates in received order and never sort them, or the index it sends
// resolves to the wrong file. TrackedID is omitted — the view never reads it.
//
// Claude 2026-08-21: PHash is on the wire for pair-level VMAF gating.
// Reason: Dedup groups by phash OR TMDB identity; VMAF must run only when
// THIS candidate matches the VMAF reference on phash, not when the group's
// worst-pair pHashSimilarity clears 0.7. The list handler encodes domain
// proposals.Candidate (which already had phash); this field keeps the DTO
// honest so the client can skip the /vmaf fetch without duplicating Hamming
// against an undocumented extra key.
// Review if: VMAF gating moves to a server-computed boolean per candidate.
type Candidate struct {
	Label      string `json:"label"`
	Path       string `json:"path"`
	Resolution int    `json:"resolution"`
	Codec      string `json:"codec"`
	BitRate    int64  `json:"bitRate"`
	Size       int64  `json:"size"`
	Winner     bool   `json:"winner"`
	PHash      string `json:"phash,omitempty"`
}

// Proposal is one staged review-queue row as the Rename/Purge/Dedup views consume
// it. SourceName/RootFolderPath/Reason are always present; Title/Year are only
// meaningful once Status is pending/applied; Reason explains an unmatched row;
// DraftID is set once a successful submit-draft ("give back") has run, so the
// button renders as already-done and can't re-submit. Studio/Date/PHash are
// Adult-only (captured from Adult identification); SeasonNumber/EpisodeNumber
// are Series-only (a season-pack orphan produces one proposal per episode).
// ExtraEpisodeNumbers is Series-only too, and only non-empty for a logical-
// episode-split file (e.g. "S01E01-E02" produces EpisodeNumber=1,
// ExtraEpisodeNumbers=[2]) — Apply relocates the file once and creates one
// Episode row per number, primary plus each of these, all at that path.
// Candidates is Dedup-only: the duplicate group's files, exactly one flagged
// Winner (the keeper); Rename/Purge never populate it (it's absent from their
// wire rows, so the shared TS type carries it as optional).
type Proposal struct {
	ID                  int64  `json:"id"`
	Status              string `json:"status"`
	SourceName          string `json:"sourceName"`
	SourcePath          string `json:"sourcePath,omitempty"`
	RootFolderPath      string `json:"rootFolderPath"`
	Title               string `json:"title,omitempty"`
	Year                int    `json:"year,omitempty"`
	SeasonNumber        int    `json:"seasonNumber,omitempty"`
	EpisodeNumber       int    `json:"episodeNumber,omitempty"`
	ExtraEpisodeNumbers []int  `json:"extraEpisodeNumbers,omitempty"`
	Studio              string `json:"studio,omitempty"`
	Date                string `json:"date,omitempty"`
	PHash               string `json:"phash,omitempty"`
	// GiveBackBox/GiveBackSceneID mirror the wire fields that proposals.go
	// already carries (proposals.go:167-168). The frontend uses the ABSENCE of
	// GiveBackSceneID (with a non-empty Title) as the structural signal that an
	// Adult Unmatched row is web-identified — enabling the Review action without
	// matching on the reason string (a fragility the old reason-substring checks
	// all had). Added 2026-08-12 for adult-rename-review-alts E2.
	GiveBackBox     string      `json:"giveBackBox,omitempty"`
	GiveBackSceneID string      `json:"giveBackSceneId,omitempty"`
	Reason          string      `json:"reason,omitempty"`
	DraftID         string      `json:"draftId,omitempty"`
	Candidates      []Candidate `json:"candidates,omitempty"`
	// PHashSimilarity is the minimum pairwise phash similarity across the
	// duplicate group [0.0–1.0], populated only by phash-primary scans
	// (Movies/Series). Zero means the proposal was produced by the legacy
	// TMDB-keyed path and no similarity score was computed.
	PHashSimilarity float64 `json:"pHashSimilarity,omitempty"`
	// Genres/Cast are populated for Movies/Series Rename proposals only —
	// empty for Dedup/Purge/Adult. Soft-fail: absent when TMDB credits
	// call failed at Scan time.
	Genres []string `json:"genres,omitempty"`
	Cast   []string `json:"cast,omitempty"`
}

// --- Purge (no dedicated DTOs) ---------------------------------------------
//
// Claude 2026-08-11: the per-mode tag allowlist that used to be documented
// here (AllowlistAddRequest + its bare-[]string list response) is retired —
// see PruningRule below.
// Reason: Purge matches every tracked item against that mode's enabled
// pruning rules; it has no mechanism, and so no DTO, of its own beyond the
// scan/apply routes.
//
// Purge reuses the shared Proposal type above unchanged — its queue rows read
// only Title/Status/RootFolderPath/Reason, all already present. No
// Purge-specific proposal fields exist (no re-pick / give-back / draft), so
// none are added here.

// RepickRequest is the body of POST /api/proposals/{id}/repick — Rename's
// manual-override path when Scan's automatic match was wrong or scored too low
// to auto-accept. Movies/Series require TMDBID and Title from tmdb-search;
// Adult requires Box, SceneID, and Title from scene-search (studio/date echo
// the chosen AdultSceneCandidate). Year is optional on TMDB picks (parsed from
// the result's release date when present). Mirrors internal/api's
// repickProposalRequest.
type RepickRequest struct {
	TMDBID int    `json:"tmdbId,omitempty"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
	// SeasonNumber/EpisodeNumber are OPTIONAL and Series-only: the operator's
	// direct slot assignment for a file with no recoverable season/episode
	// signal (an opaque hash basename, a raw DVD VIDEO_TS authoring name, or an
	// ambiguous multi-slot title match). Both nil means "show-level re-pick
	// only" — the pre-existing behaviour, unchanged.
	//
	// POINTERS, NOT ints, and this is load-bearing: season 0 is Specials, a
	// real value an operator can legitimately mean. Same reasoning as
	// DedupApplyRequest.KeepIndex below and grabs.Grab.SeasonSpecified. A
	// client that omits a literal 0 assigns the wrong slot silently.
	//
	// Validated as a PAIR: supplying one without the other is a 400. Both are
	// rejected outright on a Movies proposal.
	SeasonNumber  *int `json:"seasonNumber,omitempty"`
	EpisodeNumber *int `json:"episodeNumber,omitempty"`

	// --- Adult (stash-box / TPDB) ---
	// Box and SceneID are required when the proposal's mode is adult.
	Box     string `json:"box,omitempty"`
	SceneID string `json:"sceneId,omitempty"`
	Studio  string `json:"studio,omitempty"`
	Date    string `json:"date,omitempty"`
}

// MoveModeRequest is the body of POST /api/proposals/{id}/move-mode — the
// cross-mode reassignment path. Deliberately NOT an extension of
// RepickRequest: repick's handler treats the proposal's own Mode as canonical
// and immutable (internal/api/proposals.go:916-919), and bolting a Mode field
// onto it would silently no-op. See .omc/plans/autopilot-impl.md.
//
// The payload is discriminated by TargetMode. Exactly one catalog group is
// read; fields belonging to the other groups are ignored, and every field
// belonging to the NON-target catalog is CLEARED in the database by the write.
type MoveModeRequest struct {
	// TargetMode is "movies" | "series" | "adult" and must differ from the
	// proposal's current mode (a same-mode move is a 400, not a no-op —
	// silently accepting it would mask a frontend bug).
	TargetMode string `json:"targetMode"`

	// --- Movies / Series (TMDB) ---
	// TMDBID and Title are required when TargetMode is movies or series.
	TMDBID int    `json:"tmdbId,omitempty"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
	// SeasonNumber/EpisodeNumber are OPTIONAL and Series-only, with exactly
	// the semantics RepickRequest documents: POINTERS, not ints, because
	// season 0 is Specials and an int+omitempty cannot distinguish "the
	// operator chose 0" from "the operator left it blank". Both nil is a
	// show-level move. Half a pair is a 400. Non-nil on a non-Series target
	// is a 400.
	SeasonNumber  *int `json:"seasonNumber,omitempty"`
	EpisodeNumber *int `json:"episodeNumber,omitempty"`

	// --- Adult (stash-box / TPDB) ---
	// Box and SceneID are required when TargetMode is "adult" and are the
	// hard prerequisite for Apply: rename.ApplyLibraryAdult refuses any
	// proposal missing either (internal/rename/rename_adult_library.go:195-197).
	// Box is "stashdb" | "fansdb" | "tpdb". Studio and Date feed
	// naming.AdultFileName and should be echoed straight back from the
	// chosen AdultSceneCandidate.
	Box     string `json:"box,omitempty"`
	SceneID string `json:"sceneId,omitempty"`
	Studio  string `json:"studio,omitempty"`
	Date    string `json:"date,omitempty"`
}

// AdultSceneCandidate is one row of GET /api/modes/adult/scene-search — a
// pickable stash-box/TPDB scene for the cross-mode move's Adult target. It is
// deliberately a LIST shape: identify.BoxSearcher's existing SearchStashBox /
// SearchTPDB collapse to a single best *MatchResult, which cannot back a
// "pick one of these" UI.
type AdultSceneCandidate struct {
	Box             string `json:"box"`
	SceneID         string `json:"sceneId"`
	Title           string `json:"title"`
	Studio          string `json:"studio,omitempty"`
	Date            string `json:"date,omitempty"`
	ImageURL        string `json:"imageUrl,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
}

// AdultSceneSearchResponse envelopes the candidate list plus per-box soft
// failures, so one unreachable stash-box degrades the result instead of
// failing the whole search (same tolerance internal/identify's cascade has).
type AdultSceneSearchResponse struct {
	Items  []AdultSceneCandidate `json:"items"`
	Errors []string              `json:"errors,omitempty"`
}

// AdultSceneResolveResponse is GET /api/modes/adult/scene-resolve — one
// catalog match from a pasted URL, or a message when resolution fails softly.
type AdultSceneResolveResponse struct {
	Item    *AdultSceneCandidate `json:"item,omitempty"`
	Message string               `json:"message,omitempty"`
}

// DedupApplyRequest is the OPTIONAL body of POST /api/proposals/{id}/apply when
// the proposal is a Dedup group (Rename/Purge send an empty body and ignore
// these fields — see internal/api's applyProposalRequest, which this mirrors).
// Exactly one of two shapes is sent per Apply:
//
//   - {keepIndex: N} — keep candidate N, delete every other candidate in the
//     group. N is an ARRAY INDEX into Proposal.Candidates in received order
//     (the radio the operator selected; the group's Winner is pre-selected).
//     KeepIndex MUST be sent even when it is 0 — a falsy-guard that drops a
//     literal 0 makes the backend fall back to its own auto-winner and can
//     delete the file the operator actually chose to keep (dedup.ApplyLibrary
//     indexes p.Candidates[keepIndex] directly).
//   - {keepAll: true} — keep every candidate, delete nothing ("Keep All"). The
//     conservative escape hatch when the group isn't really a duplicate.
//
// KeepIndex is a pointer/omitempty so "keep all" omits it entirely rather than
// sending 0 (which would mean "keep candidate 0").
//
// AdditionalKeepIndices is the multi-keep set: besides the single primary
// (KeepIndex), every index listed here is also kept on disk untouched (only the
// primary is tracked). It carries ARRAY INDICES into Proposal.Candidates, same
// as KeepIndex. It MUST be omitted (undefined), never sent as [], when the
// operator kept only one candidate — the empty-set-omission rule that keeps the
// single-keep wire shape unchanged for the existing strict request-shape tests.
// The backend rejects (400) a set that is out of range, that contains KeepIndex,
// that is present with a nil KeepIndex, or that is combined with KeepAll.
type DedupApplyRequest struct {
	KeepIndex             *int  `json:"keepIndex,omitempty"`
	KeepAll               bool  `json:"keepAll,omitempty"`
	AdditionalKeepIndices []int `json:"additionalKeepIndices,omitempty"`
}

// --- Bulk apply: same-screen multi-select of Pending proposals -------------
//
// POST /api/proposals/apply-batch applies several already-reviewed Pending
// proposals from ONE screen (single workflow+mode) in a single call, applied
// sequentially with skip-and-continue per-item results. It is the bounded,
// opt-in exception to the "one item at a time" apply principle (see
// CLAUDE.md / docs/ARCHITECTURE.md), NOT a global "apply everything" bypass.
// Mirrors internal/api's applyBatchItem/applyBatchRequest.

// ApplyBatchItem is one selected proposal plus its optional Dedup override.
// KeepIndex/KeepAll carry the same three-state Dedup semantics as
// DedupApplyRequest (a Dedup group's radio the operator changed before adding
// it to the batch); Rename and Purge items omit both. KeepIndex MUST be sent
// even when it is 0 — see DedupApplyRequest's doc comment for why a dropped
// literal 0 can delete the wrong file. AdditionalKeepIndices carries the same
// multi-keep set as DedupApplyRequest and MUST be threaded through the batch too
// (omitted, never [], when empty) — dropping it on the bulk path would delete
// files the operator checked as "keep".
type ApplyBatchItem struct {
	ID                    int64  `json:"id"`
	SourcePath            string `json:"sourcePath,omitempty"`
	KeepIndex             *int   `json:"keepIndex,omitempty"`
	KeepAll               bool   `json:"keepAll,omitempty"`
	AdditionalKeepIndices []int  `json:"additionalKeepIndices,omitempty"`
}

// ApplyBatchRequest is POST /api/proposals/apply-batch's body. Rename batches
// are unbounded; Dedup/Purge are capped at the Organize page-size max (200).
// An empty Items is rejected. Send Accept: application/x-ndjson for live progress.
type ApplyBatchRequest struct {
	Items []ApplyBatchItem `json:"items"`
}

// ProposalPage is a paginated Organize proposals list response.
type ProposalPage struct {
	Items  []Proposal `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

// PendingIDsResponse is Rename "Select all matching" — all Pending ids for a mode.
type PendingIDsResponse struct {
	IDs []int64 `json:"ids"`
}

// AdultReviewPreview is the response body of
// GET /api/modes/{mode}/rename/proposals/{id}/review — the preview shown in the
// Review modal before the operator commits. Added 2026-08-12 for
// adult-rename-review-alts E2.
type AdultReviewPreview struct {
	// ProposedName is the canonical AdultFileName computed from the best
	// available identity (catalog values when present, web-identified values
	// otherwise). It seeds the editable input in the modal.
	ProposedName string `json:"proposedName"`
	Studio       string `json:"studio,omitempty"`
	Title        string `json:"title,omitempty"`
	Date         string `json:"date,omitempty"`
	// PHash is the file's computed perceptual hash. Absent when hashing failed;
	// the modal warns the operator that Confirm will fail without it.
	PHash string `json:"phash,omitempty"`
	// CatalogBox/CatalogSceneID are non-empty only when a fresh DB recheck
	// found a catalog match. When both are present the modal must tell the
	// operator that the edited FileName is ignored and the catalog identity is
	// used instead (spec requirement: no silent mis-leading).
	CatalogBox     string `json:"catalogBox,omitempty"`
	CatalogSceneID string `json:"catalogSceneId,omitempty"`
	CatalogTitle   string `json:"catalogTitle,omitempty"`
	CatalogStudio  string `json:"catalogStudio,omitempty"`
	CatalogDate    string `json:"catalogDate,omitempty"`
	// RecheckError is a soft informational message — not a blocker. Shown muted
	// in the modal; Confirm is still available (unless PHash is absent).
	RecheckError string `json:"recheckError,omitempty"`
}

// AdultReviewConfirmRequest is the body of
// POST /api/modes/{mode}/rename/proposals/{id}/review-confirm. Added 2026-08-12
// for adult-rename-review-alts E2.
//
// Branching: when both Box and SceneID are non-empty, the catalog branch runs
// (RepickAdultScene → ApplyLibraryAdult). Otherwise the local branch runs
// (ConfirmAdultReviewLocal with FileName). FileName is ignored on the catalog
// branch; the modal must make this clear to the operator.
type AdultReviewConfirmRequest struct {
	FileName string `json:"fileName"`          // required on the local branch
	Box      string `json:"box,omitempty"`     // both present → catalog branch
	SceneID  string `json:"sceneId,omitempty"` // both present → catalog branch
	Title    string `json:"title,omitempty"`
	Studio   string `json:"studio,omitempty"`
	Date     string `json:"date,omitempty"`
}

// OrganizeEvent is one activity-log row for Rename/Dedup/Purge screens.
type OrganizeEvent struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Mode       string `json:"mode"`
	Kind       string `json:"kind"`
	ProposalID int64  `json:"proposalId,omitempty"`
	OK         *bool  `json:"ok,omitempty"`
	Message    string `json:"message"`
	DetailJSON string `json:"detailJson,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

// ApplyBatchResultItem is one item's outcome — every requested id gets exactly
// one, in request order, whether it applied or was skipped. OK true means the
// proposal was applied and Proposal holds its refreshed (now-applied) row; OK
// false means it was skipped and Error explains why (the batch never aborts on
// a single failure). Proposal is the curated review-queue shape (same subset
// the Rename/Purge/Dedup views already consume), not the full domain struct.
type ApplyBatchResultItem struct {
	ID       int64     `json:"id"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
}

// ApplyBatchResponse is POST /api/proposals/apply-batch's response — always
// HTTP 200; per-item success/failure lives here in Results, not in the status
// code.
type ApplyBatchResponse struct {
	Results []ApplyBatchResultItem `json:"results"`
}

// --- Rename Delete action -------------------------------------------------
//
// DeleteBatch is Rename's Delete action: POST /api/proposals/delete-batch
// permanently removes each proposal's source file from disk AND deletes the
// proposal row entirely (NOT a Dismiss — the operator chose to leave no
// trace). Pending/Unmatched Rename proposals only, enforced server-side.
//
// It is a SEPARATE endpoint from apply-batch on purpose. Apply-vs-dismiss is
// already partitioned client-side by endpoint (Rename.tsx's confirmApplyAll
// splits one apply-batch call from N per-id dismiss calls); delete is the
// third partition, and gets a BATCH endpoint rather than a per-item one
// because unlike dismiss it commits real PathChanges that must reach
// NotifyPlayers as one grouped call per mode. See
// .omc/plans/autopilot-impl.md §1.
type DeleteBatchItem struct {
	ID         int64  `json:"id"`
	SourcePath string `json:"sourcePath,omitempty"`
}

type DeleteBatchRequest struct {
	Items []DeleteBatchItem `json:"items"`
}

// DeleteBatchResultItem carries NO proposal field, unlike
// ApplyBatchResultItem. A deleted item has no row to refresh — that absence
// IS the success condition. The existing dismiss path already returns this
// exact {id, ok, error} shape client-side and BatchResultSummary already
// consumes it, so nothing downstream needs widening.
type DeleteBatchResultItem struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type DeleteBatchResponse struct {
	Results []DeleteBatchResultItem `json:"results"`
}

// --- Rename Undo: the "Recently Applied" list ------------------------------
//
// RecentlyAppliedEntry is one row of GET /api/modes/{mode}/rename/recently-applied
// — the bounded, undo-eligible-only list the Rename screen's "Recently Applied"
// section renders (deep-interview-rename-undo). It is NOT the general history
// view (`?view=history` on the proposals list, which shows every
// Applied/Dismissed proposal): this list IS the full undoable set by
// construction, capped at the mode's configured undoDepth.
//
// It is a LEAN PROJECTION of internal/rename's UndoEntry, deliberately: that
// struct carries PreApplyProposalSnapshot and TouchedRowsJSON, two raw internal
// JSON blobs the UI has no use for and must never be handed. SourceName/Title
// are extracted from the proposal snapshot server-side rather than re-read from
// the live proposal row — the archive's whole point is that it records the
// pre-Apply state, and a second DB read would also re-introduce a dependency on
// a row that may since have been retired.
//
// ViaAlternateFold true means undoing this entry will NOT move any file back
// (the Apply took Movies'/Series' promote-demote-by-tier branch, whose file the
// undo must not touch — it belongs to a different, still-valid proposal). The
// UI has to surface that up front; an operator who does not know it will read
// the resulting "file not restored" as a failure.
//
// DEVIATION from the implementation plan's §1, recorded rather than left
// implicit: the plan spelled this as a local unexported `recentlyAppliedEntry`
// struct inside internal/api/rename_undo.go. It lives here instead so the
// frontend type is GENERATED (internal/apidto/gen's TestNoDrift then fails the
// Go suite if the two ever disagree) rather than hand-declared and free to
// drift. Mode is a plain string, not internal/mode.Mode: this package
// deliberately imports no internal domain packages, and the frontend narrows it
// to its own Mode union at the call boundary.
type RecentlyAppliedEntry struct {
	UndoID           int64  `json:"undoId"`
	ProposalID       int64  `json:"proposalId"`
	Mode             string `json:"mode"`
	AppliedAt        string `json:"appliedAt"`
	SourceName       string `json:"sourceName"`
	Title            string `json:"title,omitempty"`
	ViaAlternateFold bool   `json:"viaAlternateFold"`
}

// --- Tag workflow: vocabulary + tracked-item picker ------------------------
//
// The Tag workflow is direct CRUD on a tracked item's tags — no staged
// scan→propose→apply queue like Rename/Purge/Dedup. Two GETs back the view (a
// tag vocabulary for autocomplete + the tracked items each carrying their
// current tags), and add/remove act immediately on one item.
//
// CRITICAL per-mode routing (see internal/api/tag.go and the frontend's
// src/api/tag.ts): Movies/Series use the GENERIC item-tag routes
// (GET /api/modes/{mode}/tags, POST/DELETE /api/modes/{mode}/items/{itemId}/tags[/{tagId}]),
// while Adult uses its OWN DEDICATED scene-tag routes
// (GET /api/modes/adult/scenes/tags, GET/POST/DELETE /api/modes/adult/scenes/{sceneId}/tags[/{tagId}])
// — the generic routes 400 for Adult (Whisparr eliminated; Adult tags are
// scene-level). The wire SHAPES below are identical across modes; only the URLs
// the client builds differ.

// TagEntry is one entry in a mode's tag vocabulary — mirrors internal/api's
// libraryTagEntry. A local tag has no numeric id, so ID and Label are the same
// string value; ID exists only to keep the {id, label} shape the frontend's
// datalist/lookup logic expects. Returned by both the Movies/Series generic
// vocab route and Adult's dedicated scene-tag vocab route.
type TagEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// TrackedItem is one row in the Tag workflow's item picker — mirrors
// internal/api's libraryTrackedItem, served from GET /api/modes/{mode}/tracked
// for EVERY mode (items for Movies, series for Series, scenes for Adult). ID is
// the library row id (a library_scenes.id for Adult, which is exactly the
// {sceneId} the scene-tag routes take). Tags is the item's current tag labels
// (a local tag has no numeric id — it's the label string itself, matching
// TagEntry.ID).
//
// TmdbId/Year are additive, present only for Movies/Series (both carry a TMDB
// identity in the library); they are absent for Adult scenes, which are keyed
// on (box, sceneId) with no TMDB id. Discover's existing-library row uses
// TmdbId to lazily fetch each card's poster + availability and to drive
// auto-grab; Year is display-only. The Tag picker (this type's original
// caller) ignores both.
//
// CreatedAt is additive for every mode so the Library screen can sort by
// added date. Adult scenes used to omit it (there was no Adult Library grid);
// that grid now exists, and the Adult tracked handler always sends it.
//
// Box/SceneID/Studio/Date are Adult-only stored catalog identity, copied from
// library_scenes. They let Library cards open Discover's DetailPopup
// (allowGrab=false) and show a studio/date hover overlay without a live
// catalog lookup on GET /tracked. Box is AdultDiscoverItem.source; SceneID is
// the catalog UUID, not library_scenes.id (that stays on ID). Empty for
// Movies/Series.
type TrackedItem struct {
	ID             int64             `json:"id"`
	Title          string            `json:"title"`
	Tags           []string          `json:"tags"`
	TmdbId         int               `json:"tmdbId,omitempty"`
	Year           int               `json:"year,omitempty"`
	CollectionName string            `json:"collectionName,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	Cast           []string          `json:"cast,omitempty"`
	CreatedAt      string            `json:"createdAt,omitempty"`
	QualityTiers   []string          `json:"qualityTiers,omitempty"`
	Files          []TrackedItemFile `json:"files,omitempty"`
	VideoURL       string            `json:"videoUrl,omitempty"`
	PosterURL      string            `json:"posterUrl,omitempty"`
	Box            string            `json:"box,omitempty"`
	SceneID        string            `json:"sceneId,omitempty"`
	Studio         string            `json:"studio,omitempty"`
	Date           string            `json:"date,omitempty"`
	// Claude 2026-08-14: operator 1–5 star rating from the library row.
	// 0/omitted = unrated. GET /tracked must not copy catalog TMDB/TPDB scores.
	// Review if: Discover's existing-library row starts showing stars.
	Rating int `json:"rating,omitempty"`
}

// LibraryRatingRequest is PUT /api/modes/{mode}/items/{itemId}/rating
// (Movies/Series) and PUT /api/modes/adult/scenes/{sceneId}/rating (Adult).
// 0 clears the operator score; 1–5 sets it. Catalog TMDB/TPDB ratings are
// not accepted here.
type LibraryRatingRequest struct {
	Rating int `json:"rating"`
}

// TrackedItemFile is one primary or alternate video under a Movies tracked
// title (GET /api/modes/movies/tracked). Series/Adult leave Files empty.
type TrackedItemFile struct {
	ID          int64   `json:"id"`
	FilePath    string  `json:"filePath"`
	IsPrimary   bool    `json:"isPrimary"`
	QualityTier string  `json:"qualityTier,omitempty"`
	Size        int64   `json:"size,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	VideoCodec  string  `json:"videoCodec,omitempty"`
	BitRate     int64   `json:"bitrate,omitempty"`
	DurationSec float64 `json:"durationSec,omitempty"`
	// VideoURL is set only when the file's container is one a <video>
	// element can decode (mp4/m4v/webm/mov). mkv/avi/wmv omit it so the
	// Library player does not mount a broken element. GET the URL for
	// bytes; optional fileId is already in the path's query when present.
	VideoURL string `json:"videoUrl,omitempty"`
}

// CollectionSummary is one entry from GET /api/modes/movies/collections —
// a TMDB franchise collection with the count of tracked movies belonging to it.
type CollectionSummary struct {
	TMDBCollectionID int    `json:"tmdbCollectionId"`
	Name             string `json:"name"`
	Count            int    `json:"count"`
}

// StorageAllocationCell is one (mode, tier) intersection of the Dashboard's
// Storage Allocation grid. Cells with nothing behind them are still sent, as
// zeroes, so the frontend never has to invent a missing cell.
type StorageAllocationCell struct {
	Tier       string `json:"tier"` // low|medium|high|lossless|unknown
	TotalBytes int64  `json:"totalBytes"`
	ItemCount  int    `json:"itemCount"`
}

// StorageAllocationRow is one mode's full tier axis. Cells is ALWAYS
// len(Tiers) == 5, in the fixed StorageAllocation.Tiers order.
//
// RowItemCount is the sum of the row's cell counts. For Series it can exceed
// the number of distinct series: a series whose episodes span two tiers is
// counted in each of those two cells, which is what makes each cell's count
// reconcile with the drill-down it links to. RowTotalBytes never
// double-counts — a file belongs to exactly one tier group.
type StorageAllocationRow struct {
	Mode          string                  `json:"mode"` // movies|series|adult
	Cells         []StorageAllocationCell `json:"cells"`
	RowTotalBytes int64                   `json:"rowTotalBytes"`
	RowItemCount  int                     `json:"rowItemCount"`
}

// StorageAllocation is GET /api/admin/storage-allocation's response: a dense
// mode x tier grid of the tracked library's byte totals and item counts. Rows
// is always len 3 (movies, series, adult, in that order) and Tiers is always
// the fixed 5-tier axis, so the table's shape is a constant.
//
// Every number here comes from one grouped SQL query over columns captured at
// write time — the endpoint does NO disk I/O and makes NO external calls.
type StorageAllocation struct {
	Rows            []StorageAllocationRow `json:"rows"`
	Tiers           []string               `json:"tiers"`
	GrandTotalBytes int64                  `json:"grandTotalBytes"`
}

// --- Stage 4: Settings + Advanced Settings ---------------------------------
//
// The DTOs backing the ported Settings view (Connections, API Access, Auth
// mode, AI provider/model, per-mode library/quality/naming/kids, plus the new
// Advanced Settings section: phash-threshold, rename-match-config,
// identify-enabled, recheck-interval). Each mirrors the exact wire shape of the
// matching handler in internal/api (settings.go, library.go, recheck.go,
// rename.go, connections.go, netscan.go) so a future handler swap onto these
// types is a substitution, not a wire-format change (see this package's doc /
// Guardrail #4's "the DTO set grows per stage").
//
// AuthModeResponse/Request, OIDCStatusResponse/OIDCConfigRequest,
// APIKeyStatusResponse/APIKeyRegenerateResponse, ConnectionSummary/
// ConnectionUpsertRequest, and DismissSetupRequest already exist above (auth
// boot + the three-state secret reference) and are reused by Settings as-is.

// ConnectionTestRequest is POST /api/connections/test's body — enough to
// construct a client and make one real, read-only call (Settings' "Test"
// button). Nothing is persisted, so APIKey here is a PLAIN string (not the
// three-state *string of ConnectionUpsertRequest): a test always sends exactly
// what's currently typed. Mirrors internal/api.ConnectionTestRequest.
type ConnectionTestRequest struct {
	Service  string `json:"service"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
}

// ConnectionTestResult is POST /api/connections/test's response. A false OK
// with a populated Error is the normal "wrong URL / wrong key" shape, not a
// server-side failure. Mirrors internal/api.ConnectionTestResult.
type ConnectionTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AIProviderResponse / AIProviderRequest back GET/PUT /api/settings/ai-provider
// — which AI backend every AI-assisted feature uses. Provider is one of
// "ollama", "openai", "gemini", "anthropic".
type AIProviderResponse struct {
	Provider string `json:"provider"`
}

type AIProviderRequest struct {
	Provider string `json:"provider"`
}

// AIModelResponse / AIModelRequest back GET/PUT /api/settings/ai-model — the
// model name the configured provider should use (empty string = unset).
type AIModelResponse struct {
	Model string `json:"model"`
}

type AIModelRequest struct {
	Model string `json:"model"`
}

// QualityPrefsResponse / QualityPrefsRequest back
// GET/PUT /api/modes/{mode}/quality-prefs — Movies, Series, and Adult (the
// Discover detail popup's availability grid applies to all three, so all
// three get a configurable default; this used to say "Movies/Series only,
// Adult has no Search workflow," which stopped being true once Adult grew
// its own availability-popup search path). Tier is one of "low", "medium",
// "high", "lossless"; MaxResolution is one of 480/720/1080/2160, or 0 for
// "no cap" (a SOFT cap — see internal/quality's own package doc: it never
// excludes a result outside the cap, only prefers at-or-below-cap when
// choosing). Protocol is "usenet", "torrent", or "" for no preference.
//
// UndoDepth is Rename Undo's per-mode rolling depth: how many of this mode's
// most recent Applies stay undoable (deep-interview-rename-undo). It rides on
// this existing per-mode request rather than a new endpoint, so the Settings
// screen gains one field instead of a second round trip. 1..100; the response
// substitutes the default (10) when nothing is stored, and a request carrying 0
// — an older client that predates the field — stores the default rather than
// failing the whole quality save.
//
// Tiers is the accepted quality set (any non-empty subset of low/medium/high/
// lossless). Auto-grab tries them highest-first. A request that omits Tiers
// treats Tier as a floor and expands (high → high+lossless). GET always
// returns the effective set; Tier on the response is the lowest accepted
// value so older clients still see a single floor.
type QualityPrefsResponse struct {
	Tier          string   `json:"tier"`
	Tiers         []string `json:"tiers"`
	MaxResolution int      `json:"maxResolution"`
	Protocol      string   `json:"protocol"`
	UndoDepth     int      `json:"undoDepth"`
}

// Claude 2026-08-10: added UndoDepth to both quality-prefs DTOs.
// Reason: deep-interview-rename-undo — Rename Undo's per-mode rolling depth
//   rides this existing per-mode request instead of a new endpoint. The REQUEST
//   field is a pointer and the RESPONSE field is a plain int; that asymmetry is
//   load-bearing, not an oversight — see the note directly below.
// Troubleshooting: `pnpm typecheck` failed in Library.tsx's putQualityPrefs
//   call, taking `vite build` and therefore `go build ./cmd/sakms` (which
//   embeds the built assets) with it — the request field was a plain int, which
//   generates a REQUIRED TS property that no existing caller sends.
// Review if: Pass 2's Settings control ships and every caller sends the field —
//   the pointer is STILL required then, for the nil-means-leave-alone semantic.
// Related files: internal/api/library.go, internal/apidto/ts/dto.gen.ts

// UndoDepth is a POINTER here and a plain int on the response, deliberately.
// The generated TS makes a non-pointer field REQUIRED, and the existing
// putQualityPrefs caller does not send this one yet (Pass 2 adds the control),
// so a plain int fails `tsc --noEmit` and takes the whole frontend build — and
// therefore `go build ./cmd/sakms`, which embeds it — down with it. The pointer
// also carries the three-state meaning the save path needs: nil = "not sent,
// leave the stored value alone", which is NOT the same as an explicit value and
// must not be collapsed into a default (see putQualityPrefsHandler).
type QualityPrefsRequest struct {
	Tier          string   `json:"tier"`
	Tiers         []string `json:"tiers,omitempty"`
	MaxResolution int      `json:"maxResolution"`
	Protocol      string   `json:"protocol"`
	UndoDepth     *int     `json:"undoDepth,omitempty"`
}

// NamingPresetResponse / NamingPresetRequest back
// GET/PUT /api/modes/{mode}/naming-preset (Movies/Series only). Preset is one
// of "jellyfin" (default) or "legacy".
type NamingPresetResponse struct {
	Preset string `json:"preset"`
}

type NamingPresetRequest struct {
	Preset string `json:"preset"`
}

// LibraryRootFolderResponse / LibraryRootFolderRequest back
// GET/PUT /api/modes/{mode}/library/root-folder — the free-typed root folder
// SAK scans/imports into for a mode. The Settings UI exposes this for
// Movies/Series only (matching the old renderLibrarySettings), even though the
// backend key now exists for Adult too.
type LibraryRootFolderResponse struct {
	Path string `json:"path"`
}

type LibraryRootFolderRequest struct {
	Path string `json:"path"`
}

// KidsRootPathResponse / KidsRootPathRequest back
// GET/PUT /api/modes/{mode}/rename/kids-root-path (Movies/Series only — the
// endpoint 400s for other modes). Empty Path turns Kids classification off.
type KidsRootPathResponse struct {
	Path string `json:"path"`
}

type KidsRootPathRequest struct {
	Path string `json:"path"`
}

// PHashThresholdResponse / PHashThresholdRequest back
// GET/PUT /api/modes/{mode}/phash-threshold — the Dedup perceptual-hash
// similarity cut (per-frame average Hamming bits). Valid range 0 to the active
// algorithm's per-frame bit width (0–256 for PDQ); the frontend mirrors that
// bound before submitting (backend re-validates).
type PHashThresholdResponse struct {
	Threshold int `json:"threshold"`
}

type PHashThresholdRequest struct {
	Threshold int `json:"threshold"`
}

// MatchConfigResponse / MatchConfigRequest back
// GET/PUT /api/modes/{mode}/rename-match-config — Rename drilldown candidate
// walk size and duration tolerance percentage.
type MatchConfigResponse struct {
	CandidateN           int `json:"candidateN"`
	DurationTolerancePct int `json:"durationTolerancePct"`
}

type MatchConfigRequest struct {
	CandidateN           int `json:"candidateN"`
	DurationTolerancePct int `json:"durationTolerancePct"`
}

// IdentifyEnabledResponse / IdentifyEnabledRequest back
// GET/PUT /api/modes/{mode}/identify-enabled — Adult's phash-first
// identification toggle (default true). ADULT-ONLY: the endpoint 400s for any
// other mode, so the Settings UI only renders this control in the Adult
// context.
type IdentifyEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type IdentifyEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// RecheckIntervalResponse / RecheckIntervalRequest back
// GET/PUT /api/settings/recheck-interval — the background recheck cadence in
// whole seconds. GLOBAL (not per-mode). 0 = off (the opt-in default); a
// negative value is rejected, so the frontend mirrors that >= 0 bound.
type RecheckIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type RecheckIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// NetscanFinding is one entry from the LAN-discovery probe endpoints
// (GET /api/netscan/known, POST /api/netscan/host) — an unauthenticated,
// spoofable HINT to verify, never a confirmed fact. Mirrors
// internal/netscan.Finding. Service is one of "prowlarr" | "qbittorrent" |
// "nzbget" | "jellyfin" | "stash" | "ntfy" | "gotify" | "node-red".
type NetscanFinding struct {
	Service string `json:"service"`
	URL     string `json:"url"`
	Label   string `json:"label"`
}

// NetscanHostRequest is POST /api/netscan/host's body — probe one
// operator-supplied host/LAN IP across the known services' default ports (the
// server refuses any non-private host).
type NetscanHostRequest struct {
	Host string `json:"host"`
}

// NetscanProwlarrKeyRequest / NetscanProwlarrKeyResponse back
// POST /api/netscan/prowlarr-key — the one explicit action that reads a
// Prowlarr instance's live API key from its unauthenticated /initialize.json.
// A fetched key must be treated as touched by the connection form (see
// src/api/settings.ts), or the three-state upsert would drop it as "untouched".
type NetscanProwlarrKeyRequest struct {
	URL string `json:"url"`
}

type NetscanProwlarrKeyResponse struct {
	APIKey string `json:"apiKey"`
}

// --- Discover: TMDB categories + custom sliders (mainstream-discover-seerr) -
//
// Seerr-inspired Discover category rows layered on top of the existing
// trending/popular DiscoverItem rows: fixed built-in categories (Upcoming,
// browse-by-genre/studio/network) plus a fully admin-defined custom-slider
// system (Seerr's CreateSlider/DiscoverSliderEdit equivalent). Item rows for
// every one of these categories (including a resolved slider) reuse
// DiscoverItem unchanged above — Upcoming/genre/studio/network/keyword
// results are still just TMDB movie/TV titles, wire-identical to
// trending/popular, so no new item type is introduced here.

// Genre is one TMDB genre — mirrors tmdb.Genre's wire shape. Backs GET
// /api/modes/{mode}/discover/genres (a movie or TV genre list depending on
// {mode}'s media type) and is the reference list a "genre" slider's
// FilterValue picks from.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Studio is a well-known movie production company — mirrors tmdb.Studio.
// Backs GET /api/discover/studios, the fixed seed-list reference a "browse
// by studio" row / "studio" slider's FilterValue picks from. Movies-only —
// TMDB companies are a movie-catalog concept with no TV equivalent (see
// Network below for TV's parallel concept).
type Studio struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Network is a well-known TV network/streaming service — mirrors
// tmdb.Network. Backs GET /api/discover/networks, Studio's direct sibling
// for the TV catalog (TV-only, symmetric restriction to Studio's
// movies-only one).
type Network struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Keyword is one TMDB keyword search result — mirrors tmdb.Keyword. Backs
// GET /api/discover/keywords?q=, the free-text lookup an admin slider editor
// uses to resolve typed text (e.g. "heist") into the numeric TMDB keyword id
// a "keyword" filter_type slider actually stores as FilterValue — unlike
// Genre/Studio/Network, TMDB has no fixed enumerable keyword list to serve
// as a seed list.
type Keyword struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Slider is one admin-defined custom Discover row — mirrors
// discoversliders.Slider's wire shape. FilterType is one of "genre" |
// "keyword" | "studio" | "network" | "upcoming" | "trending" | "popular";
// Target restricts results to "movie" | "tv" | "mixed". FilterValue is a
// stringified TMDB id (genre/studio/network/keyword) and is empty for the
// three fixed feeds (upcoming/trending/popular) — see
// discoversliders.Store's validate for the enforced pairing rule.
type Slider struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	FilterType  string `json:"filterType"`
	FilterValue string `json:"filterValue,omitempty"`
	Target      string `json:"target"`
	SortOrder   int    `json:"sortOrder"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// SliderUpsertRequest is the body of POST /api/discover/sliders (create) and
// PUT /api/discover/sliders/{id} (update) — every editable field, mirroring
// discoversliders.Store.Create/Update's parameters exactly. Nothing in a
// slider is a secret, so unlike ConnectionUpsertRequest.APIKey every field
// here is a plain (non-pointer) required value — there is no "preserve
// unchanged" partial-update mode; a save always sends the full slider.
type SliderUpsertRequest struct {
	Title       string `json:"title"`
	FilterType  string `json:"filterType"`
	FilterValue string `json:"filterValue,omitempty"`
	Target      string `json:"target"`
	Enabled     bool   `json:"enabled"`
}

// SliderReorderRequest is POST /api/discover/sliders/reorder's body — ids in
// display order, covering every existing slider exactly once. One explicit
// "here is the full new order" action, not a per-item bulk mutation (see
// discoversliders.Store.Reorder's doc comment for why).
type SliderReorderRequest struct {
	IDs []int `json:"ids"`
}

// --- Adult Discover "newest" rows (internal/adultnewest) — Prowlarr-backed,
// not TMDB-backed like Slider above. RowType is "movie" | "scene" |
// "performer" | "studio"; GenreFilter is always optional (every row type can
// be freely narrowed by genre or left unfiltered — unlike Slider's
// FilterValue there is no required/forbidden pairing rule). See
// adultnewest.Row and internal/api/adult_newest_rows.go.
type AdultNewestRow struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	RowType     string `json:"rowType"`
	GenreFilter string `json:"genreFilter,omitempty"`
	SortOrder   int    `json:"sortOrder"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// AdultNewestRowUpsertRequest is the body of POST /api/modes/adult/newest-rows
// (create) and PUT /api/modes/adult/newest-rows/{id} (update) — mirrors
// SliderUpsertRequest's convention: every editable field, no secrets, no
// partial-update mode.
type AdultNewestRowUpsertRequest struct {
	Title       string `json:"title"`
	RowType     string `json:"rowType"`
	GenreFilter string `json:"genreFilter,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// AdultNewestRowReorderRequest is POST /api/modes/adult/newest-rows/reorder's
// body — mirrors SliderReorderRequest exactly.
type AdultNewestRowReorderRequest struct {
	IDs []int `json:"ids"`
}

// AdultNewestReleaseItem is one entry in a resolved adult newest row — the
// enriched result of matching a Prowlarr release to a TPDB/StashDB/FansDB
// entity (internal/adultnewest's background scan job). Deliberately the same
// shape as AdultDiscoverItem's display-relevant fields (Title/Studio/Date/
// Image/Source) so AdultCard/EntityCard/DetailPopup need no changes to
// render it — see this feature's plan, Stage 3.
type AdultNewestReleaseItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Studio  string `json:"studio"`
	Date    string `json:"date"`
	Image   string `json:"image"`
	Source  string `json:"source"`
	RowType string `json:"rowType"`
	// DurationSeconds is the matched entity's runtime, 0 if unknown — see
	// adultnewest.MatchedRelease.EntityDurationSeconds's doc comment. Added
	// specifically so the frontend can build a real grab request instead of
	// hardcoding 0 (a live bug: Adult's auto-grab scorer never re-fetches a
	// real runtime, so a 0 here silently fails to auto-qualify anything).
	DurationSeconds int `json:"durationSeconds"`
	// ReleaseTitle is the raw Prowlarr release title that first matched this
	// entity — see adultnewest.MatchedRelease.FirstSeenReleaseTitle's doc
	// comment. Used as the Grab-time Prowlarr search query in place of
	// reconstructing one from Title/Studio, which included tokens (e.g.
	// TPDB's "S6:E10" episode notation) real indexer filenames never
	// contain. "" for Studio/Performer rows and for entities matched before
	// this field existed.
	ReleaseTitle string   `json:"releaseTitle,omitempty"`
	Genres       []string `json:"genres,omitempty"`
	Performers   []string `json:"performers,omitempty"`
	// Gender is only ever meaningful for a RowPerformer item ("female"/"male"/
	// "" — see adultnewest.MatchedRelease.Gender's doc comment); always "" for
	// Scene/Movie/Studio rows. Not itself rendered on the card face — it backs
	// the Adult Discover dynamic gender-split (Option 5A), which reads it
	// indirectly via the ?gender= filter on the resolve endpoint rather than
	// from this field client-side.
	Gender string `json:"gender,omitempty"`
	// DownloadURL/Protocol/SizeBytes are the feed enclosure for a feed-sourced
	// pooled entity — populated ONLY when the item's feed is currently fresh (via
	// FeedHealth.DirectGrabURL). Empty for a browse-only entity or a feed not
	// currently fresh, in which case the card grabs via the Prowlarr path (D4/D5).
	DownloadURL string `json:"downloadUrl,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
}

// --- Trakt (mainstream-discover-seerr): watchlist connection + OAuth device flow -
//
// Mirrors internal/api/trakt.go's local request/response structs field-for-
// field (that file is deliberately self-contained and doesn't import this
// package — see its own doc comment); these DTOs exist purely for the
// TypeScript codegen boundary. Route table:
//   GET  /api/trakt/status          -> TraktStatusResponse
//   PUT  /api/trakt/credentials     -> TraktCredentialsRequest
//   POST /api/trakt/device/start    -> TraktDeviceStartResponse
//   POST /api/trakt/device/poll     -> TraktDevicePollResponse
//   POST /api/trakt/disconnect      -> (204, no body)
//   GET  /api/trakt/watchlist       -> []TraktWatchlistItem

// TraktStatusResponse is GET /api/trakt/status's response — the general
// "is Trakt usable right now" summary, consumed by both Settings (to render
// configured/linked state and pre-fill the client_id field via ClientID)
// and the Discover watchlist row. An unconfigured connection returns the
// zero value (Configured: false), not an error. ClientID is not secret
// (Trakt sends it as a plain header on every request, same as
// ConnectionSummary.URL's pre-fill convention) — never the client_secret or
// tokens themselves.
type TraktStatusResponse struct {
	Configured     bool   `json:"configured"`
	Linked         bool   `json:"linked"`
	ClientID       string `json:"clientId,omitempty"`
	TokenExpiresAt string `json:"tokenExpiresAt,omitempty"`
}

// TraktCredentialsRequest is PUT /api/trakt/credentials's body — the
// operator-entered Trakt application. ClientSecret follows the same
// three-state rule as ConnectionUpsertRequest.APIKey (nil = preserve
// stored secret, "" = clear, non-empty = set) — see that field's doc
// comment for the full rule; a naive `clientSecret?: string` would
// silently wipe the stored secret on an untouched save here too.
type TraktCredentialsRequest struct {
	ClientID     string  `json:"clientId"`
	ClientSecret *string `json:"clientSecret,omitempty"`
}

// TraktDeviceStartResponse is POST /api/trakt/device/start's response —
// everything the frontend needs to show the operator (a code to enter and
// a URL to visit) and to know how often to call POST /api/trakt/device/poll.
// The device_code itself (the secret the server polls with) is deliberately
// not included; polling is server-side.
type TraktDeviceStartResponse struct {
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

// TraktDevicePollResponse is POST /api/trakt/device/poll's response — one
// non-blocking poll attempt against the in-progress device authorization
// started by TraktDeviceStartResponse. Deliberately a separate endpoint
// from TraktStatusResponse above: this one drives the Connect-flow UI's
// polling loop, the other answers "is Trakt usable right now" everywhere
// else. Linked true means tokens were saved and the flow is done; Pending
// true means keep polling; both false (a denied or expired device code)
// means the flow is over without success — the frontend's own
// client-side deadline (from TraktDeviceStartResponse.ExpiresIn) and the
// 409 a subsequent poll gets (the server clears the flow on any terminal
// outcome) are what surface that to the operator, since there's no
// separate "denied"/"expired" field on the wire.
type TraktDevicePollResponse struct {
	Linked  bool `json:"linked"`
	Pending bool `json:"pending"`
}

// TraktWatchlistItem is one entry of GET /api/trakt/watchlist's response —
// a near-direct mirror of internal/trakt.WatchlistItem's fields. Note this
// is deliberately NOT DiscoverItem's shape: Trakt's watchlist API provides
// no poster/overview/rating at all, so there is nothing to mirror there;
// any TMDB enrichment by TmdbId is the frontend's job, not done server-side
// (an N-item watchlist would otherwise mean N extra TMDB calls per page
// load).
type TraktWatchlistItem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
	TMDBID int    `json:"tmdbId"`
}

// BrowseEntry is one directory GET /api/browse's response lists — a
// subdirectory of the requested path, never a file (the endpoint's root-
// folder picker use case has no reason to surface files).
type BrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BrowseResponse is GET /api/browse's response — Path echoes back the
// resolved, cleaned directory that was listed (empty when no path was
// requested, in which case Entries is the fixed set of browsable roots
// themselves). See internal/api/browse.go for the allowlist and validation
// this endpoint enforces.
type BrowseResponse struct {
	Path    string        `json:"path"`
	Entries []BrowseEntry `json:"entries"`
}

// --- Optional raw RSS feed rows (internal/rssfeeds + internal/rssfeed) — a
// per-row raw RSS 2.0 feed URL (NZBGeek saved-search style), fetched and
// parsed server-side, rendered as one more optional Discover row. Target is
// "movie" | "tv" | "adult" | "adult-movie" (a feed belongs to exactly one
// mode, no "mixed"). "adult" is scene RSS; "adult-movie" is Adult Movies RSS.
// Both are Adult-section locked. Mirrors Slider's CRUD+reorder DTO shape
// almost exactly.
type RssFeed struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	// FeedURL is MASKED in every response — a feed URL commonly embeds an indexer
	// API key, encrypted at rest (feed_url_encrypted), and the Settings form must
	// never receive the real value back (a naive re-send on an untouched save
	// would round-trip it through the wire and into a plaintext-in-transit
	// exposure). The handler always emits "" here (omitempty drops it); the
	// frontend shows a "set" placeholder and sends null on preserve. Same posture
	// as ConnectionSummary never returning the raw APIKey.
	FeedURL   string `json:"feedUrl,omitempty"`
	Target    string `json:"target"`
	Protocol  string `json:"protocol"`
	SortOrder int    `json:"sortOrder"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// RssFeedUpdateRequest is the body of PUT /api/discover/rss-feeds/{id}
// (update only — Create has its own RssFeedCreateRequest below). FeedURL
// follows the same three-state secret rule as ConnectionUpsertRequest.APIKey
// (nil = preserve the stored URL, "" = reject as feed-url-required, non-empty =
// replace) — now that the URL is a masked secret, a naive `feedUrl: string`
// would silently wipe it on an untouched save. Protocol is required here: an
// update always resends the feed's current (or operator-changed) protocol,
// which the Store validates against its fixed enum.
type RssFeedUpdateRequest struct {
	Title    string  `json:"title"`
	FeedURL  *string `json:"feedUrl,omitempty"`
	Target   string  `json:"target"`
	Protocol string  `json:"protocol"`
	Enabled  bool    `json:"enabled"`
}

// RssFeedCreateRequest is the body of POST /api/discover/rss-feeds (create).
// It is RssFeedUpdateRequest with exactly two deltas, and every other field
// (Title, Target, Enabled) is identical:
//   - FeedURL is a required plain string, not the three-state *string of
//     update: create has no stored URL to preserve, so there is nothing to
//     express with a nil.
//   - Protocol is an optional *string: nil means "auto-detect the protocol
//     from the feed's enclosures server-side" (the Add flow with no manual
//     protocol field); a non-nil value is used as-is (Mainstream's
//     AddRssFeedModal, and the Adult fallback pop-up's retry after an
//     inconclusive detection, both send an explicit protocol). Because the
//     JSON field names match the old shared type, a present `protocol` string
//     unmarshals into a non-nil pointer exactly as before — Mainstream's wire
//     behavior is unchanged.
type RssFeedCreateRequest struct {
	Title    string  `json:"title"`
	FeedURL  string  `json:"feedUrl"`
	Target   string  `json:"target"`
	Protocol *string `json:"protocol,omitempty"`
	Enabled  bool    `json:"enabled"`
}

// ProtocolUndetectedResponse is the 422 body returned by create (with an
// omitted protocol) and rescan when the feed's enclosures don't yield a
// confident torrent/usenet determination. Error is always the fixed sentinel
// "protocol_undetected", which the frontend's fallback pop-up keys on to
// prompt the operator for a one-time manual protocol pick.
type ProtocolUndetectedResponse struct {
	Error string `json:"error"`
}

// RssFeedReorderRequest is POST /api/discover/rss-feeds/reorder's body — ids
// in display order, covering every existing feed exactly once. One explicit
// "here is the full new order" action, not a per-item bulk mutation (see
// rssfeeds.Store.Reorder's doc comment for why).
type RssFeedReorderRequest struct {
	IDs []int `json:"ids"`
}

// RssFeedItem is one resolved item from GET /api/discover/rss-feeds/{id}/resolve
// — a fully-resolved release (a real downloadUrl+protocol already in hand,
// unlike a TMDB/TPDB catalog item), mapped from rssfeed.Item. DownloadURL is
// the item's enclosure URL, falling back to its Link when the item has no
// enclosure (a malformed/no-enclosure item). SizeBytes is the enclosure's
// byte length, 0 when absent. Indexer is the feed's own configured Title,
// reusing the existing free-form Indexer display field grabs already have —
// see internal/api/rss_feeds.go's resolve handler.
//
// ResolvedTitle/ResolvedStudio/ResolvedImage are populated ONLY for an
// Adult-targeted feed whose item enclosure key matched a row in the Adult
// identify pipeline's pool (adult_newest_releases) — the feed-first-identify
// resolved poster/title/studio for this exact release, mirroring
// AdultDiscoverItem.Title/Studio/Image's shape (ResolvedImage is a raw TPDB CDN
// URL the client MUST route through the image proxy, never hot-link). All three
// are omitempty because they are only ever set on that one path: a Movies/Series
// feed has no pool to join against (the handler never even looks one up for a
// non-Adult target), and an Adult item not yet identified — or that matched
// nothing — leaves them empty, so the card falls back to the raw feed Title with
// no poster exactly as before this field existed. The grab still uses the raw
// Title + DownloadURL, never these display-only fields.
type RssFeedItem struct {
	Title          string `json:"title"`
	Link           string `json:"link"`
	PubDate        string `json:"pubDate"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
	DownloadURL    string `json:"downloadUrl"`
	Protocol       string `json:"protocol"`
	Indexer        string `json:"indexer"`
	ResolvedTitle  string `json:"resolvedTitle,omitempty"`
	ResolvedStudio string `json:"resolvedStudio,omitempty"`
	ResolvedImage  string `json:"resolvedImage,omitempty"`
}

// --- Discover row order (internal/api/discover_row_order.go) — a
// best-effort display-order hint over the FULL merged row set (built-in rows
// plus every dynamic row type: sliders, adult newest rows, rss feeds), one
// per screen ("mainstream" | "adult"). NOT backed by its own table — a thin
// wrapper over two fixed settings.Store keys, since the value is just a
// JSON array of stable string keys (e.g. "builtin:trending-movies",
// "slider:4", "rssfeed:2"). Deliberately not validated against a fixed
// known-id set the way RssFeedReorderRequest is — see
// internal/api/discover_row_order.go's doc comment: the frontend appends any
// key it knows about but doesn't find in the stored order to the end, and
// skips any stored key that no longer resolves to anything live.
type RowOrderResponse struct {
	Keys []string `json:"keys"`
}

// RowOrderRequest is PUT /api/discover/row-order/{screen}'s body — the full
// replacement key order, same shape as the response.
type RowOrderRequest struct {
	Keys []string `json:"keys"`
}

// RowHiddenResponse is GET /api/discover/row-hidden/{screen}'s body — the set
// of currently-hidden structural row keys for one screen; absent-from-list
// means visible. A sibling of RowOrderResponse (same shape) but deliberately
// a distinct type: the semantics differ (there is no append-missing concept
// for hidden state), so it is stored under its own settings key.
type RowHiddenResponse struct {
	Keys []string `json:"keys"`
}

// RowHiddenRequest is PUT /api/discover/row-hidden/{screen}'s body — the full
// replacement set of currently-hidden structural row keys for one screen;
// absent-from-list means visible.
type RowHiddenRequest struct {
	Keys []string `json:"keys"`
}

// SysinfoServerDisk is per-physical-disk I/O from /proc/diskstats.
type SysinfoServerDisk struct {
	Name     string  `json:"name"`
	ReadBPS  float64 `json:"readBps"`
	WriteBPS float64 `json:"writeBps"`
}

// SysinfoStorageMount is one named filesystem mount's usage reading.
type SysinfoStorageMount struct {
	Name       string `json:"name"`
	TotalBytes int64  `json:"totalBytes"`
	AvailBytes int64  `json:"availBytes"`
	Configured bool   `json:"configured"`
}

// SysinfoGPU is one GPU's point-in-time reading. UtilPercent is -1 when
// utilization is unavailable (NVIDIA/Intel expose no sysfs util path without a
// vendor library); PowerMicrowatts is 0 when unavailable. See
// internal/sysinfo/gpu.go for the per-vendor sourcing and its soft-failure rule.
type SysinfoGPU struct {
	Name            string `json:"name"`
	UtilPercent     int    `json:"utilPercent"` // -1 = unavailable
	VRAMUsedBytes   int64  `json:"vramUsedBytes"`
	VRAMTotalBytes  int64  `json:"vramTotalBytes"`
	PowerMicrowatts int64  `json:"powerMicrowatts"`
}

// SysinfoSnapshot is one live-resource reading streamed by GET /api/admin/sysinfo/stream.
type SysinfoSnapshot struct {
	CPUPercent            float64               `json:"cpuPercent"`
	MemUsedBytes          int64                 `json:"memUsedBytes"`
	MemLimitBytes         int64                 `json:"memLimitBytes"`
	NetRxBPS              float64               `json:"netRxBps"`
	NetTxBPS              float64               `json:"netTxBps"`
	ContainerDiskReadBPS  float64               `json:"containerDiskReadBps"`
	ContainerDiskWriteBPS float64               `json:"containerDiskWriteBps"`
	ServerDisks           []SysinfoServerDisk   `json:"serverDisks"`
	StorageMounts         []SysinfoStorageMount `json:"storageMounts"`
	GPUs                  []SysinfoGPU          `json:"gpus"`
}

// WebhookSummary is one outbound webhook subscription as returned by the API.
// The signing secret is never included — secretSet indicates whether one is stored.
type WebhookSummary struct {
	ID        int64    `json:"id"`
	URL       string   `json:"url"`
	SecretSet bool     `json:"secretSet"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// WebhookCreateRequest is the body for POST /api/webhooks.
// Secret is the plaintext signing secret; omit or set "" for no signing.
type WebhookCreateRequest struct {
	URL     string   `json:"url"`
	Secret  string   `json:"secret"`
	Events  []string `json:"events"`
	Enabled bool     `json:"enabled"`
}

// WebhookUpdateRequest is the body for PUT /api/webhooks/{id}.
// Secret follows three-state semantics: null/absent = preserve existing,
// "" = clear, non-empty = update.
type WebhookUpdateRequest struct {
	URL     string   `json:"url"`
	Secret  *string  `json:"secret"`
	Events  []string `json:"events"`
	Enabled bool     `json:"enabled"`
}

// AllWebhookEvents is the canonical list of event names for webhook subscriptions.
var AllWebhookEvents = []string{
	"rename.applied",
	"purge.applied",
	"dedup.applied",
	"grab.completed",
}

// --- Unified downloader (aria2c queue) -------------------------------------

// Download is one item in the unified downloader's queue (active, waiting, or
// recently stopped), as reported by the aria2c engine (see
// GET /api/downloads and the /api/downloads/stream SSE). All numeric fields
// are real int64s here — the api layer parses aria2's decimal-string wire
// values before this DTO is emitted.
//
// Claude 2026-08-04: SeedCount and UploadSpeed are torrent-only. Reason: a
// usenet download has no seeder/upload concept at all — toUsenetDTODownload
// leaves both at their zero value, and the frontend hides both fields
// entirely for protocol == "usenet" rather than rendering a zero, so a zero
// here always means "torrent, no seeders / no upload yet", never "usenet".
// Review if: usenet ever gains a peer-exchange or reciprocation concept.
type Download struct {
	GID             string `json:"gid"`
	Status          string `json:"status"` // "active" | "waiting" | "paused" | "error" | "complete" | "removed"
	Filename        string `json:"filename"`
	TotalLength     int64  `json:"totalLength"`
	CompletedLength int64  `json:"completedLength"`
	DownloadSpeed   int64  `json:"downloadSpeed"`
	SeedCount       int64  `json:"seedCount"`
	UploadSpeed     int64  `json:"uploadSpeed"`
	Protocol        string `json:"protocol"` // "torrent" | "usenet"
	ErrorMessage    string `json:"errorMessage"`
}

// DownloadProtocolTorrent and DownloadProtocolUsenet are the two values
// Download.Protocol can take. Exported so the mappers in internal/api set a
// literal from here rather than each spelling its own string, per D-2 in the
// implementing plan — Protocol is a per-mapper constant (the caller's
// argument type already knows it with certainty), not a re-derived GID-prefix
// convention.
const (
	DownloadProtocolTorrent = "torrent"
	DownloadProtocolUsenet  = "usenet"
)

// DownloaderConfig is the unified downloader's operator-tunable settings
// (GET/PUT /api/downloader/config).
//
// PUT IS A FULL-DOCUMENT REPLACE. Every field is a plain (non-pointer,
// non-omitempty) type, so an omitted field arrives as its zero value and is
// stored as such — a client must GET, mutate, and PUT the whole document back.
// Omitting MaxConcurrent, MaxConnections or ListenPort is a loud 400; omitting
// DHTEnabled or PEXEnabled silently DISABLES them (both default true), and
// omitting the rate/ratio/duration/stale fields silently means unlimited/off.
//
// StagingDir comes back "" when unset — the handler has no data directory to
// synthesize the boot default (<dataDir>/downloads) from. Empty is a normal
// "not configured" state here, not an error.
type DownloaderConfig struct {
	StagingDir    string `json:"stagingDir"`
	MaxConcurrent int    `json:"maxConcurrent"`
	// MaxConnections applies to the torrent engine ONLY. Usenet connection
	// counts are per-subscription (serviceconn.Connection.MaxConns /
	// ServiceConnectionSummary.MaxConns above, one value per registered NNTP
	// server), not a single global figure — this field has no effect on
	// Usenet downloads.
	MaxConnections int `json:"maxConnections"`

	// DownloadRateLimitBytes caps the torrent engine's aggregate download rate
	// in bytes/sec. 0 means unlimited.
	DownloadRateLimitBytes int `json:"downloadRateLimitBytes"`
	// DHTEnabled and PEXEnabled toggle the two peer-discovery mechanisms.
	// Both default true, matching the torrent library's own defaults.
	DHTEnabled bool `json:"dhtEnabled"`
	PEXEnabled bool `json:"pexEnabled"`
	// ListenPort is the torrent engine's peer listen port, 1024-65535.
	ListenPort int `json:"listenPort"`
	// ObfuscationMode is one of "require", "prefer" or "off". Note that "off"
	// is the STRICTEST setting, not the most permissive one: it rejects every
	// encrypted peer connection rather than merely preferring plaintext, which
	// can measurably shrink the reachable swarm. UI copy must say so.
	ObfuscationMode string `json:"obfuscationMode"`
	// SeedingEnabled is the seeding master switch, default OFF. Turning it on
	// keeps a second copy of completed content in the staging directory (so
	// roughly double the disk space for that content) until a limit below is
	// reached; the library copy is never affected.
	SeedingEnabled bool `json:"seedingEnabled"`
	// SeedRatioLimit stops seeding once this upload:download ratio is reached.
	// 0 means no ratio limit.
	SeedRatioLimit float64 `json:"seedRatioLimit"`
	// SeedDurationMinutes stops seeding after this long. 0 means no duration
	// limit. Both limits apply — whichever is reached first wins.
	SeedDurationMinutes int `json:"seedDurationMinutes"`
	// StaleThresholdMinutes is how long a torrent may make no progress with no
	// connected peers before it is cancelled, its partial files deleted, and
	// the title requeued for another attempt. 0 disables stale detection.
	StaleThresholdMinutes int `json:"staleThresholdMinutes"`
}

// DownloaderConfigApplyResult is PUT /api/downloader/config's response body.
// The PUT does not merely store settings — it applies them to the running
// torrent engine before persisting anything, so the response has to tell the
// operator WHICH of the two apply paths ran. A save that only moved the rate
// limit is disruption-free; a save that changed the listen port tore the engine
// down and stood it back up, briefly interrupting every in-flight download.
// Presenting both as an undifferentiated "Saved" hides a real consequence.
type DownloaderConfigApplyResult struct {
	// Applied is "live" when every changed field could be patched into the
	// running engine in place, or "rebuilt" when at least one rebuild-class
	// field changed and the engine had to be reconstructed.
	//
	// Rebuild-class fields are stagingDir, listenPort, dhtEnabled, pexEnabled,
	// obfuscationMode, and seedingEnabled going false->true (that one direction
	// only — the engine's upload gate is fixed at client construction, so
	// turning seeding ON cannot be patched live, while turning it OFF can).
	// Everything else is live.
	Applied string `json:"applied"`
	// RestartRequired is true exactly when Applied is "rebuilt". It is a
	// separate field rather than something the client derives because it is the
	// stable signal across two possible backend implementations: the engine
	// rebuild that shipped restarts the engine in-process, but the documented
	// fallback for that work was store-and-require-an-operator-restart, and the
	// UI copy keyed on this flag is the same either way — "this change did not
	// take effect quietly and instantly". Render it as a warning, not an error.
	RestartRequired bool `json:"restartRequired"`
	// Message is operator-facing copy describing what happened, suitable for
	// the save-status line verbatim.
	Message string `json:"message"`
}

// --- Downloads: bulk cancel + global pause ---------------------------------

// BulkCancelRequest is POST /api/downloads/cancel-batch's body — cancel several
// downloads (and delete their files, same as the per-item DELETE) in one call.
// Each GID is routed and cancelled independently with skip-and-continue
// semantics (one failure never blocks the rest).
type BulkCancelRequest struct {
	GIDs []string `json:"gids"`
}

// BulkCancelResultItem is one GID's outcome. OK true means it was cancelled and
// its files deleted; OK false means it was skipped and Error explains why.
type BulkCancelResultItem struct {
	GID   string `json:"gid"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// BulkCancelResponse is POST /api/downloads/cancel-batch's response — always
// HTTP 200; per-item success/failure lives here in Results, in request order.
type BulkCancelResponse struct {
	Results []BulkCancelResultItem `json:"results"`
}

// DownloadPauseState is the global download pause toggle
// (GET/PUT /api/downloads/pause-state). When Paused is true every currently
// active download is paused AND every new grab is blocked at the shared dispatch
// choke point (internal/api/search.go's dispatchToDownloadClient) until it is set
// back to false. It is a single system-wide flag, distinct from each row's
// existing per-item pause/resume.
type DownloadPauseState struct {
	Paused bool `json:"paused"`
}

// --- Worker nodes (worker-node feature) -------------------------------------
//
// GET /api/nodes returns the server's live view of connected worker nodes for
// the Settings → Nodes tab. Status is derived server-side from heartbeat
// freshness ("online" when within 90s, "offline" otherwise) so the client
// never has to replicate that threshold. Capabilities is the hwaccel string
// reported by the node at connect time (e.g. ["cuda"]). LastHeartbeat is
// RFC3339.

// NodeInfo is one connected (or recently disconnected) worker node as
// returned by GET /api/nodes.
//
// MaxJobs is the node's stored, operator-owned concurrency cap (from
// nodesettings.Store — 0 if nothing was ever saved). It's included here so
// the frontend's EditSettingsModal can preload the real current value
// instead of defaulting its input to 0: without this, an operator who opens
// the modal just to look at the (now read-only) path mappings and clicks
// "Save settings" without touching MaxJobs would silently reset an existing
// non-zero cap to 0, since updateNodeSettingsOperatorAuth applies
// body.MaxJobs unconditionally by design (MaxJobs is the one field operator
// auth is meant to write). The frontend already fetches this list on
// Nodes-screen load and again whenever the modal opens, so extending this
// existing DTO avoids a new endpoint.
type NodeInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`        // "online" | "offline"
	Capabilities  []string `json:"capabilities"`  // hwaccels, e.g. ["cuda"]
	LastHeartbeat string   `json:"lastHeartbeat"` // RFC3339
	MaxJobs       int      `json:"maxJobs"`       // stored operator-owned concurrency cap, 0 = unlimited/unset
	// PauseDispatch is the node's stored, server-owned dispatch-pause bit (from
	// nodesettings.Store — false if nothing was ever saved). Included so the
	// frontend can preload the real current value into EditSettingsModal's
	// pause toggle and render a "Paused" badge in the node list, exactly as
	// MaxJobs is preloaded — never defaulting a toggle to false and flipping an
	// existing pause.
	PauseDispatch bool `json:"pauseDispatch"`
	// CPUCapPercent is the node's stored, operator-owned max-CPU governor
	// ("% of total CPU", 0 = unlimited/unset), preloaded exactly like MaxJobs so
	// the frontend's slider shows the real current value instead of defaulting to
	// 0 (which a subsequent Save would then persist, silently clearing an
	// existing cap). NOT omitempty — an explicit 0 is meaningful.
	CPUCapPercent int `json:"cpuCapPercent"`
	// Enforcement is the node daemon's STATIC capability report: whether a real
	// cgroup CPU cap can work on this node at all (systemd + cgroup-v2 present and
	// writable). It is deliberately DISTINCT from CPUCapApply below — a mechanism
	// being present at startup does not mean any specific apply succeeded, so this
	// must never stand in for actual enforcement. Values: "available" |
	// "unavailable" | "" (not yet reported). Stage 2 has no daemon source for this
	// yet, so the server emits "" (unknown); Stage 3 wires the daemon's real probe
	// result through GET /status → SSE → here.
	Enforcement string `json:"enforcement"`
	// CPUCapApply is the LAST-APPLY result: the quota actually in force right now
	// plus any error from the most recent apply attempt — reality, not intent.
	// Kept separate from Enforcement (static capability) so the UI can show a
	// "capable but not currently enforced: <reason>" state. Its zero value
	// (EffectivePercent 0, no error) honestly reads as "nothing enforced yet",
	// never as a fabricated success. Populated by Stage 3.
	CPUCapApply NodeCPUCapApply `json:"cpuCapApply"`
}

// NodeCPUCapApply is a node's most-recent CPU-cap apply result, carried on
// NodeInfo. EffectivePercent is the resolved cap actually in force (the
// percentage the daemon last successfully applied; 0 = uncapped / nothing
// applied). Error is the failure from the most recent apply attempt, empty when
// the last apply succeeded or none has run. The pair is deliberately two fields,
// never collapsed into one: an EffectivePercent that disagrees with the
// configured NodeInfo.CPUCapPercent, or a non-empty Error, is what the frontend
// reads as "not currently enforced". Populated by Stage 3 (the daemon reports it
// via GET /status); Stage 2 defines the shape only and emits the zero value.
type NodeCPUCapApply struct {
	EffectivePercent int    `json:"effectivePercent"`
	Error            string `json:"error,omitempty"`
}

// PendingNodeInfo is a node waiting for operator approval in GET /api/nodes.
type PendingNodeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PairingCode string `json:"pairingCode"`
	RequestedAt string `json:"requestedAt"` // RFC3339
}

// PathMapping translates one server-side path prefix to its local equivalent
// on the worker node. This is the wire shape pushed down to the node itself
// (over the settings SSE event) — unchanged by the library-path-driven
// mapping feature; only how the operator PRODUCES a []PathMapping changed
// (see NodePathMappingInput below), not what the node receives.
type PathMapping struct {
	Server string `json:"server"`
	Local  string `json:"local"`
}

// NodeSettingsRequest is the body for PUT /api/nodes/{id}/settings — served by
// BOTH operator auth and node bearer auth, with the write partitioned by which
// credential authenticated it (D1/D3):
//
//   - Operator auth writes ONLY MaxJobs (concurrency stays an operator knob);
//     PathMap in an operator body is ignored — it is now node-owned.
//   - Node bearer auth writes ONLY PathMap (a single-key delta per entry, or a
//     Clear per entry, see NodePathMappingInput), keyed by the node's bearer
//     identity — NOT the URL {id} — and always preserves the stored MaxJobs.
//
// PathMap and MaxJobs no longer "travel together" on one write path (that
// coupling was what would silently reset MaxJobs to 0 on a path-only save); the
// auth partition is what now prevents that, so each side touches exactly one.
//
// MediaRoots is the node's locally-asserted media-root allowlist, reported by
// the node on its own push (mediaRoots is node-local — the server has no other
// way to know it). A node-auth request with zero MediaRoots is hard-rejected
// before any verification runs: a mapping may only be authored once at least
// one independent containment boundary exists on the node. Ignored on the
// operator path.
type NodeSettingsRequest struct {
	PathMap []NodePathMappingInput `json:"pathMap"`
	MaxJobs int                    `json:"maxJobs"`
	// CPUCapPercent is the operator-owned max-CPU governor ("% of total CPU",
	// 0 = unlimited). Operator auth writes it (alongside MaxJobs); it is ignored
	// on the node-auth path, exactly like MaxJobs. NOT omitempty — an explicit 0
	// must cross the wire to clear a cap, mirroring MaxJobs.
	CPUCapPercent int      `json:"cpuCapPercent"`
	MediaRoots    []string `json:"mediaRoots,omitempty"`
}

// ApproveNodeRequest is the body for POST /api/nodes/{id}/approve.
type ApproveNodeRequest struct {
	PathMap       []NodePathMappingInput `json:"pathMap"`
	MaxJobs       int                    `json:"maxJobs"`
	CPUCapPercent int                    `json:"cpuCapPercent"`
}

// NodePauseRequest is the body for PUT /api/nodes/{id}/pause — the dedicated,
// dual-authed dispatch-pause toggle. It carries ONLY the pause bit: a value
// bool (not the three-state *bool of ConnectionUpsertRequest.APIKey) is safe
// here precisely because there is no sibling field on this wire to reset —
// pause is written by its own column-scoped storage method and its own
// endpoint, so it can never carry or clobber MaxJobs/PathMap (the
// parallel-write footgun, eliminated structurally at both the HTTP and storage
// layers). Served by BOTH operator auth (keyed by the URL {id}) and node
// bearer auth (keyed by the bearer identity, never the URL {id}).
type NodePauseRequest struct {
	Paused bool `json:"paused"`
}

// NodesResponse is GET /api/nodes's response.
type NodesResponse struct {
	Nodes   []NodeInfo        `json:"nodes"`
	Pending []PendingNodeInfo `json:"pending"`
}

// --- Library-path-driven node path mapping ---------------------------------
//
// Replaces the free-form, operator-typed PathMapping editor with a fixed set
// of rows, one per configured library root-folder path. LibraryPathKey
// values are the exact settings-store keys these paths are already stored
// under (see internal/api/library.go's *LibraryRootFolderKey constants and
// internal/mode.Mode.KidsRootPathKey) so no separate translation table is
// needed between this feature and Library settings' own storage. Adult has
// no kids-root setting, so there are 5 keys total, not 6.

// LibraryPathKey identifies one of the 5 library root-folder-type settings a
// node's path mapping can correspond to.
type LibraryPathKey string

const (
	LibraryPathMoviesRoot LibraryPathKey = "movies_library_root_folder"
	LibraryPathSeriesRoot LibraryPathKey = "series_library_root_folder"
	LibraryPathAdultRoot  LibraryPathKey = "adult_library_root_folder"
	LibraryPathMoviesKids LibraryPathKey = "movies_kids_root_path"
	LibraryPathSeriesKids LibraryPathKey = "series_kids_root_path"
)

// NodePathMappingEntry is one row in a node's library-path-driven path
// mapping. ServerPath is read fresh from Library settings every time (a
// read-only label — Library settings, not this feature, owns configuring
// it). NodePath is what's persisted for this node. Configured is false when
// ServerPath is empty (the library path hasn't been set up yet) — the row
// still renders, just disabled, rather than being omitted.
type NodePathMappingEntry struct {
	Key        LibraryPathKey `json:"key"`
	ServerPath string         `json:"serverPath"`
	NodePath   string         `json:"nodePath"`
	Configured bool           `json:"configured"`
}

// NodePathMappingsResponse is GET /api/nodes/{id}/path-mappings's response —
// read-only; see NodeSettingsRequest's doc comment for why there is no
// corresponding write endpoint at this path.
type NodePathMappingsResponse struct {
	Entries []NodePathMappingEntry `json:"entries"`
}

// NodePathMappingInput is one entry in a save request (via
// NodeSettingsRequest/ApproveNodeRequest's PathMap field). ServerPath is
// never submitted — it's derived server-side from Library settings by Key,
// never editable here.
//
// Clear is the explicit delete signal (D7): true means "remove this key's
// mapping row entirely." A blank NodePath still means "skip / leave this key
// untouched" — it is NOT overloaded to mean delete (blank is skipped on every
// push path, so it cannot express deletion). Clear is the only delete signal;
// when Clear is true, NodePath is ignored.
type NodePathMappingInput struct {
	Key      LibraryPathKey `json:"key"`
	NodePath string         `json:"nodePath"`
	Clear    bool           `json:"clear,omitempty"`
}

// NodeBrowseEntry is one directory GET /api/nodes/{id}/browse's response
// lists — a subdirectory of the requested path on the NODE's filesystem, not
// the server's. Deliberately a distinct type from BrowseEntry: the node has
// no allowlist restriction the way the server's browse.go does (see
// cmd/sakms-node's browse implementation), so sharing the type would risk
// that validation difference leaking silently between the two.
type NodeBrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// NodeBrowseResponse is GET /api/nodes/{id}/browse's response. Path echoes
// back the directory that was listed on the node.
type NodeBrowseResponse struct {
	Path    string            `json:"path"`
	Entries []NodeBrowseEntry `json:"entries"`
}

// SeasonState is one row of
// GET /api/modes/series/library/{seriesID}/seasons — Series-only, since Movies
// has no seasons and Adult has no episode model.
//
// EpisodeCount counts every episode ROW the season has, on disk or not;
// MissingCount is the subset with no file yet, so downloaded = EpisodeCount -
// MissingCount. Monitored is false for a season with no monitored row at all —
// there is no tri-state, an absent row means unmonitored.
type SeasonState struct {
	SeasonNumber int  `json:"seasonNumber"`
	EpisodeCount int  `json:"episodeCount"`
	MissingCount int  `json:"missingCount"`
	Monitored    bool `json:"monitored"`
}

// SetSeasonMonitoredRequest is the body of both season-monitoring writes: the
// per-season PUT .../seasons/{seasonNumber}/monitored and the all-seasons
// PUT .../seasons/monitored.
//
// Setting it false is not a pure flag write — the handler also cancels that
// season's queued air-date retries in the same request, since nothing else in
// the retry loop knows about monitored state.
type SetSeasonMonitoredRequest struct {
	Monitored bool `json:"monitored"`
}

// SeriesNewSeasonDiscoveryResponse / SeriesNewSeasonDiscoveryRequest back
// GET/PUT /api/settings/series-new-season-discovery — off by default.
//
// It governs ONLY entirely-new seasons (a season TMDB reports that has no
// episode rows at all): those are synced and auto-monitored when it is on.
// Seasons that already have episode rows are always synced regardless, because
// monitoring gates searching, never metadata.
//
// Unlike the usenet auto-grab toggle, this one writes no coupled interval: it
// has no scheduler of its own, running instead inside the existing retry cycle.
type SeriesNewSeasonDiscoveryResponse struct {
	Enabled bool `json:"enabled"`
}

type SeriesNewSeasonDiscoveryRequest struct {
	Enabled bool `json:"enabled"`
}

// --- Calendar: Upcoming + pre-release requests -----------------------------
//
// Calendar's two Upcoming views are structurally different queries against
// different backends, not one query over a mode variable, so they have two
// separate entry shapes rather than a shared one (see the Calendar plan's
// route-shape justification). Neither carries a download URL: grabs.Grab's
// DownloadURL is json:"-" because an indexer/NZB URL commonly embeds an API
// key, and no DTO here may reintroduce a passthrough for it.

// UpcomingSeriesEntry is one not-yet-aired episode of an already-tracked
// series, sourced from library.MissingEpisodes — tracked series only, so this
// view is bounded by the operator's own library rather than by a catalog
// query. SeriesID is the library row id (library.Series.ID), TMDBID the show's
// TMDB id; AirDate is a bare date string as TMDB returns it.
type UpcomingSeriesEntry struct {
	SeriesTitle   string `json:"seriesTitle"`
	SeriesID      int64  `json:"seriesId"`
	TMDBID        int    `json:"tmdbId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	EpisodeTitle  string `json:"episodeTitle"`
	AirDate       string `json:"airDate"`
}

// UpcomingMovieEntry is one upcoming movie release from TMDB.
//
// ReleaseKind is "digital" or "planned" and nothing else — the same two values
// tmdb.ReleaseKindDigital/ReleaseKindPlanned carry. They are repeated here as
// literal strings rather than referenced, because this package deliberately
// imports no internal/domain package: it is a hand-picked wire boundary, not a
// re-export of domain types (hence the flat fields below rather than embedding
// tmdb.Item the way tmdb.UpcomingMovie does).
// AlreadyRequested is the server-computed answer to "is this movie already
// tracked or already being grabbed" — the flag AC6's click-to-request
// affordance keys on, so the client never cross-references two lists itself
// (and never asks Prowlarr: the signal comes from the tracked library plus the
// grab log, per CLAUDE.md's Discover-never-queries-Prowlarr rule). Deliberately
// NOT omitempty: false is the meaningful "clickable" case and must reach
// TypeScript as a required boolean, not an optional one.
type UpcomingMovieEntry struct {
	TMDBID           int    `json:"tmdbId"`
	Title            string `json:"title"`
	PosterPath       string `json:"posterPath,omitempty"`
	ReleaseDate      string `json:"releaseDate"`
	ReleaseKind      string `json:"releaseKind"`
	AlreadyRequested bool   `json:"alreadyRequested"`
}

// PreReleaseRequestRequest is POST /api/calendar/prerelease-request's body —
// the operator clicking an un-requested upcoming movie. ReleaseDate is the
// date the resulting grab row is held until.
type PreReleaseRequestRequest struct {
	TMDBID      int    `json:"tmdbId"`
	Title       string `json:"title"`
	ReleaseDate string `json:"releaseDate"`
}

// PreReleaseRequestResponse reports the held grab row the request produced.
//
// AlreadyRequested is true when the click matched an existing outstanding
// request instead of creating one — the UI says "already requested" rather
// than silently appearing to succeed twice. GrabID is int64 to match
// RequestStatusItem.GrabID, since both name the same grabs.Grab row.
type PreReleaseRequestResponse struct {
	GrabID           int64  `json:"grabId"`
	HeldUntil        string `json:"heldUntil"`
	AlreadyRequested bool   `json:"alreadyRequested"`
}

// --- Section PIN lock (internal/api/sectionlock.go) ------------------------
//
// Mirrors of internal/api's handler-local sectionLock* structs, guarded by
// internal/api/dto_drift_test.go. The PIN fields below are plain strings, NOT
// the *string/omitempty three-state pattern Guardrail #5 reserves for stored
// secrets: a PIN here is either being VERIFIED (CurrentPin) or REPLACED
// outright (NewPin), never "preserved unchanged" — so there is no third state
// to express, and a pointer would only invite a frontend to send null and
// mean something the server does not implement.
//
// No field here carries omitempty. Each one is meaningful at its zero value —
// pinSet:false, unlocked:false and enforcementAvailable:false are the states
// the panel must actually render, and an empty lockedSections is "nothing is
// locked", not "unknown" — so they must all reach TypeScript as required
// fields rather than optional ones.

// SectionLockStatusResponse is GET /api/section-lock/status: what the Settings
// panel and the sidebar lock badges read. The PIN itself, and its hash, never
// cross this boundary — PinSet is the only thing said about it.
//
// EnforcementAvailable is false when the lock cannot enforce anything on this
// instance (SAKMS_SECTION_LOCK_DISABLE is set, or the instance runs auth mode
// "none"); the panel renders disabled on false.
type SectionLockStatusResponse struct {
	PinSet               bool     `json:"pinSet"`
	LockedSections       []string `json:"lockedSections"`
	Unlocked             bool     `json:"unlocked"`
	EnforcementAvailable bool     `json:"enforcementAvailable"`
}

// SectionLockUnlockRequest is POST /api/section-lock/unlock's body — the PIN
// exchanged for the sakms_unlock ticket cookie.
type SectionLockUnlockRequest struct {
	Pin string `json:"pin"`
}

// SectionLockPinRequest serves BOTH PUT /api/section-lock/pin (both fields)
// and DELETE /api/section-lock/pin (CurrentPin only) — one shape for two
// routes, matching the single handler-local struct it mirrors.
//
// CurrentPin is required whenever a PIN already exists, and is required from
// the BODY even when the caller already holds a live unlock ticket: the ticket
// says "the operator entered the PIN recently", which is the right bar for
// viewing a locked section and the wrong one for rewriting the lock's own
// configuration.
type SectionLockPinRequest struct {
	CurrentPin string `json:"currentPin"`
	NewPin     string `json:"newPin"`
}

// SectionLockSectionsRequest is PUT /api/section-lock/sections' body: the
// complete replacement set of locked section ids, plus the same CurrentPin
// re-authentication SectionLockPinRequest carries.
//
// Sections is a full replacement, not a delta, and a non-empty array is
// rejected with 400 while no PIN is set — locking a section with no PIN in
// existence would deny with no credential that could ever satisfy the gate.
type SectionLockSectionsRequest struct {
	CurrentPin string   `json:"currentPin"`
	Sections   []string `json:"sections"`
}

// --- Pruning rules (internal/pruning) — propose-only Purge safety rules ----
//
// Mirrors pruning.Rule's wire shape (see .omc/plans/autopilot-impl-pruning-rules.md
// §2.1). Criteria is the current matching surface (AND'd rows). The five
// scalar fields stay on the wire for legacy payloads and Match fallback
// (0/0/""/[]/0 means "not configured"). Unlike ConnectionUpsertRequest.APIKey
// these are plain values, never *T.

// PruningCriterion is one AND'd row on a pruning rule: field + operator +
// free-fill value, plus unit when the field needs one (age/size/rating).
// Tag rows send Values (chip list) and MatchMode ("any" | "all"); Value is
// the one-element fallback for pre-0015 payloads.
type PruningCriterion struct {
	Field     string   `json:"field"`
	Op        string   `json:"op"`
	Value     string   `json:"value"`
	Unit      string   `json:"unit,omitempty"`
	Values    []string `json:"values,omitempty"`
	MatchMode string   `json:"matchMode,omitempty"`
}

// PruningRule is the full read shape for GET /api/pruning-rules and its
// per-id counterpart — one operator-authored rule for the Purge workflow.
type PruningRule struct {
	ID               int64              `json:"id"`
	Name             string             `json:"name"`
	Mode             string             `json:"mode"`
	AgeDays          int                `json:"ageDays,omitempty"`
	SizeBytes        int64              `json:"sizeBytes,omitempty"`
	QualityTierFloor string             `json:"qualityTierFloor,omitempty"`
	Tags             []string           `json:"tags,omitempty"`
	MinRating        int                `json:"minRating,omitempty"`
	Criteria         []PruningCriterion `json:"criteria,omitempty"`
	Enabled          bool               `json:"enabled"`
	CreatedAt        string             `json:"createdAt"`
	UpdatedAt        string             `json:"updatedAt"`
}

// PruningRuleUpsertRequest is the body of POST /api/pruning-rules (create)
// and PUT /api/pruning-rules/{id} (update) — every editable field, mirroring
// SliderUpsertRequest's plain-non-pointer shape: nothing here is a secret, so
// a save always sends the full rule rather than a partial "preserve
// unchanged" update. The frontend always sends Criteria and zeros the five
// scalars; empty Criteria still accepts the scalars (legacy / Go tests).
type PruningRuleUpsertRequest struct {
	Name             string             `json:"name"`
	Mode             string             `json:"mode"`
	AgeDays          int                `json:"ageDays"`
	SizeBytes        int64              `json:"sizeBytes"`
	QualityTierFloor string             `json:"qualityTierFloor"`
	Tags             []string           `json:"tags"`
	MinRating        int                `json:"minRating"`
	Criteria         []PruningCriterion `json:"criteria"`
	Enabled          bool               `json:"enabled"`
}

// PruningRulePreviewResponse is POST .../pruning-rules/preview's response
// (spec §13.1) — the soft-warning match count for a draft or existing rule,
// shown before/after save without ever blocking it.
type PruningRulePreviewResponse struct {
	MatchCount int `json:"matchCount"`
}

// --- Stash-box databases (internal/stashboxdb) — the configurable registry --
//
// Mirrors stashboxdb.Summary's wire shape (see
// .omc/plans/ralplan-adult-identify-configurable-databases.md §2.1/§2.6).
// EVERY row is a peer: the two rows migration 0061 seeds for StashDB and
// FansDB are fully editable, reorderable and deletable, and there is
// deliberately NO "builtin"/reserved flag on the wire — the UI renders every
// row identically (Stage 5 AC11). The internal secret_ref handle that routes
// where a row's key is stored is likewise never exposed: HasAPIKey/KeySuffix
// already report the RESOLVED key, so the UI masks uniformly with no idea
// which table the secret lives in.

// StashBoxDatabase is one configured stash-box-protocol database as exposed
// over the API — GET /api/stashbox-databases returns a list of these, and
// POST/PUT return the affected row. Redacted the same way ConnectionSummary
// is: never the secret, only HasAPIKey plus its last 4 characters.
type StashBoxDatabase struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	FansiteOnly bool   `json:"fansiteOnly"`
	HasAPIKey   bool   `json:"hasApiKey"`
	KeySuffix   string `json:"keySuffix,omitempty"`
	UpdatedAt   string `json:"updatedAt"`
}

// StashBoxDatabaseCreateRequest is POST /api/stashbox-databases' body. APIKey
// is a plain (non-pointer) string here, unlike the update below: a create has
// no stored secret to preserve, so all three states collapse to "this is the
// key", and an empty one is rejected outright.
type StashBoxDatabaseCreateRequest struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"apiKey"`
}

// StashBoxDatabaseUpdateRequest is PUT /api/stashbox-databases/{id}'s body.
// Every field is a pointer so an omitted field means "leave it alone" — and
// APIKey specifically carries the SAME three-state secret rule as
// ConnectionUpsertRequest.APIKey (absent = preserve, "" = clear, non-empty =
// set). Read that type's doc comment before touching this one: sending "" for
// an untouched key field is the exact bug that silently wipes a working
// stored secret, and it is a real incident class in this project's history.
type StashBoxDatabaseUpdateRequest struct {
	Name        *string `json:"name,omitempty"`
	Endpoint    *string `json:"endpoint,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	FansiteOnly *bool   `json:"fansiteOnly,omitempty"`
	APIKey      *string `json:"apiKey,omitempty"`
}

// StashBoxDatabaseReorderRequest is PUT /api/stashbox-databases/reorder's
// body: the complete set of stored ids in their new cascade order (index 0 is
// consulted first). A partial list is rejected rather than silently leaving
// an unlisted row at a stale priority — same full-set contract as
// RssFeedReorderRequest.
type StashBoxDatabaseReorderRequest struct {
	IDs []int64 `json:"ids"`
}

// StashBoxDatabaseTestRequest is POST /api/stashbox-databases/test's body —
// the STATELESS test, run against field values the operator has typed but not
// necessarily saved. The saved-row counterpart is
// POST /api/stashbox-databases/{id}/test-stored, which takes no body and
// resolves the key server-side (the client never holds it).
type StashBoxDatabaseTestRequest struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"apiKey"`
}
