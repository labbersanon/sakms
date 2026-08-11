// Webhook subscription data access — CRUD + test-fire for the operator-defined
// outbound webhook subscriptions in Settings → Webhooks. Every call goes
// through api() so it inherits the session cookie and the 401 → re-boot
// session-expiry fallback. Request/response shapes are the generated DTOs
// (@dto), never hand-duplicated.

import { createSignal } from "solid-js";
import { api } from "./client";
import type {
  WebhookCreateRequest,
  WebhookSummary,
  WebhookUpdateRequest,
} from "@dto";

export type { WebhookCreateRequest, WebhookSummary, WebhookUpdateRequest };

// ALL_WEBHOOK_EVENTS mirrors internal/webhooks.AllEvents exactly.
export const ALL_WEBHOOK_EVENTS = [
  "rename.applied",
  "purge.applied",
  "dedup.applied",
  "grab.completed",
] as const;
export type WebhookEvent = (typeof ALL_WEBHOOK_EVENTS)[number];

export const WEBHOOK_EVENT_LABELS: Record<WebhookEvent, string> = {
  "rename.applied": "Rename applied",
  "purge.applied": "Clean-up applied",
  "dedup.applied": "Dedup applied",
  "grab.completed": "Grab completed",
};

// WEBHOOK_DISCOVERY_SERVICES are the netscan service ids that are plausible
// outbound-webhook TARGETS, as opposed to the media services netscan also
// probes. These strings must match the `name` field of the corresponding
// entries in internal/netscan.knownServices EXACTLY — Finding.service is an
// untyped string on both sides, so a mismatch silently renders no hint at all
// rather than failing loudly. Same mirror-a-Go-list convention as
// ALL_WEBHOOK_EVENTS above, with ONE deliberate difference: NO `as const`
// here. A readonly literal tuple's .includes() only accepts the literal
// union, so `as const` would break `.includes(f.service)` where f.service is
// a plain string.
export const WEBHOOK_DISCOVERY_SERVICES = ["ntfy", "gotify", "node-red"];

export function fetchWebhooks(): Promise<WebhookSummary[]> {
  return api<WebhookSummary[]>("/api/webhooks");
}

export function createWebhook(body: WebhookCreateRequest): Promise<WebhookSummary> {
  return api<WebhookSummary>("/api/webhooks", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function updateWebhook(
  id: number,
  body: WebhookUpdateRequest,
): Promise<void> {
  return api<void>(`/api/webhooks/${id}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteWebhook(id: number): Promise<void> {
  return api<void>(`/api/webhooks/${id}`, { method: "DELETE" });
}

export function testWebhook(id: number): Promise<void> {
  return api<void>(`/api/webhooks/${id}/test`, { method: "POST" });
}

// --- Browser (desktop) notifications preference ----------------------------
// The opt-in "Enable browser notifications" preference (off by default),
// mirroring the ai_fallback_enabled boolean-preference pattern in settings.ts.

export function fetchBrowserNotificationsEnabled(): Promise<boolean> {
  return api<{ enabled: boolean }>(
    "/api/settings/browser-notifications-enabled",
  ).then((r) => r.enabled);
}

export function putBrowserNotificationsEnabled(enabled: boolean): Promise<void> {
  return api<void>("/api/settings/browser-notifications-enabled", {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}

// browserNotificationsEnabled is genuinely SHARED module-level state, not a
// per-component signal: the Settings toggle (Webhooks.tsx, mounted inside a
// swapped <Route>) and BrowserNotifications (mounted in AppShell's persistent
// ShellRoot) live in disjoint component subtrees and must react to each other
// without a page reload. Both read/write this one signal — a toggle flip is
// immediately visible to the shell-mounted component, which opens/closes its
// EventSource accordingly.
export const [browserNotificationsEnabled, setBrowserNotificationsEnabled] =
  createSignal(false);

let prefLoaded = false;

// loadBrowserNotificationsEnabled seeds the shared signal from the server once,
// on first use. Idempotent (guarded) so multiple readers can call it without
// racing duplicate fetches or clobbering a later user toggle. A failed fetch
// leaves the default (false) in place — a settings read error must not break
// the shell.
export async function loadBrowserNotificationsEnabled(): Promise<void> {
  if (prefLoaded) return;
  prefLoaded = true;
  try {
    setBrowserNotificationsEnabled(await fetchBrowserNotificationsEnabled());
  } catch {
    /* keep the default (false); the toggle can still be flipped manually */
  }
}
