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
  AuthModeRequest,
  AuthModeResponse,
  BrowseResponse,
  ConfidenceThresholdRequest,
  ConfidenceThresholdResponse,
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

// SERVICES_WITH_USERNAME authenticate with username+password rather than a bare
// API key — their key field is a password, and they surface a Username input.
export const SERVICES_WITH_USERNAME: string[] = ["nntp"];

// SERVICES_WITH_FIXED_URL are fixed public APIs with one canonical address each,
// hardcoded server-side as package constants (internal/tmdb, internal/stashbox,
// internal/tpdbrest) — the operator never types a URL for them. Their rows show
// only an API Key field, and the backend accepts an upsert with no `url` for
// exactly these services (mirrors fixedURLServices in internal/api/handler.go).
export const SERVICES_WITH_FIXED_URL = ["tmdb", "tvdb", "stashdb", "fansdb", "tpdb"];

// SERVICES_WITH_HOST_LOOKUP are the services the netscan package can identify
// on the LAN, enabling a "look up on a different host" input on their rows.
export const SERVICES_WITH_HOST_LOOKUP = ["prowlarr", "jellyfin", "stash"];

// CONNECTION_SERVICES is the full ordered set the Connections table lists, one
// row each. There is no radarr/sonarr/whisparr — SAK owns those libraries now
// (see internal/library's package doc). qbittorrent/nzbget were also removed
// (2026-07-18): the unified aria2c downloader replaced them as SAK's download
// engine, so there's no external download-client connection to configure — the
// engine's tunables live in the Downloader settings section instead.
// The AI providers (ollama/openai/gemini/anthropic) and Brave web-search
// grounding are deliberately NOT here — they live in the AI tab instead
// (rendered via the same ConnectionRow so their save path stays identical),
// scoped to the currently-selected provider plus the always-visible Brave row.
export const CONNECTION_SERVICES = [
  "prowlarr",
  "nntp",
  "tmdb",
  "tvdb",
  "stashdb",
  "fansdb",
  "tpdb",
  "stash",
  "jellyfin",
];

export const AI_PROVIDERS = ["ollama", "openai", "gemini", "anthropic"];
export const QUALITY_TIERS = ["low", "medium", "high", "lossless"];
export const MAX_RESOLUTIONS = [0, 480, 720, 1080, 2160];
export const NAMING_PRESETS = [
  { value: "jellyfin", label: "Jellyfin/Emby standard (default)" },
  { value: "legacy", label: "Legacy (SAK's original convention)" },
];

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
export function putAuthMode(body: AuthModeRequest): Promise<void> {
  return api<void>("/api/auth/mode", {
    method: "PUT",
    body: JSON.stringify(body),
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

// Per-mode Dedup perceptual-hash similarity threshold (0–64, backend-validated).
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

// Per-mode Rename match-confidence threshold (0–100 percentage, backend-validated).
export function fetchConfidenceThreshold(mode: Mode): Promise<number> {
  return api<ConfidenceThresholdResponse>(
    `/api/modes/${mode}/match-confidence-threshold`,
  ).then((r) => r.threshold);
}

export function putConfidenceThreshold(
  mode: Mode,
  threshold: number,
): Promise<void> {
  const body: ConfidenceThresholdRequest = { threshold };
  return api<void>(`/api/modes/${mode}/match-confidence-threshold`, {
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

// --- Worker nodes ----------------------------------------------------------

export function fetchNodes(): Promise<NodesResponse> {
  return api<NodesResponse>("/api/nodes");
}
