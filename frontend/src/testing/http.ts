export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function noContent(): Response {
  return new Response(null, { status: 204 });
}

// seriesMonitorDefaults answers Discover DetailPopup's per-season monitoring
// fetches. Per-season PUTs end in /monitored (with a season number in
// between). Discover's all-seasons write is PUT on the TMDB collection
// (same path as GET) because .../tmdb/{id}/seasons/monitored overlaps
// Library's per-season pattern on Go's ServeMux.
export function seriesMonitorDefaults(
  url: string,
  method = "GET",
): Response | null {
  if (url.includes("/usenet-autograb-enabled")) {
    return jsonResponse({ enabled: true });
  }
  if (url.endsWith("/monitored")) {
    return noContent();
  }
  if (
    method === "PUT" &&
    url.includes("/library/tmdb/") &&
    /\/seasons$/.test(url)
  ) {
    return noContent();
  }
  if (url.includes("/library/tmdb/") && url.includes("/seasons")) {
    return jsonResponse([
      { seasonNumber: 0, episodeCount: 3, missingCount: 3, monitored: false },
      { seasonNumber: 1, episodeCount: 7, missingCount: 2, monitored: true },
    ]);
  }
  return null;
}

export function asPage(items: unknown[]) {
  return {
    items,
    total: items.length,
    limit: 50,
    offset: 0,
  };
}
