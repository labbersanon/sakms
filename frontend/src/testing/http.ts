export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function noContent(): Response {
  return new Response(null, { status: 204 });
}

export function asPage(items: unknown[]) {
  return {
    items,
    total: items.length,
    limit: 50,
    offset: 0,
  };
}
