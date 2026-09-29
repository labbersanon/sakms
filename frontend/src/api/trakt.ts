// Trakt data access — backs the Settings "Trakt" connection section (OAuth
// device-code flow) and Discover's Trakt Watchlist row (task #8).
//
// CONFIRMED CONTRACT (2026-07-14): task #5/#9 landed real internal/apidto
// types and internal/api routes matching this file's original proposal
// field-for-field (worker-1 verified dto.gen.ts against this file directly
// before finalizing) — GET /api/trakt/status, PUT /api/trakt/credentials,
// POST /api/trakt/device/start, POST /api/trakt/device/poll,
// POST /api/trakt/disconnect, GET /api/trakt/watchlist. Types now come from
// the generated @dto boundary like every other api/*.ts file, not hand-
// duplicated here.
//
// Three-state secret convention (same as ConnectionUpsertRequest.apiKey /
// Guardrail #5): clientSecret is omitted entirely to preserve the stored
// secret, sent as "" to clear it, non-empty to set it. NEVER send null.

import { api } from "./client";
import type {
  TraktCredentialsRequest,
  TraktDevicePollResponse,
  TraktDeviceStartResponse,
  TraktStatusResponse,
  TraktWatchlistItem,
} from "@dto";

export type {
  TraktCredentialsRequest,
  TraktDevicePollResponse,
  TraktDeviceStartResponse,
  TraktStatusResponse,
  TraktWatchlistItem,
};

// --- Calls -------------------------------------------------------------------

export function fetchTraktStatus(): Promise<TraktStatusResponse> {
  return api<TraktStatusResponse>("/api/trakt/status");
}

// buildTraktCredentialsBody is the three-state secret gate for Trakt's
// client_secret, same shape as settings.ts's buildConnectionUpsertBody — an
// untouched secret field must be OMITTED, never sent as "".
export function buildTraktCredentialsBody(input: {
  clientId: string;
  secretTouched: boolean;
  secretValue: string;
}): TraktCredentialsRequest {
  const body: TraktCredentialsRequest = { clientId: input.clientId };
  if (input.secretTouched) body.clientSecret = input.secretValue;
  return body;
}

export function saveTraktCredentials(
  body: TraktCredentialsRequest,
): Promise<void> {
  return api<void>("/api/trakt/credentials", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function startTraktDeviceFlow(): Promise<TraktDeviceStartResponse> {
  return api<TraktDeviceStartResponse>("/api/trakt/device/start", {
    method: "POST",
  });
}

// pollTraktDevice makes one poll attempt. Callers drive their own interval
// timer (device.interval seconds, per Trakt's guidance) rather than blocking —
// see TraktConnectionSection in Settings.tsx.
export function pollTraktDevice(): Promise<TraktDevicePollResponse> {
  return api<TraktDevicePollResponse>("/api/trakt/device/poll", {
    method: "POST",
  });
}

export function disconnectTrakt(): Promise<void> {
  return api<void>("/api/trakt/disconnect", { method: "POST" });
}

export function fetchTraktWatchlist(): Promise<TraktWatchlistItem[]> {
  return api<TraktWatchlistItem[]>("/api/trakt/watchlist");
}

// Claude 2026-09-28: opt-in unattended ingest of the linked Trakt watchlist.
// Reason: Discover already shows the row; this toggle is the ingest gate.
// Troubleshooting: GET/PUT /api/trakt/watchlist-ingest; seventh retry-cycle pass.
export function fetchTraktWatchlistIngest(): Promise<boolean> {
  return api<{ enabled: boolean }>("/api/trakt/watchlist-ingest").then(
    (r) => r.enabled,
  );
}

export function putTraktWatchlistIngest(enabled: boolean): Promise<void> {
  return api<void>("/api/trakt/watchlist-ingest", {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}
