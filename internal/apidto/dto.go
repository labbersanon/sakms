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
	Mode           string `json:"mode"`
	Available      bool   `json:"available"`
	ArrConfigured  bool   `json:"arrConfigured"`
	AllowlistCount int    `json:"allowlistCount"`
}

// SetupStatusResponse is GET /api/setup/status's response — a pure read
// model over what's already configured, driving whether the setup wizard
// shows itself at all and which of its steps it can skip.
type SetupStatusResponse struct {
	Modes              []ModeStatus `json:"modes"`
	JellyfinConfigured bool         `json:"jellyfinConfigured"`
	OllamaConfigured   bool         `json:"ollamaConfigured"`
	Dismissed          bool         `json:"dismissed"`
	AnyConfigured      bool         `json:"anyConfigured"`
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
// Populated for TPDB-sourced items (catalog browse and newest-rows alike —
// see internal/tpdbrest.Scene.Tags/Performers and
// adultnewest.MatchedRelease.Genres/Performers for sourcing); empty for a
// StashDB/FansDB item (that schema's shape hasn't been verified against a
// live instance yet — see CLAUDE.md's "honesty about unverified
// assumptions"). Both omitempty since most callers (any pre-existing cached
// entity, any stash-box item) legitimately have neither.
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
// ConnectionUpsertRequest's doc comment for why.
type ConnectionSummary struct {
	Service   string `json:"service"`
	URL       string `json:"url"`
	Username  string `json:"username,omitempty"`
	HasAPIKey bool   `json:"hasApiKey"`
	KeySuffix string `json:"keySuffix,omitempty"`
	UpdatedAt string `json:"updatedAt"`
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
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
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
}

// AutoGrabCandidate is one graded release in an auto-grab manual-fallback list
// (AutoGrabResponse.Candidates). It pairs the grade (Status = why it did/didn't
// auto-qualify, Score = the bitrate ranking key) with the exact release
// identity the frontend needs to grab it manually via
// POST /api/modes/{mode}/search/grab — one release per click, never a batch.
// Status is one of internal/autograb.Status's values ("qualified",
// "below-floor", "mislabeled", "low-seeders", "unknown-bitrate",
// "unknown-resolution").
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
type AutoGrabResponse struct {
	Grabbed    bool                `json:"grabbed"`
	Fallback   bool                `json:"fallback"`
	Message    string              `json:"message"`
	Grab       *Grab               `json:"grab,omitempty"`
	Candidates []AutoGrabCandidate `json:"candidates,omitempty"`
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
// CURATED subset of internal/proposals.Candidate: only the fields the view
// actually displays (Label/Path/Resolution/Codec/BitRate) plus Winner, the
// keeper-vs-duplicate flag. Winner==true is the "tracked copy" the group keeps;
// every other candidate is a duplicate the Apply removes (see
// internal/dedup.ApplyLibrary*). Wire order is load-bearing: DedupApplyRequest's
// KeepIndex is an ARRAY INDEX into this exact slice (proposals.Proposal.Candidates
// order), so the client MUST render candidates in received order and never sort
// them, or the index it sends resolves to the wrong file. proposals.Candidate's
// TrackedID/PHash are intentionally omitted — the view reads neither, and this
// package curates to what the frontend consumes (see the Proposal doc / package doc).
type Candidate struct {
	Label      string `json:"label"`
	Path       string `json:"path"`
	Resolution int    `json:"resolution"`
	Codec      string `json:"codec"`
	BitRate    int64  `json:"bitRate"`
	Winner     bool   `json:"winner"`
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
	ID                  int64       `json:"id"`
	Status              string      `json:"status"`
	SourceName          string      `json:"sourceName"`
	RootFolderPath      string      `json:"rootFolderPath"`
	Title               string      `json:"title,omitempty"`
	Year                int         `json:"year,omitempty"`
	SeasonNumber        int         `json:"seasonNumber,omitempty"`
	EpisodeNumber       int         `json:"episodeNumber,omitempty"`
	ExtraEpisodeNumbers []int       `json:"extraEpisodeNumbers,omitempty"`
	Studio              string      `json:"studio,omitempty"`
	Date                string      `json:"date,omitempty"`
	PHash               string      `json:"phash,omitempty"`
	Reason              string      `json:"reason,omitempty"`
	DraftID             string      `json:"draftId,omitempty"`
	Candidates          []Candidate `json:"candidates,omitempty"`
}

// --- Purge allowlist (Stage 3) --------------------------------------------
//
// Purge's allowlist is an editable set of tag NAMES; any tracked item whose
// tags match one becomes a delete proposal (see internal/purge's package doc).
// The list itself crosses the wire as a bare JSON array of strings
// (GET /api/modes/{mode}/purge/allowlist → []string), so it needs no named
// response DTO — the client types it as string[] directly. Only the add-body
// warrants a DTO, below. Removal is path-only
// (DELETE /api/modes/{mode}/purge/allowlist/{tag}), no body.
//
// Purge reuses the shared Proposal type above unchanged — its queue rows read
// only Title/Status/RootFolderPath/Reason, all already present. No
// Purge-specific proposal fields exist (no re-pick / give-back / draft), so
// none are added here.

// AllowlistAddRequest is the body of POST /api/modes/{mode}/purge/allowlist —
// adds one tag rule to a mode's Purge allowlist. Mirrors internal/api's
// unexported addAllowlistTagRequest exactly. Adding a tag already present is
// not an error (see allowlist.Store.Add).
type AllowlistAddRequest struct {
	Tag string `json:"tag"`
}

// RepickRequest is the body of POST /api/proposals/{id}/repick — Rename's
// manual-override path when Scan's automatic TMDB match was wrong or scored too
// low to auto-accept (Movies/Series only; Adult identifies via a different id
// space with its own give-back correction). TMDBID and Title are both required
// and carry the NEWLY chosen match from the tmdb-search result, never the
// proposal's current tmdbId; Year is optional (parsed from the result's
// release date when present). Mirrors internal/api's repickProposalRequest.
type RepickRequest struct {
	TMDBID int    `json:"tmdbId"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
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
type DedupApplyRequest struct {
	KeepIndex *int `json:"keepIndex,omitempty"`
	KeepAll   bool `json:"keepAll,omitempty"`
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
// literal 0 can delete the wrong file.
type ApplyBatchItem struct {
	ID        int64 `json:"id"`
	KeepIndex *int  `json:"keepIndex,omitempty"`
	KeepAll   bool  `json:"keepAll,omitempty"`
}

// ApplyBatchRequest is POST /api/proposals/apply-batch's body. Items is capped
// server-side (200); an empty Items is rejected.
type ApplyBatchRequest struct {
	Items []ApplyBatchItem `json:"items"`
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
type TrackedItem struct {
	ID             int64    `json:"id"`
	Title          string   `json:"title"`
	Tags           []string `json:"tags"`
	TmdbId         int      `json:"tmdbId,omitempty"`
	Year           int      `json:"year,omitempty"`
	CollectionName string   `json:"collectionName,omitempty"`
}

// CollectionSummary is one entry from GET /api/modes/movies/collections —
// a TMDB franchise collection with the count of tracked movies belonging to it.
type CollectionSummary struct {
	TMDBCollectionID int    `json:"tmdbCollectionId"`
	Name             string `json:"name"`
	Count            int    `json:"count"`
}

// --- Stage 4: Settings + Advanced Settings ---------------------------------
//
// The DTOs backing the ported Settings view (Connections, API Access, Auth
// mode, AI provider/model, per-mode library/quality/naming/kids, plus the new
// Advanced Settings section: phash-threshold, match-confidence-threshold,
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
type QualityPrefsResponse struct {
	Tier          string `json:"tier"`
	MaxResolution int    `json:"maxResolution"`
	Protocol      string `json:"protocol"`
}

type QualityPrefsRequest struct {
	Tier          string `json:"tier"`
	MaxResolution int    `json:"maxResolution"`
	Protocol      string `json:"protocol"`
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
// similarity cut (per-frame average Hamming bits). Valid range 0–64; the
// frontend mirrors that bound before submitting (backend re-validates).
type PHashThresholdResponse struct {
	Threshold int `json:"threshold"`
}

type PHashThresholdRequest struct {
	Threshold int `json:"threshold"`
}

// ConfidenceThresholdResponse / ConfidenceThresholdRequest back
// GET/PUT /api/modes/{mode}/match-confidence-threshold — the Rename
// match-confidence cut (a 0–100 percentage). The frontend mirrors that bound
// before submitting (backend re-validates).
type ConfidenceThresholdResponse struct {
	Threshold int `json:"threshold"`
}

type ConfidenceThresholdRequest struct {
	Threshold int `json:"threshold"`
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
// "nzbget" | "jellyfin".
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
// "movie" | "tv" | "adult" (a feed belongs to exactly one mode, no "mixed").
// Mirrors Slider's CRUD+reorder DTO shape almost exactly.
type RssFeed struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	FeedURL   string `json:"feedUrl"`
	Target    string `json:"target"`
	Protocol  string `json:"protocol"`
	SortOrder int    `json:"sortOrder"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// RssFeedUpsertRequest is the body of POST /api/discover/rss-feeds (create)
// and PUT /api/discover/rss-feeds/{id} (update) — every editable field,
// mirroring rssfeeds.Store.Create/Update's parameters exactly. Nothing here
// is a secret, so unlike ConnectionUpsertRequest.APIKey every field is a
// plain value, no three-state pointer semantics needed.
type RssFeedUpsertRequest struct {
	Title    string `json:"title"`
	FeedURL  string `json:"feedUrl"`
	Target   string `json:"target"`
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`
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
type RssFeedItem struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	PubDate     string `json:"pubDate"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	DownloadURL string `json:"downloadUrl"`
	Protocol    string `json:"protocol"`
	Indexer     string `json:"indexer"`
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
