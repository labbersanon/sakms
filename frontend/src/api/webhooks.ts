// Webhook subscription data access — CRUD + test-fire for the operator-defined
// outbound webhook subscriptions in Settings → Webhooks. Every call goes
// through api() so it inherits the session cookie and the 401 → re-boot
// session-expiry fallback. Request/response shapes are the generated DTOs
// (@dto), never hand-duplicated.

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
  "purge.applied": "Purge applied",
  "dedup.applied": "Dedup applied",
  "grab.completed": "Grab completed",
};

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
