// Settings data access (Stage 4). Ported verbatim from the vanilla-JS
// frontend's renderSettings and everything it calls (internal/web/static/
// index.html: renderConnections, renderAPIAccess, renderAuthMode,
// renderAISettings, renderLibrarySettings, renderQualityPrefs,
// renderNamingPreset, renderKidsRootPath) plus the new Advanced Settings
// section (phash-threshold, match-confidence-threshold, identify-enabled,
// recheck-interval — existing GET/PUT routes with zero prior UI).
//
// Every call goes through api() (src/api/client.ts) so it inherits the session
// cookie and the global 401 → re-boot session-expiry fallback. Request/response
// shapes are the generated DTOs (@dto), never hand-duplicated (Guardrail #4).
//
// THE SINGLE MOST SAFETY-CRITICAL THING IN THIS FILE is buildConnectionUpsertBody
// (Guardrail #5): a connection save that doesn't touch the API-key field MUST
// omit `apiKey` from the request body entirely — never send `""`, which the
// backend reads as "clear the stored secret" and silently wipes a working key.
// This is a real incident class in this project's history. See the function's
// doc comment and its dedicated test (settings.test.tsx).

import { api } from "./client";
import type {
  AIModelRequest,
  AIModelResponse,
  AIProviderRequest,
  AIProviderResponse,
  APIKeyRegenerateResponse,
  APIKeyStatusResponse,
  ApproveNodeRequest,
  AuthModeRequest,
  AuthModeResponse,
  BrowseResponse,
  MatchConfigRequest,
  MatchConfigResponse,
  ConnectionSummary,
  ConnectionTestRequest,
  ConnectionTestResult,
  ConnectionUpsertRequest,
  IdentifyEnabledRequest,
  IdentifyEnabledResponse,
  KidsRootPathRequest,
  KidsRootPathResponse,
  LibraryRootFolderRequest,
  LibraryRootFolderResponse,
  NamingPresetRequest,
  NamingPresetResponse,
  NetscanFinding,
  NetscanHostRequest,
  NetscanProwlarrKeyRequest,
  NetscanProwlarrKeyResponse,
  NodeBrowseResponse,
  NodePathMappingsResponse,
  NodePauseRequest,
  NodeSettingsRequest,
  NodesResponse,
  OIDCConfigRequest,
  OIDCStatusResponse,
  PHashThresholdRequest,
  PHashThresholdResponse,
  QualityPrefsRequest,
  QualityPrefsResponse,
  RecheckIntervalRequest,
  RecheckIntervalResponse,
} from "@dto";
import type { Mode } from "./discover";

export type {
  APIKeyRegenerateResponse,
  APIKeyStatusResponse,
  AuthModeResponse,
  ConnectionSummary,
  ConnectionTestResult,
  ConnectionUpsertRequest,
  NetscanFinding,
  OIDCStatusResponse,
};

// SERVICES_WITH_FIXED_URL are fixed public APIs with one canonical address each,
// hardcoded server-side as package constants (internal/tmdb, internal/stashbox,
// internal/tpdbrest, internal/openai, internal/gemini, internal/anthropic,
// internal/bravesearch) — the operator never types a URL for them. Their rows
// show only an API Key field, and the backend accepts an upsert with no `url`
// for exactly these services (mirrors fixedURLServices in
// internal/api/handler.go). The backend reports the real in-use base URL for
// each of these via ConnectionSummary.fixedUrl (sourced directly from the Go
// package constants, so the frontend never hardcodes and drifts from them),
// and ConnectionRow renders it in a disabled, read-only input. For
// openai/gemini/anthropic/brave that URL supersedes any value the operator
// stored back when it was user-supplied; tmdb/tvdb/stashdb/fansdb/tpdb never
// collected one in the first place.
// Claude 2026-08-04: stashdb/fansdb commented out (Stage 5 Wave 5, §5.2).
// Reason: neither renders a ConnectionRow anymore (see LIBRARY_MODE_SERVICES
// above), so their membership here is read by nothing. Their registry rows
// carry a REAL, operator-editable endpoint field instead of the fixed constant
// this list exists to describe — leaving them here would state the opposite of
// what is now true.
// Review if: a stash-box service ever returns to the singleton connections UI.
export const SERVICES_WITH_FIXED_URL = [
  "tmdb",
  "tvdb",
  // "stashdb",
  // "fansdb",
  "tpdb",
  "openai",
  "gemini",
  "anthropic",
  "brave",
];

// SERVICES_WITH_HOST_LOOKUP are the services the netscan package can identify
// on the LAN, enabling a "look up on a different host" input on their rows.
// jellyfin is deliberately absent: media players moved out of `connections`
// into the multi-connection registry (migration 0053), so their host lookup
// belongs to the registry UI, not to a singleton ConnectionRow.
// ntfy, gotify and node-red are deliberately absent for the same reason: they
// are outbound-webhook targets, not connection types — no serviceconn entry,
// no TestConnection case, so no ConnectionRow is ever built for them and
// membership here would be read by nothing. Their host lookup lives in the
// Add-webhook form (screens/settings/Webhooks.tsx), which surfaces all three
// from one probe rather than one per row.
export const SERVICES_WITH_HOST_LOOKUP = ["prowlarr", "stash"];

// API_SECTION_SERVICES are the global, mode-independent singleton connections,
// rendered by Settings' Advanced -> "API Connections" section.
export const API_SECTION_SERVICES = ["prowlarr", "stash"];

// LIBRARY_MODE_SERVICES are the metadata-source connections that belong to one
// specific mode, rendered inside Settings' Library tab under that mode.
// Claude 2026-08-04: adult trimmed from ["stashdb","fansdb","tpdb"] to
// ["tpdb"] (Stage 5 Wave 5, plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md §5.1, AC2/AC13).
// Reason: StashDB and FansDB are rows of the configurable stash-box registry
// now and render in Settings → Library → Adult's own StashBoxDatabases panel.
// The backend ALSO filters them out of GET /api/connections, so leaving them
// here would render two empty, permanently-unconfigurable ConnectionRows.
// TPDB stays: it is a singleton on a different protocol, with no registry row.
// Troubleshooting: if the Adult metadata-sources table looks empty apart from
// TPDB, that is correct — the stash-box entries moved, they were not lost.
// Review if: TPDB ever gains a stash-box-protocol registry row of its own.
//  adult: ["stashdb", "fansdb", "tpdb"],   // ← was
export const LIBRARY_MODE_SERVICES: Record<Mode, string[]> = {
  movies: ["tmdb"],
  series: ["tvdb"],
  adult: ["tpdb"],
};

// CONNECTION_SERVICES is the full set of SINGLETON services — the ones whose
// `connections` PRIMARY KEY(service) shape is still correct, so there is at most
// one of each. It is the union of the two placement groups above; the groups are
// the source of truth, this is the flat view of them.
//
// There is no radarr/sonarr/whisparr — SAK owns those libraries now (see
// internal/library's package doc). qbittorrent/nzbget were also removed
// (2026-07-18): the unified aria2c downloader replaced them as SAK's download
// engine, so there's no external download-client connection to configure — the
// engine's tunables live in the Downloader settings section instead.
// nntp and jellyfin are gone too (migration 0053): Usenet subscriptions and
// media players are the two classes an operator can have MORE THAN ONE of, so
// they moved to the `service_connections` registry and are configured on the
// Usenet page / the API Connections player list rather than as a singleton row.
// The AI providers (ollama/openai/gemini/anthropic) and Brave web-search
// grounding are deliberately NOT here — they live in the AI tab instead
// (rendered via the same ConnectionRow so their save path stays identical),
// scoped to the currently-selected provider plus the always-visible Brave row.
export const CONNECTION_SERVICES = [
  ...API_SECTION_SERVICES,
  ...LIBRARY_MODE_SERVICES.movies,
  ...LIBRARY_MODE_SERVICES.series,
  ...LIBRARY_MODE_SERVICES.adult,
];

// ADULT_ONLY_CONNECTION_SERVICES are the 4 CONNECTION_SERVICES entries that
// are exclusively Adult-related: `stash` for phash-first identification, the
// other three for the stash-box identification network (StashDB/FansDB/TPDB).
// Settings -> Advanced -> API Connections (APISection.tsx) filters these out
// of the rendered table when the global adult_mode_enabled switch (see
// fetchAdultModeEnabled below) is off — see ralplan-adult-disable-switch.md
// step 9.
// Claude 2026-08-04: stashdb/fansdb commented out (Stage 5 Wave 5, §5.1).
// Reason: this list exists so APISection can HIDE adult-only rows when
// adult_mode_enabled is off. Those two no longer produce a row anywhere in the
// connections UI, so there is nothing left for their entries to hide.
// Troubleshooting: hiding the registry panel itself is not this list's job —
// ModeSelector omits the Adult mode entirely when the switch is off, which
// takes the whole panel with it.
export const ADULT_ONLY_CONNECTION_SERVICES = [
  // "stashdb",
  // "fansdb",
  "tpdb",
  "stash",
];

export const AI_PROVIDERS = ["ollama", "openai", "gemini", "anthropic"];
export const QUALITY_TIERS = ["low", "medium", "high", "lossless"];
export const MAX_RESOLUTIONS = [0, 480, 720, 1080, 2160];
export const NAMING_PRESETS = [
  { value: "jellyfin", label: "Jellyfin/Emby standard (default)" },
  { value: "legacy", label: "Legacy (SAK's original convention)" },
];

// AI_PROVIDER_MODELS is a curated, deliberately non-exhaustive list of common
// current models per cloud provider — the model <select> in AI.tsx falls back
// to an "Other" custom text entry for anything not listed here, so this never
// needs to track every vendor release. Ollama has no entry: its model list is
// live-fetched from the instance itself (see fetchOllamaModels below).
export const AI_PROVIDER_MODELS: Record<
  "openai" | "gemini" | "anthropic",
  { value: string; label: string }[]
> = {
  openai: [
    { value: "gpt-4o", label: "GPT-4o" },
    { value: "gpt-4o-mini", label: "GPT-4o mini" },
    { value: "gpt-4.1", label: "GPT-4.1" },
    { value: "gpt-4.1-mini", label: "GPT-4.1 mini" },
    { value: "o3", label: "o3" },
    { value: "o3-mini", label: "o3-mini" },
  ],
  gemini: [
    { value: "gemini-2.5-pro", label: "Gemini 2.5 Pro" },
    { value: "gemini-2.5-flash", label: "Gemini 2.5 Flash" },
    { value: "gemini-2.0-flash", label: "Gemini 2.0 Flash" },
    { value: "gemini-1.5-pro", label: "Gemini 1.5 Pro" },
  ],
  anthropic: [
    { value: "claude-opus-4-8", label: "Claude Opus 4.8" },
    { value: "claude-sonnet-5", label: "Claude Sonnet 5" },
    { value: "claude-haiku-4-5-20251001", label: "Claude Haiku 4.5" },
  ],
};

// API_KEY_HELP_URLS points each API-key-bearing AI/search service at its
// vendor's key-management page, for the "Get API key" link ConnectionRow
// renders next to the key field (ConnectionRow.tsx, no new prop — it reads
// this map directly keyed on props.service).
export const API_KEY_HELP_URLS: Record<string, string> = {
  openai: "https://platform.openai.com/api-keys",
  gemini: "https://aistudio.google.com/app/apikey",
  anthropic: "https://console.anthropic.com/settings/keys",
  brave: "https://brave.com/search/api/",
};

// --- Connections -----------------------------------------------------------

export function fetchConnections(): Promise<ConnectionSummary[]> {
  return api<ConnectionSummary[]>("/api/connections");
}

// buildConnectionUpsertBody is the three-state secret gate (Guardrail #5),
// ported verbatim from index.html's buildConnectionControls.requestBody(). It
// returns the exact PUT /api/connections/{service} body, and its ONLY subtle
// responsibility is deciding whether the `apiKey` property is present at all:
//
//   - keyTouched === false AND a stored key exists (hasExistingKey)
//       → OMIT apiKey entirely. The server preserves the stored secret. The
//         real key is never sent to the client (ConnectionSummary redacts it to
//         hasApiKey/keySuffix), so the key input is blank for a configured
//         connection; sending "" here would WIPE the working key.
//   - keyTouched === true
//       → include apiKey = keyValue ("" explicitly clears; non-empty sets).
//   - no stored key yet (first-time save)
//       → include apiKey = keyValue even if blank, so a no-key service (e.g.
//         Ollama) can still be saved.
//
// Present/absent is expressed by conditionally assigning the property: an unset
// property is dropped by JSON.stringify, which is exactly the "field absent"
// wire state the backend reads as "preserve".
export function buildConnectionUpsertBody(input: {
  url: string;
  username?: string;
  needsUsername: boolean;
  keyTouched: boolean;
  keyValue: string;
  hasExistingKey: boolean;
}): ConnectionUpsertRequest {
  const body: ConnectionUpsertRequest = { url: input.url };
  if (input.needsUsername) body.username = input.username ?? "";
  if (input.keyTouched || !input.hasExistingKey) {
    body.apiKey = input.keyValue;
  }
  return body;
}

export function upsertConnection(
  service: string,
  body: ConnectionUpsertRequest,
): Promise<void> {
  return api<void>(`/api/connections/${service}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteConnection(service: string): Promise<void> {
  return api<void>(`/api/connections/${service}`, { method: "DELETE" });
}

export function testConnection(
  req: ConnectionTestRequest,
): Promise<ConnectionTestResult> {
  return api<ConnectionTestResult>("/api/connections/test", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// testConnectionStored tests the SAVED connection for a service (no request
// body) — distinct from testConnection above, which tests the in-progress,
// possibly-unsaved field values. On failure the backend returns the fixed
// string "connection test failed" (never the raw downstream error, to avoid
// leaking the stored URL/key), so callers must treat `ok` as a boolean signal
// only and not surface `error` to the operator. 404 (no saved connection)
// throws.
export function testConnectionStored(
  service: string,
): Promise<ConnectionTestResult> {
  return api<ConnectionTestResult>(
    `/api/connections/${service}/test-stored`,
    { method: "POST" },
  );
}

// --- Netscan (LAN-discovery hints; relocated into the Settings connections
// table from the old setup wizard — the task's buildNetscanHint equivalent) ---

export function fetchNetscanKnown(): Promise<NetscanFinding[]> {
  return api<NetscanFinding[]>("/api/netscan/known");
}

export function probeNetscanHost(host: string): Promise<NetscanFinding[]> {
  const body: NetscanHostRequest = { host };
  return api<NetscanFinding[]>("/api/netscan/host", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchProwlarrKey(url: string): Promise<string> {
  const body: NetscanProwlarrKeyRequest = { url };
  return api<NetscanProwlarrKeyResponse>("/api/netscan/prowlarr-key", {
    method: "POST",
    body: JSON.stringify(body),
  }).then((r) => r.apiKey);
}

// --- Folder browse (root-folder picker autocomplete) -----------------------

// fetchBrowse lists the subdirectories of a path for the Settings root-folder
// pickers. An empty path is valid — the backend returns the fixed set of
// browsable roots. A resolved-but-nonexistent path returns 200 with no
// entries (graceful degradation while the operator types), never an error.
export function fetchBrowse(path: string): Promise<BrowseResponse> {
  return api<BrowseResponse>(`/api/browse?path=${encodeURIComponent(path)}`);
}

// --- API Access (break-glass key) ------------------------------------------

export function fetchAPIKeyStatus(): Promise<APIKeyStatusResponse> {
  return api<APIKeyStatusResponse>("/api/apikey");
}

export function regenerateAPIKey(): Promise<APIKeyRegenerateResponse> {
  return api<APIKeyRegenerateResponse>("/api/apikey/regenerate", {
    method: "POST",
  });
}

// --- Auth mode + OIDC config -----------------------------------------------

export function fetchAuthMode(): Promise<AuthModeResponse> {
  return api<AuthModeResponse>("/api/auth/mode");
}

// putAuthMode switches the already-authenticated operator's active auth mode.
// Preconditions (password needs an existing hash, oidc needs saved config) are
// enforced SERVER-SIDE and surface as the thrown error — the client never
// re-implements them (verbatim from renderAuthMode). Only "none" carries a
// client-side gate: acknowledgeInsecure must be true, set after an explicit
// confirm in the component.
//
// sectionPin carries the section PIN lock's §4.4 requirement: this route is the
// lock's own disarm surface (switching to "none" makes the lock inert), so the
// backend refuses it whenever a section PIN is set and none is presented. It is
// a HEADER rather than a body field because AuthModeRequest is a generated DTO
// with a fixed shape — the same reason OidcLogin.tsx's pre-session recovery
// form sends it as one.
//
// Omitted entirely when blank: an empty X-Section-Pin is not a WRONG pin and
// must never count as a failed attempt against the brute-force counter (the
// backend treats "" as ErrNoPinPresented, which deliberately does not increment
// it). Content-Type is restated because api()'s options merge is shallow —
// passing headers at all replaces the default object.
export function putAuthMode(
  body: AuthModeRequest,
  sectionPin?: string,
): Promise<void> {
  const pin = (sectionPin || "").trim();
  return api<void>("/api/auth/mode", {
    method: "PUT",
    body: JSON.stringify(body),
    ...(pin
      ? {
          headers: {
            "Content-Type": "application/json",
            "X-Section-Pin": pin,
          },
        }
      : {}),
  });
}

export function fetchOIDCStatus(): Promise<OIDCStatusResponse> {
  return api<OIDCStatusResponse>("/api/auth/oidc");
}

export function putOIDCConfig(body: OIDCConfigRequest): Promise<void> {
  return api<void>("/api/auth/oidc", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// --- AI provider + model ---------------------------------------------------

export function fetchAIProvider(): Promise<string> {
  return api<AIProviderResponse>("/api/settings/ai-provider").then(
    (r) => r.provider,
  );
}

export function putAIProvider(provider: string): Promise<void> {
  const body: AIProviderRequest = { provider };
  return api<void>("/api/settings/ai-provider", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function fetchAIModel(): Promise<string> {
  return api<AIModelResponse>("/api/settings/ai-model").then((r) => r.model);
}

export function putAIModel(model: string): Promise<void> {
  const body: AIModelRequest = { model };
  return api<void>("/api/settings/ai-model", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// fetchOllamaModels lists the models actually installed on a given Ollama
// instance (backend calls that instance's /api/tags), for the model <select>
// in AI.tsx when the ollama provider is active. Callers should source `url`
// from the SAVED ollama connection, not an in-progress edit (see plan ADR).
// Rejects cleanly (via api()'s non-ok throw) on an unreachable/bad URL so
// callers can render an inline error instead of a blank dropdown.
export function fetchOllamaModels(url: string): Promise<string[]> {
  return api<string[]>(
    `/api/ollama/models?url=${encodeURIComponent(url)}`,
  );
}

// --- Per-mode: library root folder / quality / naming / kids ----------------

export function fetchLibraryRootFolder(mode: Mode): Promise<string> {
  return api<LibraryRootFolderResponse>(
    `/api/modes/${mode}/library/root-folder`,
  ).then((r) => r.path);
}

export function putLibraryRootFolder(mode: Mode, path: string): Promise<void> {
  const body: LibraryRootFolderRequest = { path };
  return api<void>(`/api/modes/${mode}/library/root-folder`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// testLibraryRootFolder checks whether a free-typed path is usable as a root
// folder (exists, is a directory, is writable) WITHOUT saving it. Unlike
// testConnectionStored, this endpoint's `error` is a safe, human-readable
// string ("path does not exist", "path is not writable", …) — fine to show the
// operator directly. `{mode}` is accepted but ignored server-side (any path is
// tested as-is, with no browsable-roots confinement).
export function testLibraryRootFolder(
  mode: Mode,
  path: string,
): Promise<{ ok: boolean; error?: string }> {
  return api<{ ok: boolean; error?: string }>(
    `/api/modes/${mode}/library/root-folder/test`,
    { method: "POST", body: JSON.stringify({ path }) },
  );
}

export function fetchQualityPrefs(mode: Mode): Promise<QualityPrefsResponse> {
  return api<QualityPrefsResponse>(`/api/modes/${mode}/quality-prefs`);
}

export function putQualityPrefs(
  mode: Mode,
  prefs: QualityPrefsRequest,
): Promise<void> {
  return api<void>(`/api/modes/${mode}/quality-prefs`, {
    method: "PUT",
    body: JSON.stringify(prefs),
  });
}

export function fetchNamingPreset(mode: Mode): Promise<string> {
  return api<NamingPresetResponse>(`/api/modes/${mode}/naming-preset`).then(
    (r) => r.preset,
  );
}

export function putNamingPreset(mode: Mode, preset: string): Promise<void> {
  const body: NamingPresetRequest = { preset };
  return api<void>(`/api/modes/${mode}/naming-preset`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function fetchKidsRootPath(mode: Mode): Promise<string> {
  return api<KidsRootPathResponse>(
    `/api/modes/${mode}/rename/kids-root-path`,
  ).then((r) => r.path);
}

export function putKidsRootPath(mode: Mode, path: string): Promise<void> {
  const body: KidsRootPathRequest = { path };
  return api<void>(`/api/modes/${mode}/rename/kids-root-path`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// --- Advanced Settings (new UI over existing routes) ------------------------

// Per-mode Dedup perceptual-hash similarity threshold (0–256, backend-validated).
export function fetchPHashThreshold(mode: Mode): Promise<number> {
  return api<PHashThresholdResponse>(`/api/modes/${mode}/phash-threshold`).then(
    (r) => r.threshold,
  );
}

export function putPHashThreshold(
  mode: Mode,
  threshold: number,
): Promise<void> {
  const body: PHashThresholdRequest = { threshold };
  return api<void>(`/api/modes/${mode}/phash-threshold`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// Per-mode Rename drilldown match config (candidate N + duration tolerance %).
export function fetchMatchConfig(mode: Mode): Promise<MatchConfigResponse> {
  return api<MatchConfigResponse>(`/api/modes/${mode}/rename-match-config`);
}

export function putMatchConfig(
  mode: Mode,
  candidateN: number,
  durationTolerancePct: number,
): Promise<void> {
  const body: MatchConfigRequest = { candidateN, durationTolerancePct };
  return api<void>(`/api/modes/${mode}/rename-match-config`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// Adult-only phash-first identify toggle. The endpoint 400s for movies/series,
// so the component only calls this in the Adult context.
export function fetchIdentifyEnabled(mode: Mode): Promise<boolean> {
  return api<IdentifyEnabledResponse>(
    `/api/modes/${mode}/identify-enabled`,
  ).then((r) => r.enabled);
}

export function putIdentifyEnabled(
  mode: Mode,
  enabled: boolean,
): Promise<void> {
  const body: IdentifyEnabledRequest = { enabled };
  return api<void>(`/api/modes/${mode}/identify-enabled`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// Global (not per-mode) background recheck cadence in whole seconds (>= 0,
// backend-validated; 0 = off).
export function fetchRecheckInterval(): Promise<number> {
  return api<RecheckIntervalResponse>("/api/settings/recheck-interval").then(
    (r) => r.intervalSeconds,
  );
}

export function putRecheckInterval(intervalSeconds: number): Promise<void> {
  const body: RecheckIntervalRequest = { intervalSeconds };
  return api<void>("/api/settings/recheck-interval", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// Manual "Refresh now" trigger for the recheck job — runs one pass over
// every watched title immediately, regardless of the configured interval.
// Fires in the background server-side; the request returns as soon as it's
// accepted (202), not once the refresh finishes, so there's nothing to poll
// — same fire-and-forget contract as triggerEntitySync.
export function triggerRecheck(): Promise<void> {
  return api<void>("/api/admin/recheck/trigger", { method: "POST" });
}

// Claude 2026-08-03: added fetchDiscoverRefreshInterval/putDiscoverRefreshInterval/
// triggerDiscoverRefresh (FE-1, discover-scheduled-refresh plan §6.2-6.3).
// Reason: mirrors fetchRecheckInterval/putRecheckInterval/triggerRecheck above
// exactly — same unwrap-the-envelope shape as adult-newest-scan-interval
// below, since this endpoint also has no generated DTO (internal/api's
// discoverRefreshIntervalResponse/Request are local structs, not
// internal/apidto types — plan §6.2 Critic finding M2). Global (not
// per-mode) Discover background-refresh cadence in whole seconds (>= 0,
// backend-validated; 0 = off — clears the Discover cache and returns it to
// fetching live, NOT this scheduler's default; the default is 86400/24h,
// unlike recheck-interval's off-by-default 0 — see internal/api/
// discover_refresh.go's discoverRefreshDefaultSeconds).
// Review if: a generated DTO is ever added for this endpoint.
export function fetchDiscoverRefreshInterval(): Promise<number> {
  return api<{ intervalSeconds: number }>(
    "/api/settings/discover-refresh-interval",
  ).then((r) => r.intervalSeconds);
}

export function putDiscoverRefreshInterval(
  intervalSeconds: number,
): Promise<void> {
  return api<void>("/api/settings/discover-refresh-interval", {
    method: "PUT",
    body: JSON.stringify({ intervalSeconds }),
  });
}

// Manual "Refresh now" trigger for the Discover background refresh — same
// fire-and-forget contract as triggerRecheck above (202 once the cycle
// STARTS, not once it finishes; nothing to poll afterward). Unlike
// triggerRecheck, this endpoint can also answer 409 when a cycle is already
// running (internal/api/discover_refresh_trigger.go) — callers should
// branch on `e instanceof ApiError && e.status === 409` (see
// isRebuildRefused in api/torrent.ts for the same by-status, never
// by-message-text pattern), not on the thrown message text.
export function triggerDiscoverRefresh(): Promise<void> {
  return api<void>("/api/admin/discover-refresh/trigger", { method: "POST" });
}

// BYOAI fallback toggle (off by default — DB-first parsing runs alone).
export function fetchAIFallbackEnabled(): Promise<boolean> {
  return api<{ enabled: boolean }>("/api/settings/ai-fallback-enabled").then(
    (r) => r.enabled,
  );
}

export function putAIFallbackEnabled(enabled: boolean): Promise<void> {
  return api<void>("/api/settings/ai-fallback-enabled", {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}

// Claude 2026-08-05: web search primary (searxng|brave)
export type WebSearchPrimary = "searxng" | "brave";

export function fetchWebSearchPrimary(): Promise<WebSearchPrimary> {
  return api<{ primary: string } | null>("/api/settings/web-search-primary").then(
    (r) => (r?.primary === "brave" ? "brave" : "searxng"),
  );
}

export function putWebSearchPrimary(primary: WebSearchPrimary): Promise<void> {
  return api<void>("/api/settings/web-search-primary", {
    method: "PUT",
    body: JSON.stringify({ primary }),
  });
}

// Entity cache admin (Phase 6).
export type EntitySyncSource = "stash" | "tpdb" | "stashdb" | "fansdb";

export interface EntitySyncSourceStatus {
  source: EntitySyncSource;
  syncedAt: string;
  cursor: string;
}

export interface EntitySyncStatus {
  studioCount: number;
  performerCount: number;
  sources: EntitySyncSourceStatus[];
}

export function fetchEntitySyncStatus(): Promise<EntitySyncStatus> {
  return api<EntitySyncStatus>("/api/admin/entity-sync");
}

export function triggerEntitySync(source: EntitySyncSource): Promise<void> {
  return api<void>(`/api/admin/entity-sync/${source}`, { method: "POST" });
}

// Shared background sync cadence for all four entity sources combined, in
// whole seconds (>= 0, backend-validated; 0 = off, the default — entity sync
// was purely manual before this job existed). No generated DTO, same as
// adult-newest-scan-interval below — the Go handler uses local structs.
export function fetchEntitySyncInterval(): Promise<number> {
  return api<{ intervalSeconds: number }>(
    "/api/settings/entity-sync-interval",
  ).then((r) => r.intervalSeconds);
}

export function putEntitySyncInterval(intervalSeconds: number): Promise<void> {
  return api<void>("/api/settings/entity-sync-interval", {
    method: "PUT",
    body: JSON.stringify({ intervalSeconds }),
  });
}

// Global background Adult "newest" scan cadence in whole seconds (>= 0,
// backend-validated; 0 = off, opt-in). Same shape/semantics as recheck-interval
// above, but this endpoint has no generated DTO (the Go handler uses local
// request/response structs), so the wire shape is inlined here.
export function fetchAdultNewestScanInterval(): Promise<number> {
  return api<{ intervalSeconds: number }>(
    "/api/settings/adult-newest-scan-interval",
  ).then((r) => r.intervalSeconds);
}

export function putAdultNewestScanInterval(
  intervalSeconds: number,
): Promise<void> {
  return api<void>("/api/settings/adult-newest-scan-interval", {
    method: "PUT",
    body: JSON.stringify({ intervalSeconds }),
  });
}

// Adult mode enabled: the global visibility switch (adult_mode_enabled) that
// hides Adult-related frontend UI when off — NOT a backend access-control
// boundary, see ralplan-adult-disable-switch.md. Same single-key GET/PUT
// shape as watch-folders-enabled directly below. The GET's resolved default
// (when never explicitly set) is computed server-side from whether Adult's
// library root folder is configured; the frontend just reads/writes the
// resolved boolean.
export function fetchAdultModeEnabled(): Promise<boolean> {
  return api<{ enabled: boolean }>("/api/settings/adult-mode-enabled").then(
    (r) => r.enabled,
  );
}

export function putAdultModeEnabled(enabled: boolean): Promise<void> {
  return api<void>("/api/settings/adult-mode-enabled", {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}

// Watch-folders: enabled toggle + currently configured root paths. The
// backend goroutine polls WatchFoldersEnabledKey every ~30s, so a change
// takes effect within 30 seconds without a restart.
export type WatchFoldersStatus = {
  enabled: boolean;
  roots: Record<string, string>; // mode → root path (only configured roots)
};

export function fetchWatchFolders(): Promise<WatchFoldersStatus> {
  return api<WatchFoldersStatus>("/api/admin/watch-folders");
}

export function putWatchFoldersEnabled(enabled: boolean): Promise<void> {
  return api<void>("/api/admin/watch-folders/enabled", {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}

// Global watch-folders config-poll cadence in whole seconds (>= 0,
// backend-validated; 0 = unset, meaning "use the 30s default" — NOT "off";
// the feature's own on/off is the separate "Watch folders enabled" checkbox
// above). Same shape/semantics as entity-sync-interval/adult-newest-scan-interval
// above; no generated DTO, wire shape inlined here.
export function fetchWatchFoldersPollInterval(): Promise<number> {
  return api<{ intervalSeconds: number }>(
    "/api/settings/watch-folders-poll-interval",
  ).then((r) => r.intervalSeconds);
}

export function putWatchFoldersPollInterval(
  intervalSeconds: number,
): Promise<void> {
  return api<void>("/api/settings/watch-folders-poll-interval", {
    method: "PUT",
    body: JSON.stringify({ intervalSeconds }),
  });
}

// --- Worker nodes ----------------------------------------------------------

export function fetchNodes(): Promise<NodesResponse> {
  return api<NodesResponse>("/api/nodes");
}

export function approveNode(id: string, body: ApproveNodeRequest): Promise<void> {
  return api<void>(`/api/nodes/${id}/approve`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function rejectPending(id: string): Promise<void> {
  return api<void>(`/api/nodes/${id}/pending`, {
    method: "DELETE",
  });
}

// updateNodeSettings issues the operator-authed PUT /api/nodes/{id}/settings.
// This path writes ONLY maxJobs — the backend ignores any PathMap in an
// operator-auth request (path mappings are node-owned; only the node itself
// can author them via its own bearer-authed push). Callers must still send
// `pathMap: []` to satisfy NodeSettingsRequest's shape, but must never
// populate it from operator-editable state — see Nodes.tsx's
// EditSettingsModal, the only caller.
export function updateNodeSettings(id: string, body: NodeSettingsRequest): Promise<void> {
  return api<void>(`/api/nodes/${id}/settings`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// updateNodePause issues the dual-authed PUT /api/nodes/{id}/pause. This is a
// SEPARATE call from updateNodeSettings on purpose (P2, node-pause-dispatch
// plan Stage 4): the request body carries ONLY {paused}, never maxJobs or
// pathMap, so the pause toggle can never travel in the same request as a
// MaxJobs save. Do not fold this into updateNodeSettings.
export function updateNodePause(id: string, paused: boolean): Promise<void> {
  const body: NodePauseRequest = { paused };
  return api<void>(`/api/nodes/${id}/pause`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// fetchNodePathMappings is read-only: it always returns the fixed 5 rows
// (one per library root-folder setting), each with its current server-side
// value (for the label) and this node's persisted NodePath, if any was ever
// saved. Works for a not-yet-approved pending node's id too (nothing has been
// persisted for it, so every NodePath comes back blank) — labels/Configured
// come from Library settings, not from node approval/connection state.
export function fetchNodePathMappings(id: string): Promise<NodePathMappingsResponse> {
  return api<NodePathMappingsResponse>(`/api/nodes/${id}/path-mappings`);
}

// fetchNodeBrowse lists the subdirectories of a path on a specific connected
// node's own filesystem (not the server's) — only usable for an already-
// approved, currently-connected node. Throws with a clear message (surfaced
// by the caller) when the node isn't connected or doesn't answer in time.
export function fetchNodeBrowse(id: string, path: string): Promise<NodeBrowseResponse> {
  return api<NodeBrowseResponse>(
    `/api/nodes/${id}/browse?path=${encodeURIComponent(path)}`,
  );
}
