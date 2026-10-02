import { api } from "./client";
import type {
  OrganizeDiscIdentifyResponse,
  OrganizeDiscUnpackItem,
  OrganizeDiscUnpackStatus,
} from "@dto";

export function identifyOrganizeDisc(
  path: string,
  mode?: string,
): Promise<OrganizeDiscIdentifyResponse> {
  return api<OrganizeDiscIdentifyResponse>("/api/organize/discs/identify", {
    method: "POST",
    body: JSON.stringify({ path, mode }),
  });
}

export function startOrganizeDiscExtract(body: {
  path: string;
  mode?: string;
  tmdbId?: number;
  title?: string;
  year?: number;
  box?: string;
  sceneId?: string;
  studio?: string;
  date?: string;
  items: OrganizeDiscUnpackItem[];
}): Promise<OrganizeDiscUnpackStatus> {
  return api<OrganizeDiscUnpackStatus>("/api/organize/discs/extract", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchOrganizeDiscExtract(
  path: string,
): Promise<OrganizeDiscUnpackStatus> {
  return api<OrganizeDiscUnpackStatus>(
    `/api/organize/discs/extract?path=${encodeURIComponent(path)}`,
  );
}
