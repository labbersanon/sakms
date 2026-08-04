// Claude 2026-08-04: new file — client for the admin-configurable stash-box
// database registry (Stage 5 Wave 4.1, plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md §4.1). Mirrors
// rssFeeds.ts's CRUD+reorder shape (a different backend resource, same idiom:
// every call goes through api() so it inherits the session cookie and the
// global 401 fallback; request/response shapes are the generated DTOs).
// Reason: keeps this file's surface obvious against its backend twin
// (internal/api/stashboxdb.go) rather than inventing a new client shape.
// Troubleshooting: buildStashBoxDatabaseKeyPatch below is the ONLY place this
// file touches the three-state secret rule — it delegates to
// buildConnectionUpsertBody (settings.ts) rather than re-deriving the
// omit/clear/set logic, per the plan's explicit instruction to reuse it.
// Review if: StashBoxDatabaseUpdateRequest's shape changes upstream.

import { api } from "./client";
import { buildConnectionUpsertBody } from "./settings";
import type {
  ConnectionTestResult,
  StashBoxDatabase,
  StashBoxDatabaseCreateRequest,
  StashBoxDatabaseReorderRequest,
  StashBoxDatabaseTestRequest,
  StashBoxDatabaseUpdateRequest,
} from "@dto";

export type {
  StashBoxDatabase,
  StashBoxDatabaseCreateRequest,
  StashBoxDatabaseUpdateRequest,
};

// MAX_STASHBOX_DATABASES mirrors stashboxdb.MaxDatabases. The backend is the
// enforcing side (Create returns 400 at the cap); this exists so the UI can
// swap the Add button for the "5 of 5" note BEFORE the operator fills in a
// form that was always going to be rejected.
export const MAX_STASHBOX_DATABASES = 5;

// fetchStashBoxDatabases lists every configured database (seeded + operator-
// added, enabled or not) in cascade-priority order — GET
// /api/stashbox-databases.
export function fetchStashBoxDatabases(): Promise<StashBoxDatabase[]> {
  return api<StashBoxDatabase[]>("/api/stashbox-databases");
}

// createStashBoxDatabase adds a new operator database — POST
// /api/stashbox-databases. Unlike update, a create has no stored secret to
// preserve, so apiKey is a required plain string (empty is rejected
// server-side as ErrKeyRequired).
export function createStashBoxDatabase(
  body: StashBoxDatabaseCreateRequest,
): Promise<StashBoxDatabase> {
  return api<StashBoxDatabase>("/api/stashbox-databases", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// updateStashBoxDatabase is PUT /api/stashbox-databases/{id}. Every field is
// optional (omitted = leave alone); apiKey specifically carries the
// three-state secret rule — build it with buildStashBoxDatabaseKeyPatch below
// rather than setting it directly.
export function updateStashBoxDatabase(
  id: number,
  body: StashBoxDatabaseUpdateRequest,
): Promise<StashBoxDatabase> {
  return api<StashBoxDatabase>(`/api/stashbox-databases/${id}`, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteStashBoxDatabase(id: number): Promise<void> {
  return api<void>(`/api/stashbox-databases/${id}`, { method: "DELETE" });
}

// reorderStashBoxDatabases sends the FULL new display order in one call —
// ids must cover every existing row exactly once (Store.Reorder's
// requirement), never a partial list. Returns the re-read list so the caller
// can render the persisted order directly instead of issuing a second GET.
export function reorderStashBoxDatabases(
  ids: number[],
): Promise<StashBoxDatabase[]> {
  const body: StashBoxDatabaseReorderRequest = { ids };
  return api<StashBoxDatabase[]>("/api/stashbox-databases/reorder", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// testStashBoxDatabase is the STATELESS test against typed-but-unsaved field
// values — POST /api/stashbox-databases/test. The raw downstream error IS
// returned (every value came from this same request), unlike the stored
// variant below.
export function testStashBoxDatabase(
  req: StashBoxDatabaseTestRequest,
): Promise<ConnectionTestResult> {
  return api<ConnectionTestResult>("/api/stashbox-databases/test", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// testStashBoxDatabaseStored tests a SAVED row's resolved key server-side —
// POST /api/stashbox-databases/{id}/test-stored. On failure the backend
// always returns the fixed string "connection test failed" (never the raw
// downstream error, so a registry row's stored endpoint is never echoed
// back) — same contract as testConnectionStored in settings.ts.
export function testStashBoxDatabaseStored(
  id: number,
): Promise<ConnectionTestResult> {
  return api<ConnectionTestResult>(
    `/api/stashbox-databases/${id}/test-stored`,
    { method: "POST" },
  );
}

// buildStashBoxDatabaseKeyPatch derives the `apiKey` property for an update
// body using the SAME three-state gate every other secret field in this app
// uses (Guardrail #5): omitted when untouched and a key is already stored
// (preserve), present when touched or when there is nothing stored yet. It
// delegates to buildConnectionUpsertBody rather than re-deriving the rule —
// that helper's `url` input is irrelevant here, so it's passed as "" and
// discarded; only `.apiKey`'s presence/absence is read.
export function buildStashBoxDatabaseKeyPatch(input: {
  keyTouched: boolean;
  keyValue: string;
  hasExistingKey: boolean;
}): { apiKey?: string } {
  const upsert = buildConnectionUpsertBody({
    url: "",
    needsUsername: false,
    keyTouched: input.keyTouched,
    keyValue: input.keyValue,
    hasExistingKey: input.hasExistingKey,
  });
  return "apiKey" in upsert ? { apiKey: upsert.apiKey } : {};
}
