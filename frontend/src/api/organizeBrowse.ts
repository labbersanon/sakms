import { api } from "./client";
import type {
  OrganizeBrowseOpResponse,
  OrganizeBrowseResponse,
} from "@dto";

export function fetchOrganizeBrowse(path: string): Promise<OrganizeBrowseResponse> {
  const query = path ? `?path=${encodeURIComponent(path)}` : "";
  return api<OrganizeBrowseResponse>(`/api/organize/browse${query}`);
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
