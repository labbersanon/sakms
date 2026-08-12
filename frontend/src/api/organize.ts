// Organize shared helpers — page size localStorage + activity log fetch.
// Used by Rename / Dedup / Purge (deep-interview-organize-pagination-log).

import { api } from "./client";
import type { OrganizeEvent, Proposal, ProposalPage } from "@dto";
import type { Mode } from "./discover";

export type OrganizeWorkflow = "rename" | "dedup" | "purge";

// Claude 2026-08-05: live vs history list views (deep-interview-organize-hide-applied).
export type ProposalListView = "live" | "history";

export const PAGE_SIZE_OPTIONS = [25, 50, 100, 200] as const;

const pageSizeKey = (wf: OrganizeWorkflow) => `sakms.organize.${wf}.pageSize`;
const showHistoryKey = (wf: OrganizeWorkflow) =>
  `sakms.organize.${wf}.showHistory`;

export function loadPageSize(wf: OrganizeWorkflow): number {
  try {
    const raw = localStorage.getItem(pageSizeKey(wf));
    const n = raw ? Number(raw) : 50;
    if ((PAGE_SIZE_OPTIONS as readonly number[]).includes(n)) return n;
  } catch {
    /* ignore */
  }
  return 50;
}

export function savePageSize(wf: OrganizeWorkflow, size: number): void {
  try {
    localStorage.setItem(pageSizeKey(wf), String(size));
  } catch {
    /* ignore */
  }
}

export function loadShowHistory(wf: OrganizeWorkflow): boolean {
  try {
    return localStorage.getItem(showHistoryKey(wf)) === "1";
  } catch {
    return false;
  }
}

export function saveShowHistory(wf: OrganizeWorkflow, on: boolean): void {
  try {
    localStorage.setItem(showHistoryKey(wf), on ? "1" : "0");
  } catch {
    /* ignore */
  }
}

export function fetchProposalPage(
  path: string,
  limit: number,
  offset: number,
  view: ProposalListView = "live",
): Promise<ProposalPage> {
  const q = new URLSearchParams({
    limit: String(limit),
    offset: String(offset),
    view,
  });
  return api<ProposalPage>(`${path}?${q}`);
}

export function fetchOrganizeEvents(
  workflow: OrganizeWorkflow,
  limit = 50,
  signal?: AbortSignal,
): Promise<OrganizeEvent[]> {
  const q = new URLSearchParams({ workflow, limit: String(limit) });
  return api<OrganizeEvent[]>(`/api/organize/events?${q}`, { signal });
}

export function fetchPendingIDs(mode: string): Promise<number[]> {
  return api<{ ids: number[] }>(
    `/api/modes/${mode}/rename/proposals/pending-ids`,
  ).then((r) => r.ids ?? []);
}

// proposalVideoUrl builds the src for a <video> preview: the workflow-generic,
// provenance-only streaming endpoint that resolves {mode, proposalId} — plus an
// optional candidateIndex for a candidate-carrying (Dedup) proposal — to a file
// SAK itself recorded during its own scan. Never a client-supplied path; see
// internal/api/proposal_video.go.
//
// candidateIndex is OMITTED, not zeroed, for a single-source (Rename/Purge)
// proposal: the backend dispatches on the parameter's PRESENCE, and sending
// `candidateIndex=0` against a proposal with no candidate array is a 400.
//
// A plain same-origin URL the browser loads with the session cookie. It does
// NOT go through api() — that is for JSON; a <video> streams bytes with Range
// requests.
export function proposalVideoUrl(
  mode: Mode,
  id: number,
  candidateIndex?: number,
): string {
  const base = `/api/modes/${mode}/proposals/${id}/video`;
  return candidateIndex === undefined
    ? base
    : `${base}?candidateIndex=${candidateIndex}`;
}

/** Streaming apply-batch with live progress callbacks. */
export async function applyBatchStreaming(
  items: { id: number; keepIndex?: number; keepAll?: boolean; additionalKeepIndices?: number[] }[],
  onProgress: (done: number, total: number) => void,
): Promise<{ results: { id: number; ok: boolean; error?: string; proposal?: Proposal }[] }> {
  const res = await fetch("/api/proposals/apply-batch", {
    method: "POST",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/x-ndjson",
    },
    body: JSON.stringify({ items }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `apply-batch ${res.status}`);
  }
  const ctype = res.headers.get("Content-Type") || "";
  if (!ctype.includes("ndjson") || !res.body) {
    // Fallback: non-streaming JSON
    const data = (await res.json()) as {
      results: { id: number; ok: boolean; error?: string; proposal?: Proposal }[];
    };
    onProgress(data.results.length, data.results.length);
    return data;
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let final: {
    results: { id: number; ok: boolean; error?: string; proposal?: Proposal }[];
  } = { results: [] };
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const lines = buf.split("\n");
    buf = lines.pop() ?? "";
    for (const line of lines) {
      if (!line.trim()) continue;
      const msg = JSON.parse(line) as {
        type: string;
        done?: number;
        total?: number;
        results?: { id: number; ok: boolean; error?: string; proposal?: Proposal }[];
      };
      if (msg.type === "progress" && msg.total != null && msg.done != null) {
        onProgress(msg.done, msg.total);
      }
      if (msg.type === "done" && msg.results) {
        final = { results: msg.results };
      }
    }
  }
  return final;
}
