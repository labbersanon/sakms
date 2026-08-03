import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  api,
  apiWithKey,
  SectionLockedError,
  SectionLockLockoutError,
  setOnSectionLocked,
  setOnSessionExpired,
} from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
  setOnSessionExpired(null);
  setOnSectionLocked(null);
});

describe("api() 401 handling", () => {
  it("401 on a non-/api/auth path triggers session-expiry and throws", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("nope", { status: 401 })),
    );
    const onExpired = vi.fn();
    setOnSessionExpired(onExpired);

    await expect(api("/api/modes/movies/discover")).rejects.toThrow(
      "session expired",
    );
    expect(onExpired).toHaveBeenCalledOnce();
  });

  it("401 on an /api/auth/ path does NOT trigger session-expiry (surfaces inline)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response("bad key", {
            status: 401,
            headers: { "Content-Type": "text/plain" },
          }),
      ),
    );
    const onExpired = vi.fn();
    setOnSessionExpired(onExpired);

    // The break-glass recovery path relies on this: a wrong key -> 401 on
    // /api/auth/oidc must surface as an error, not reset the whole app.
    await expect(apiWithKey("/api/auth/oidc", "wrong")).rejects.toThrow(
      "bad key",
    );
    expect(onExpired).not.toHaveBeenCalled();
  });
});

describe("api() body handling", () => {
  it("returns null on 204", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 204 })),
    );
    expect(await api("/api/setup/status")).toBeNull();
  });

  it("throws the server's JSON error message on a non-ok response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: "kaboom" }), {
            status: 400,
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );
    await expect(api("/api/setup/status")).rejects.toThrow("kaboom");
  });
});

// FE-3 — a section-lock 403 and the session-expiry 401 must be genuinely
// distinct code paths, not merely claimed to be. Three assertions, because
// only the third one proves the branch keys on the BODY CODE rather than on
// the status: if it keyed on 403 alone, every unrelated 403 in the app would
// start raising a PIN overlay.
describe("FE-3 — section-lock 403 vs the 401 reboot path", () => {
  const lockedResponse = () =>
    new Response(
      JSON.stringify({
        error: "section locked",
        code: "section_locked",
        section: "organize",
      }),
      { status: 403, headers: { "Content-Type": "application/json" } },
    );

  it("throws SectionLockedError and does NOT trigger session expiry", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => lockedResponse()));
    const onExpired = vi.fn();
    setOnSessionExpired(onExpired);
    const onLocked = vi.fn();
    setOnSectionLocked(onLocked);

    const err = await api("/api/modes/adult/rename/scan").catch((e) => e);

    expect(err).toBeInstanceOf(SectionLockedError);
    expect((err as SectionLockedError).section).toBe("organize");
    expect((err as SectionLockedError).status).toBe(403);
    // Still an ApiError, so every existing `.status` branch and `.message`
    // catch in the app keeps working against it unchanged.
    expect(err).toBeInstanceOf(ApiError);
    expect((err as Error).message).toBe("section locked");
    // THE LOAD-BEARING ASSERTION: the SPA-rebooting handler was never called.
    expect(onExpired).not.toHaveBeenCalled();
    expect(onLocked).toHaveBeenCalledWith("organize");
  });

  it("a 403 WITHOUT the section_locked code stays an ordinary ApiError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: "forbidden" }), {
            status: 403,
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );
    const onLocked = vi.fn();
    setOnSectionLocked(onLocked);

    const err = await api("/api/whatever").catch((e) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).not.toBeInstanceOf(SectionLockedError);
    expect(onLocked).not.toHaveBeenCalled();
  });

  it("a 429 lockout is its own error class, never the PIN-overlay one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              error: "too many failed PIN attempts",
              code: "section_lock_lockout",
              retryAfter: 60,
            }),
            { status: 429, headers: { "Content-Type": "application/json" } },
          ),
      ),
    );
    const onExpired = vi.fn();
    setOnSessionExpired(onExpired);
    const onLocked = vi.fn();
    setOnSectionLocked(onLocked);

    const err = await api("/api/section-lock/unlock", {
      method: "POST",
    }).catch((e) => e);

    expect(err).toBeInstanceOf(SectionLockLockoutError);
    expect(err).not.toBeInstanceOf(SectionLockedError);
    expect((err as SectionLockLockoutError).retryAfter).toBe(60);
    expect(onExpired).not.toHaveBeenCalled();
    // A locked-out operator must NOT be shown a PIN box that will refuse them.
    expect(onLocked).not.toHaveBeenCalled();
  });
});

describe("apiWithKey()", () => {
  it("attaches X-Api-Key and preserves the JSON Content-Type", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await apiWithKey("/api/auth/oidc", "sk-123", { method: "PUT" });

    const [, init] = fetchMock.mock.calls[0]!;
    const headers = init?.headers as Record<string, string>;
    expect(headers["X-Api-Key"]).toBe("sk-123");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(init?.method).toBe("PUT");
  });
});
