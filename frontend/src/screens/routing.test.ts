import { describe, expect, it } from "vitest";
import { APP_ROUTES } from "./AppShell";

describe("client-side router scope (Guardrail #2 / requirement #7)", () => {
  it("no route pattern claims any /api/* path", () => {
    for (const route of APP_ROUTES) {
      expect(route.startsWith("/api")).toBe(false);
    }
  });

  // The three former top-level queue routes were removed outright (no redirects,
  // no aliases) when they became tabs under /queue — the "*" NotFound fallback
  // handles a stale bookmark. Machine-checked here rather than eyeballed.
  it("routes /queue and no longer serves the three former queue routes", () => {
    expect(APP_ROUTES).toContain("/queue");
    for (const dead of ["/downloads", "/grabs", "/requests"]) {
      expect(APP_ROUTES as readonly string[]).not.toContain(dead);
    }
  });
});
