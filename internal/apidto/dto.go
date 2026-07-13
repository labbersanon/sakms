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
type AdultDiscoverItem struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Studio          string `json:"studio"`
	Date            string `json:"date"`
	Image           string `json:"image"`
	DurationSeconds int    `json:"durationSeconds"`
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

// AvailabilityResponse is GET /api/modes/{mode}/availability's response —
// backs a Discover card's availability badge (Stage 1 scope: "poster cards
// with availability badges" per the plan). CheckedAt is an RFC3339Nano
// timestamp of when the probe ran.
type AvailabilityResponse struct {
	Available    bool   `json:"available"`
	ReleaseCount int    `json:"releaseCount"`
	CheckedAt    string `json:"checkedAt"`
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
//   - Adult:   Title + Studio (the free-text Prowlarr query, mirroring
//     availability.CheckAdultScene) + DurationSeconds (TPDB's pre-grab
//     runtime → the scorer's RuntimeSeconds; 0 = unknown, handled neutrally).
type AutoGrabRequest struct {
	Title           string `json:"title"`
	TMDBID          int    `json:"tmdbId,omitempty"`
	Studio          string `json:"studio,omitempty"`
	SeasonNumber    int    `json:"seasonNumber,omitempty"`
	EpisodeNumber   int    `json:"episodeNumber,omitempty"`
	SeasonSpecified bool   `json:"seasonSpecified,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
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
// Candidates is Dedup-only: the duplicate group's files, exactly one flagged
// Winner (the keeper); Rename/Purge never populate it (it's absent from their
// wire rows, so the shared TS type carries it as optional).
type Proposal struct {
	ID             int64       `json:"id"`
	Status         string      `json:"status"`
	SourceName     string      `json:"sourceName"`
	RootFolderPath string      `json:"rootFolderPath"`
	Title          string      `json:"title,omitempty"`
	Year           int         `json:"year,omitempty"`
	SeasonNumber   int         `json:"seasonNumber,omitempty"`
	EpisodeNumber  int         `json:"episodeNumber,omitempty"`
	Studio         string      `json:"studio,omitempty"`
	Date           string      `json:"date,omitempty"`
	PHash          string      `json:"phash,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	DraftID        string      `json:"draftId,omitempty"`
	Candidates     []Candidate `json:"candidates,omitempty"`
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
	ID     int64    `json:"id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags"`
	TmdbId int      `json:"tmdbId,omitempty"`
	Year   int      `json:"year,omitempty"`
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
// GET/PUT /api/modes/{mode}/quality-prefs (Movies/Series only — Adult has no
// Search workflow). Tier is one of "low", "medium", "high", "lossless";
// MaxResolution is one of 480/720/1080/2160, or 0 for "no cap".
type QualityPrefsResponse struct {
	Tier          string `json:"tier"`
	MaxResolution int    `json:"maxResolution"`
}

type QualityPrefsRequest struct {
	Tier          string `json:"tier"`
	MaxResolution int    `json:"maxResolution"`
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
