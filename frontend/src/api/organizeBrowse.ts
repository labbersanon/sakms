import { api } from "./client";
import type {
  OrganizeBrowseOpResponse,
  OrganizeBrowseResponse,
  OrganizeBrowseStat,
} from "@dto";

export function fetchOrganizeBrowse(path: string): Promise<OrganizeBrowseResponse> {
  const query = path ? `?path=${encodeURIComponent(path)}` : "";
  return api<OrganizeBrowseResponse>(`/api/organize/browse${query}`);
}

export function fetchOrganizeBrowseStat(path: string): Promise<OrganizeBrowseStat> {
  return api<OrganizeBrowseStat>(
    `/api/organize/browse/stat?path=${encodeURIComponent(path)}`,
  );
}

// browseVideoUrl is the <video> src for a Browse Play/preview. Path is
// allowlisted server-side by resolveBrowsablePath; this helper only encodes
// it. Same-origin session cookie, not api() — a <video> streams with Range.
// Route stays under /api/organize/ so Layer 1 classifies it {organize}.
export function browseVideoUrl(path: string): string {
  return `/api/organize/browse/video?path=${encodeURIComponent(path)}`;
}

export function renameOrganizeBrowse(
  path: string,
  newName: string,
): Promise<OrganizeBrowseOpResponse> {
  return api<OrganizeBrowseOpResponse>("/api/organize/browse/rename", {
    method: "POST",
    body: JSON.stringify({ path, newName }),
  });
}

export function moveOrganizeBrowse(
  paths: string[],
  destDir: string,
): Promise<OrganizeBrowseOpResponse> {
  return api<OrganizeBrowseOpResponse>("/api/organize/browse/move", {
    method: "POST",
    body: JSON.stringify({ paths, destDir }),
  });
}

export function deleteOrganizeBrowse(
  paths: string[],
): Promise<OrganizeBrowseOpResponse> {
  return api<OrganizeBrowseOpResponse>("/api/organize/browse/delete", {
    method: "POST",
    body: JSON.stringify({ paths }),
  });
}
