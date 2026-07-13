import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { AuthStatusResponse } from "@dto";
import App from "./App";
import { api } from "./api/client";

// jsonResponse builds a JSON 200 (override status/headers as needed).
const jsonResponse = (obj: unknown, init: ResponseInit = {}): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
    ...init,
  });

const authStatus = (over: Partial<AuthStatusResponse>): AuthStatusResponse => ({
  configured: true,
  authenticated: false,
  mode: "password",
  ...over,
});

// route wires a fetch mock from a URL->Response table; unmatched URLs throw.
// Returns the mock so callers can assert on (or against) fetch calls.
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) =>
    handler(String(input), init),
  );
  vi.stubGlobal("fetch", fn);
  return fn;
};

beforeEach(() => {
  window.history.replaceState(null, "", "/");
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("boot branch: not-set-up", () => {
  it("renders the setup wizard", async () => {
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ configured: false }));
      }
      throw new Error("unexpected " + url);
    });
    render(() => <App />);
    expect(await screen.findByText("Create your SAK login")).toBeInTheDocument();
  });
});

describe("boot branch: set-up-no-session (password)", () => {
  it("renders the password login", async () => {
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "password", authenticated: false }));
      }
      throw new Error("unexpected " + url);
    });
    render(() => <App />);
    expect(
      await screen.findByText("This is the one login that protects this instance."),
    ).toBeInTheDocument();
  });
});

describe("boot branch: set-up-no-session (oidc)", () => {
  it("renders the SSO notice with break-glass recovery always present", async () => {
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "oidc", authenticated: false }));
      }
      throw new Error("unexpected " + url);
    });
    render(() => <App />);
    expect(await screen.findByText("Log in with SSO")).toBeInTheDocument();
    // Recovery must be reachable even on a clean URL (no auth_error).
    expect(screen.getByText("Trouble logging in?")).toBeInTheDocument();
  });

  it("'Log in with SSO' is a full-page navigation, never a fetch/XHR", async () => {
    // The single most-repeated requirement in the plan: OIDC login is a
    // window.location redirect, not an API call. Wiring it as XHR breaks the
    // flow entirely. Lock it: clicking the button must not add a fetch call.
    const fetchMock = stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "oidc", authenticated: false }));
      }
      throw new Error("SSO login must NOT fetch, got: " + url);
    });
    render(() => <App />);
    const btn = await screen.findByText("Log in with SSO");
    const before = fetchMock.mock.calls.length;
    fireEvent.click(btn); // sets window.location.href (a no-op nav under jsdom)
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("shows the mapped auth_error banner from the callback and strips the query", async () => {
    window.history.replaceState(null, "", "/?auth_error=state_mismatch");
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "oidc", authenticated: false }));
      }
      throw new Error("unexpected " + url);
    });
    render(() => <App />);
    expect(
      await screen.findByText(
        "The login response didn't match this browser's request. Please try again.",
      ),
    ).toBeInTheDocument();
    // stripped so a refresh doesn't re-stick it
    expect(window.location.search).toBe("");
  });
});

describe("boot branch: authed app shell", () => {
  it("lands in the app shell for an authenticated session", async () => {
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "password", authenticated: true }));
      }
      if (url === "/api/setup/status") return jsonResponse({ dismissed: true });
      // The app shell renders the read-only Discover view, which fetches the
      // trending/popular category rows on mount — stub them empty (no cards).
      if (url.includes("/discover")) return jsonResponse([]);
      throw new Error("unexpected " + url);
    });
    render(() => <App />);
    // The header's "Log out" control is the stable "we're in the app" marker.
    expect(await screen.findByText("Log out")).toBeInTheDocument();
  });

  it("auth mode = none boots straight to the app with the disabled-auth banner", async () => {
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "none", authenticated: true }));
      }
      if (url === "/api/setup/status") return jsonResponse({ dismissed: true });
      if (url.includes("/discover")) return jsonResponse([]);
      throw new Error("unexpected " + url);
    });
    render(() => <App />);
    expect(await screen.findByText("Log out")).toBeInTheDocument();
    expect(
      screen.getByText(/Authentication is disabled for this instance/),
    ).toBeInTheDocument();
  });
});

describe("session expiry mid-app (requirement #6)", () => {
  it("a 401 on a protected request falls back to the login branch", async () => {
    let authed = true;
    stubFetch((url) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "password", authenticated: authed }));
      }
      if (url === "/api/setup/status") return jsonResponse({ dismissed: true });
      // While the session is live, the app-shell Discover view's row fetches
      // succeed (empty). Once the session is gone, any protected app request
      // 401s — the condition this test exercises.
      if (!authed) return new Response("unauthorized", { status: 401 });
      if (url.includes("/discover")) return jsonResponse([]);
      return jsonResponse([]);
    });

    render(() => <App />);
    expect(await screen.findByText("Log out")).toBeInTheDocument();

    // The session expires; a mid-app request to a protected endpoint 401s.
    authed = false;
    await expect(api("/api/modes/movies/rename")).rejects.toThrow(
      "session expired",
    );

    // App's onSessionExpired re-runs boot -> now unauthenticated -> login.
    expect(
      await screen.findByText("This is the one login that protects this instance."),
    ).toBeInTheDocument();
  });
});

describe("break-glass recovery (requirement #4)", () => {
  it("unlocking with the API key reveals the OIDC repair form", async () => {
    stubFetch((url, init) => {
      if (url === "/api/auth/status") {
        return jsonResponse(authStatus({ mode: "oidc", authenticated: false }));
      }
      if (url === "/api/auth/oidc") {
        // GET config with the break-glass key
        const headers = (init?.headers as Record<string, string>) || {};
        expect(headers["X-Api-Key"]).toBe("sk-break-glass");
        return jsonResponse({
          issuerUrl: "https://idp.example.com/",
          clientId: "cid-123",
          redirectUrl: "https://app.example.com/api/auth/oidc/callback",
          hasSecret: true,
        });
      }
      throw new Error("unexpected " + url);
    });

    render(() => <App />);
    await screen.findByText("Log in with SSO");

    const keyInput = (await screen.findByPlaceholderText(
      "break-glass API key",
    )) as HTMLInputElement;
    fireEvent.input(keyInput, { target: { value: "sk-break-glass" } });
    fireEvent.submit(keyInput.closest("form")!);

    // Repair form appears, prefilled from the fetched config.
    expect(
      await screen.findByDisplayValue("https://idp.example.com/"),
    ).toBeInTheDocument();
    expect(screen.getByText("Save fix")).toBeInTheDocument();
    expect(
      screen.getByText("Switch to password mode instead"),
    ).toBeInTheDocument();
  });
});
