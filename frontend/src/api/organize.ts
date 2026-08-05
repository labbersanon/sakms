// Organize shared helpers — page size localStorage + activity log fetch.
// Used by Rename / Dedup / Purge (deep-interview-organize-pagination-log).

import { api } from "./client";
import type { OrganizeEvent, Proposal, ProposalPage } from "@dto";

export type OrganizeWorkflow = "rename" | "dedup" | "purge";

export const PAGE_SIZE_OPTIONS = [25, 50, 100, 200] as const;

const pageSizeKey = (wf: OrganizeWorkflow) => `sakms.organize.${wf}.pageSize`;

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

export function fetchProposalPage(
  path: string,
  limit: number,
  offset: number,
): Promise<ProposalPage> {
  const q = new URLSearchParams({
    limit: String(limit),
    offset: String(offset),
  });
  return api<ProposalPage>(`${path}?${q}`);
}

export function fetchOrganizeEvents(
  workflow: OrganizeWorkflow,
  limit = 50,
): Promise<OrganizeEvent[]> {
  const q = new URLSearchParams({ workflow, limit: String(limit) });
  return api<OrganizeEvent[]>(`/api/organize/events?${q}`);
}

export function fetchPendingIDs(mode: string): Promise<number[]> {
  return api<{ ids: number[] }>(
    `/api/modes/${mode}/rename/proposals/pending-ids`,
  ).then((r) => r.ids ?? []);
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
