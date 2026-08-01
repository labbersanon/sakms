// Multi-connection registry data access — the `service_connections` table
// (migration 0053) that replaced the singleton `connections` PRIMARY KEY(service)
// shape for the two service classes an operator can have MORE THAN ONE of:
// Usenet subscriptions (kind "usenet", provider "nntp") and media players
// (kind "player", providers jellyfin/emby/plex).
//
// Mirrors src/api/rssFeeds.ts's shape — CRUD over an id-keyed resource, every
// call through api() (src/api/client.ts) so it inherits the session cookie and
// the global 401 → re-boot session-expiry fallback. Request/response shapes are
// the generated DTOs (@dto), never hand-duplicated.
//
// THE SAFETY-CRITICAL PIECE HERE is buildServiceConnectionBody — the registry's
// twin of api/settings.ts's buildConnectionUpsertBody (Guardrail #5). An update
// that doesn't touch the secret field MUST omit `secret` from the body
// entirely; sending "" is the backend's explicit "clear the stored secret"
// signal and silently wipes a working password/API key. This is a real incident
// class in this project's history — see serviceConnections.test.ts.

import { api } from "./client";
import type {
  ServiceConnectionCreateRequest,
  ServiceConnectionModesRequest,
  ServiceConnectionSummary,
  ServiceConnectionTestRequest,
  ServiceConnectionTestResult,
  ServiceConnectionUpdateRequest,
} from "@dto";
import type { Mode } from "./discover";

export type {
  ServiceConnectionCreateRequest,
  ServiceConnectionSummary,
  ServiceConnectionTestResult,
  ServiceConnectionUpdateRequest,
};

// PLAYER_PROVIDERS is the FIXED three-item provider enum for a media player
// row, mirroring internal/serviceconn's ProviderJellyfin/Emby/Plex. It is
// deliberately not a freeform type field: this registry adds Usenet
// subscriptions and media players and nothing else — a generic "add any
// connection type" framework is an explicit non-goal.
export const PLAYER_PROVIDERS = [
  { value: "jellyfin", label: "Jellyfin" },
  { value: "emby", label: "Emby" },
  { value: "plex", label: "Plex" },
] as const;

export type PlayerProvider = (typeof PLAYER_PROVIDERS)[number]["value"];

// PLAYER_MODES mirrors internal/serviceconn's validModes. Assignment is
// many-to-many with NO exclusivity: 0, 1 or many players per mode are all
// valid, so nothing here warns about a mode that already has a player.
export const PLAYER_MODES = [
  "movies",
  "series",
  "adult",
] as const satisfies readonly Mode[];

// NNTP_DEFAULT_PORT is the conventional NNTP-over-TLS port, used only to seed a
// new subscription's form (which defaults to TLS on). The backend stores
// whatever it is given.
export const NNTP_DEFAULT_PORT = 563;

// DEFAULT_MAX_CONNS mirrors internal/usenet's defaultMaxConnsPerServer — the
// value the pool substitutes when maxConns is left at 0. Shown as help text so
// a blank field reads as "use the default", not as "zero connections".
export const DEFAULT_MAX_CONNS = 4;

// fetchServiceConnections lists every registry row, secret-redacted
// (hasSecret/secretSuffix, never the secret itself), usenet rows then players,
// each already in display order — GET /api/service-connections.
export function fetchServiceConnections(): Promise<ServiceConnectionSummary[]> {
  return api<ServiceConnectionSummary[]>("/api/service-connections");
}

// createServiceConnection adds a row — POST /api/service-connections. `secret`
// is PLAIN here, not three-state: a brand-new row has no stored secret to
// preserve, so absent can only mean "none". `modes` is honoured on create only;
// every later change goes through setServiceConnectionModes.
export function createServiceConnection(
  body: ServiceConnectionCreateRequest,
): Promise<ServiceConnectionSummary> {
  return api<ServiceConnectionSummary>("/api/service-connections", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// updateServiceConnection overwrites every editable field — PUT
// /api/service-connections/{id}. Mode assignment is NOT editable here (the
// backend's Store.Update ignores incoming modes by contract); a row whose mode
// checkboxes changed needs a second call to setServiceConnectionModes, or the
// reassignment silently no-ops.
export function updateServiceConnection(
  id: number,
  body: ServiceConnectionUpdateRequest,
): Promise<ServiceConnectionSummary> {
  return api<ServiceConnectionSummary>(`/api/service-connections/${id}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteServiceConnection(id: number): Promise<void> {
  return api<void>(`/api/service-connections/${id}`, { method: "DELETE" });
}

// setServiceConnectionModes replaces a player's whole mode assignment — PUT
// /api/service-connections/{id}/modes. A full replace, never a per-mode toggle,
// matching serviceconn.Store.SetModes. Rejected by the backend for a usenet row.
export function setServiceConnectionModes(
  id: number,
  modes: string[],
): Promise<ServiceConnectionSummary> {
  const body: ServiceConnectionModesRequest = { modes };
  return api<ServiceConnectionSummary>(`/api/service-connections/${id}/modes`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// testServiceConnection makes one real, read-only call against the field values
// in the body — POST /api/service-connections/test. Nothing is persisted, so
// this is the right test for an unsaved Add form, or for a saved row whose
// secret the operator has just retyped. For a saved row with an UNTOUCHED
// secret use testStoredServiceConnection instead: the client never holds the
// stored secret, so testing here would test a blank one.
export function testServiceConnection(
  body: ServiceConnectionTestRequest,
): Promise<ServiceConnectionTestResult> {
  return api<ServiceConnectionTestResult>("/api/service-connections/test", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// testStoredServiceConnection tests an already-saved row using its STORED
// secret — POST /api/service-connections/{id}/test. A genuine downstream
// failure comes back detail-free by design (a raw client error would echo the
// stored host:port or key); only an unsupported provider is reported verbatim.
export function testStoredServiceConnection(
  id: number,
): Promise<ServiceConnectionTestResult> {
  return api<ServiceConnectionTestResult>(
    `/api/service-connections/${id}/test`,
    { method: "POST" },
  );
}

// buildServiceConnectionBody is the registry's three-state secret gate — the
// direct twin of buildConnectionUpsertBody (api/settings.ts) for
// PUT /api/service-connections/{id}. Its ONLY subtle responsibility is deciding
// whether the `secret` property is present at all:
//
//   - secretTouched === false AND a stored secret exists (hasExistingSecret)
//       → OMIT secret entirely. The server preserves what's stored. The real
//         secret is never sent back to the client (ServiceConnectionSummary
//         redacts it to hasSecret/secretSuffix), so the input is blank for a
//         configured row; sending "" here would WIPE a working password.
//   - secretTouched === true
//       → include secret ("" explicitly clears, non-empty replaces).
//   - no stored secret yet
//       → include secret even if blank, so a no-auth row can still be saved.
//
// Present/absent is expressed by conditionally assigning the property: an unset
// property is dropped by JSON.stringify, which is exactly the "field absent"
// wire state the backend reads as "preserve".
export function buildServiceConnectionBody(input: {
  base: Omit<ServiceConnectionUpdateRequest, "secret">;
  secretTouched: boolean;
  secretValue: string;
  hasExistingSecret: boolean;
}): ServiceConnectionUpdateRequest {
  const body: ServiceConnectionUpdateRequest = { ...input.base };
  if (input.secretTouched || !input.hasExistingSecret) {
    body.secret = input.secretValue;
  }
  return body;
}

// sameModes compares two mode assignments order-independently, so a player save
// only fires the extra /modes request when the checkboxes actually changed.
export function sameModes(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const sortedB = [...b].sort();
  return [...a].sort().every((m, i) => m === sortedB[i]);
}
