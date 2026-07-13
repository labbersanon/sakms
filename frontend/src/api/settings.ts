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
// API key (verbatim from index.html) — their key field is a password, and they
// surface a Username input.
export const SERVICES_WITH_USERNAME = ["qbittorrent", "nzbget"];

// CONNECTION_SERVICES is the full ordered set the Connections table lists, one
// row each (verbatim from index.html). There is no radarr/sonarr/whisparr — SAK
// owns those libraries now (see internal/library's package doc).
export const CONNECTION_SERVICES = [
  "prowlarr",
  "qbittorrent",
  "nzbget",
  "tmdb",
  "ollama",
  "openai",
  "gemini",
  "anthropic",
  "stashdb",
  "fansdb",
  "tpdb",
  "brave",
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
