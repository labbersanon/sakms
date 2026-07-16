// Stage 4 Settings tests. The load-bearing one this file exists for
// (Acceptance Criterion 5 / Guardrail #5): saving a connection WITHOUT touching
// the API-key field must NOT send `apiKey` at all — the stored secret is
// preserved by omission, and sending "" would silently wipe it. That is asserted
// both directly against buildConnectionUpsertBody (all four states) AND through
// the rendered UI (the property must be ABSENT from the parsed request body,
// not merely undefined). The rest covers each ported panel and the new Advanced
// Settings section, including its range validation.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@solidjs/testing-library";
import { buildConnectionUpsertBody } from "../api/settings";
import { buildTraktCredentialsBody } from "../api/trakt";
import { Settings } from "./Settings";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });
const errorResponse = (status: number, msg: string): Response =>
  new Response(msg, { status });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

// defaultGet answers every GET the Settings mount fires so a test only has to
// override the handful it actually cares about.
function defaultGet(url: string): Response | undefined {
  if (url.includes("/api/connections")) return jsonResponse([]);
  if (url.includes("/api/netscan/known")) return jsonResponse([]);
  if (url.includes("/api/apikey"))
    return jsonResponse({ hasKey: false, source: "none" });
  if (url.includes("/api/auth/mode")) return jsonResponse({ mode: "password" });
  if (url.includes("/api/auth/oidc"))
    return jsonResponse({
      issuerUrl: "",
      clientId: "",
      redirectUrl: "",
      hasSecret: false,
    });
  if (url.includes("/api/settings/ai-fallback-enabled"))
    return jsonResponse({ enabled: true });
  if (url.includes("/api/settings/ai-provider"))
    return jsonResponse({ provider: "ollama" });
  if (url.includes("/api/settings/ai-model")) return jsonResponse({ model: "" });
  if (url.includes("/api/settings/recheck-interval"))
    return jsonResponse({ intervalSeconds: 0 });
  if (url.includes("/library/root-folder")) return jsonResponse({ path: "" });
  if (url.includes("/quality-prefs"))
    return jsonResponse({ tier: "high", maxResolution: 0, protocol: "" });
  if (url.includes("/naming-preset")) return jsonResponse({ preset: "jellyfin" });
  if (url.includes("/rename/kids-root-path")) return jsonResponse({ path: "" });
  if (url.includes("/phash-threshold")) return jsonResponse({ threshold: 8 });
  if (url.includes("/match-confidence-threshold"))
    return jsonResponse({ threshold: 70 });
  if (url.includes("/identify-enabled")) return jsonResponse({ enabled: true });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/api/discover/sliders")) return jsonResponse([]);
  // FolderPicker's as-you-type fetch; the empty-path case returns the fixed
  // browsable roots, matching the real backend.
  if (url.includes("/api/browse"))
    return jsonResponse({
      path: "",
      entries: [
        { name: "/media", path: "/media" },
        { name: "/downloads", path: "/downloads" },
        { name: "/adult", path: "/adult" },
      ],
    });
  return undefined;
}

const stubFetch = (override?: Override) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (override) {
      const r = await override(url, init);
      if (r) return r;
    }
    if (method === "GET") {
      const d = defaultGet(url);
      if (d) return d;
    }
    // default for mutations (PUT/POST/DELETE) is a clean 204.
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  vi.stubGlobal("confirm", vi.fn(() => true));
  return calls;
};

const renderSettings = () => render(() => <Settings onReboot={() => {}} />);

// goToSection clicks a section tab. Settings now splits its panels across
// section tabs (Connections is the default), so a test targeting any non-
// Connections panel must navigate there first. Section-tab buttons are queried
// by role+name so they never collide with a Card's <legend> of the same text
// (legends aren't buttons) nor with the Movies/Series/Adult mode buttons.
const goToSection = (
  name: "Connections" | "Auth" | "AI" | "Library" | "Advanced" | "Sliders",
) => fireEvent.click(screen.getByRole("button", { name }));

// clickSectionSave clicks the one section-level Save button per tab. The batched-
// save refactor consolidated the former per-row / per-card Save buttons into it;
// each tab (Connections, AI, Library-per-mode, Advanced) now has exactly one.
const clickSectionSave = () =>
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// --- The pure three-state gate (exhaustive) --------------------------------

describe("buildConnectionUpsertBody — three-state secret semantics", () => {
  it("OMITS apiKey entirely for an untouched, already-configured connection", () => {
    const body = buildConnectionUpsertBody({
      url: "http://prowlarr:9696",
      needsUsername: false,
      keyTouched: false,
      keyValue: "",
      hasExistingKey: true,
    });
    // The non-negotiable assertion: property ABSENT, not merely undefined.
    expect(body).not.toHaveProperty("apiKey");
    expect(body).toEqual({ url: "http://prowlarr:9696" });
  });

  it("sends the new value when the key was touched", () => {
    const body = buildConnectionUpsertBody({
      url: "http://x",
      needsUsername: false,
      keyTouched: true,
      keyValue: "sk-new",
      hasExistingKey: true,
    });
    expect(body.apiKey).toBe("sk-new");
  });

  it("sends \"\" when the key was touched and cleared (explicit clear)", () => {
    const body = buildConnectionUpsertBody({
      url: "http://x",
      needsUsername: false,
      keyTouched: true,
      keyValue: "",
      hasExistingKey: true,
    });
    expect(body).toHaveProperty("apiKey");
    expect(body.apiKey).toBe("");
  });

  it("sends the (possibly blank) key on a first-time save with no stored key", () => {
    const body = buildConnectionUpsertBody({
      url: "http://ollama:11434",
      needsUsername: false,
      keyTouched: false,
      keyValue: "",
      hasExistingKey: false,
    });
    // A no-key service (Ollama) must still be savable → apiKey present as "".
    expect(body).toHaveProperty("apiKey");
    expect(body.apiKey).toBe("");
  });

  it("includes username for username-based services", () => {
    const body = buildConnectionUpsertBody({
      url: "http://qb:8080",
      username: "admin",
      needsUsername: true,
      keyTouched: false,
      keyValue: "",
      hasExistingKey: true,
    });
    expect(body.username).toBe("admin");
    expect(body).not.toHaveProperty("apiKey");
  });
});

// --- The same guarantee through the rendered UI (graded) -------------------

describe("Connections table — untouched key is never sent (Acceptance Criterion 5)", () => {
  it("saving after editing ONLY the URL omits apiKey from the PUT body", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "prowlarr",
            url: "http://prowlarr:9696",
            hasApiKey: true,
            keySuffix: "abcd",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      return undefined;
    });

    renderSettings();
    const urlInput = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    // The configured connection loads its URL; the key input is blank (the real
    // key is never sent to the client). Edit only the URL, then Save this row.
    fireEvent.input(urlInput, { target: { value: "http://prowlarr:9999" } });
    // One section Save button commits the dirty row; only prowlarr was edited,
    // so only its PUT fires — still built by that row's own body logic.
    clickSectionSave();

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/connections/prowlarr"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/connections/prowlarr"),
    )!;
    // The whole point: apiKey must be ABSENT so the backend preserves the key.
    expect(put.body).not.toHaveProperty("apiKey");
    expect(put.body).toEqual({ url: "http://prowlarr:9999" });
  });

  it("sends the new apiKey when the key field IS edited", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "prowlarr",
            url: "http://prowlarr:9696",
            hasApiKey: true,
            keySuffix: "abcd",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      return undefined;
    });

    renderSettings();
    const keyInput = await screen.findByLabelText("prowlarr API key");
    fireEvent.input(keyInput, { target: { value: "sk-rotated" } });
    clickSectionSave();

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ url: "http://prowlarr:9696", apiKey: "sk-rotated" });
  });

  // The single most important batched-save test: the section's ONE Save button
  // must fire one PUT per dirty row, each built by that row's OWN body logic —
  // never a merged/shared payload. An untouched-key row must OMIT apiKey entirely
  // (property absent, not ""), so its stored secret is preserved, even while a
  // sibling row in the same batched Save sends a freshly-edited key.
  it("batched Save fires one PUT per dirty row with each row's OWN body (untouched key omitted)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "prowlarr",
            url: "http://prowlarr:9696",
            hasApiKey: true,
            keySuffix: "abcd",
            updatedAt: "2026-07-13T00:00:00Z",
          },
          {
            service: "stash",
            url: "http://stash:9999",
            hasApiKey: true,
            keySuffix: "wxyz",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      return undefined;
    });

    renderSettings();
    // Edit ONLY prowlarr's URL (key untouched) and ONLY stash's key.
    const prowlarrUrl = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    fireEvent.input(prowlarrUrl, { target: { value: "http://prowlarr:9999" } });
    const stashKey = await screen.findByLabelText("stash API key");
    fireEvent.input(stashKey, { target: { value: "sk-stash" } });

    // ONE section Save commits both dirty rows in a single click.
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.filter(
          (c) => c.method === "PUT" && c.url.includes("/api/connections/"),
        ).length,
      ).toBe(2),
    );
    const prowlarrPut = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/connections/prowlarr"),
    )!;
    const stashPut = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/connections/stash"),
    )!;
    // prowlarr: untouched key → apiKey ABSENT (stored secret preserved).
    expect(prowlarrPut.body).not.toHaveProperty("apiKey");
    expect(prowlarrPut.body).toEqual({ url: "http://prowlarr:9999" });
    // stash: edited key → its own new key, its own url. Never merged with prowlarr.
    expect(stashPut.body).toEqual({ url: "http://stash:9999", apiKey: "sk-stash" });
  });

  it("Saves a fixed-URL row (tmdb) with no url — no client-side 'url is required' throw", async () => {
    const calls = stubFetch();
    renderSettings();
    // tmdb has no URL input; the operator only sets the API key.
    const keyInput = await screen.findByLabelText("tmdb API key");
    fireEvent.input(keyInput, { target: { value: "tmdb-key" } });
    clickSectionSave();

    // The Save must reach the network (not throw "url is required" first).
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" && c.url.includes("/api/connections/tmdb"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/connections/tmdb"),
    )!;
    // No url in the body (the UI collects none); apiKey carries through.
    expect(put.body).toEqual({ url: "", apiKey: "tmdb-key" });
  });

  it("no longer lists the AI provider / Brave rows (moved to the AI tab)", async () => {
    stubFetch();
    renderSettings();
    // Connections is the default tab; a still-listed service confirms it mounted.
    expect(await screen.findByLabelText("prowlarr URL")).toBeInTheDocument();
    for (const moved of ["ollama", "openai", "gemini", "anthropic", "brave"]) {
      expect(screen.queryByLabelText(`${moved} URL`)).toBeNull();
    }
  });

  it("renders no URL input for fixed-public-API rows (tmdb/stashdb/fansdb/tpdb), only their API Key", async () => {
    stubFetch();
    renderSettings();
    // A URL-required service still shows a URL input.
    expect(await screen.findByLabelText("prowlarr URL")).toBeInTheDocument();
    for (const fixed of ["tmdb", "stashdb", "fansdb", "tpdb"]) {
      // No URL input for these fixed-URL services...
      expect(screen.queryByLabelText(`${fixed} URL`)).toBeNull();
      // ...but their API Key field is still present, so the row is usable.
      expect(screen.getByLabelText(`${fixed} API key`)).toBeInTheDocument();
    }
  });

  it("Delete calls DELETE for that service", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "prowlarr",
            url: "http://prowlarr:9696",
            hasApiKey: true,
            keySuffix: "abcd",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      return undefined;
    });
    renderSettings();
    const urlInput = await screen.findByLabelText("prowlarr URL");
    const row = (urlInput as HTMLElement).closest("tr")!;
    fireEvent.click(within(row).getByText("Delete"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url.includes("/api/connections/prowlarr"),
        ),
      ).toBe(true),
    );
  });
});

// --- Netscan hints (relocated buildNetscanHint) ----------------------------

describe("Connections — netscan LAN-discovery hints", () => {
  it("'Use this URL' pre-fills a discovered Prowlarr URL, and Save sends it", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/netscan/known"))
        return jsonResponse([
          {
            service: "prowlarr",
            url: "http://prowlarr:9696",
            label: "possible Prowlarr",
          },
        ]);
      return undefined;
    });
    renderSettings();
    // The hint block renders inside the prowlarr row.
    const urlInput = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    const row = urlInput.closest("tr")!;
    fireEvent.click(within(row).getByText("Use this URL"));
    expect(urlInput.value).toBe("http://prowlarr:9696");
    clickSectionSave();
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ url: "http://prowlarr:9696", apiKey: "" });
  });

  it("'Fetch API key' fills the key AND marks it touched, so Save includes it", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/netscan/known"))
        return jsonResponse([
          {
            service: "prowlarr",
            url: "http://prowlarr:9696",
            label: "possible Prowlarr",
          },
        ]);
      if (url.includes("/api/netscan/prowlarr-key"))
        return jsonResponse({ apiKey: "fetched-key" });
      return undefined;
    });
    renderSettings();
    const urlInput = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    const row = urlInput.closest("tr")!;
    fireEvent.click(within(row).getByText("Use this URL"));
    fireEvent.click(within(row).getByText("Fetch API key"));
    // Wait for the fetched key to actually land in the input (the fetch is
    // recorded before it resolves; saving too early would race setKey).
    await waitFor(() =>
      expect(
        (screen.getByLabelText("prowlarr API key") as HTMLInputElement).value,
      ).toBe("fetched-key"),
    );
    clickSectionSave();
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    // The fetched key survives the three-state gate because Fetch marks touched.
    expect(put.body).toEqual({
      url: "http://prowlarr:9696",
      apiKey: "fetched-key",
    });
  });
});

// --- Trakt (Watchlist connection) -------------------------------------------
//
// PLACEHOLDER CONTRACT: /api/trakt/* is a proposed shape (src/api/trakt.ts),
// not yet confirmed against task #5's real backend routes. These tests pin
// down this component's OWN logic (three-state secret gate, device-flow
// polling, disconnect) against that proposed contract — not fidelity to
// whatever worker-1 ultimately ships.

describe("buildTraktCredentialsBody — three-state secret semantics", () => {
  it("OMITS clientSecret when untouched", () => {
    const body = buildTraktCredentialsBody({
      clientId: "abc123",
      secretTouched: false,
      secretValue: "",
    });
    expect(body).not.toHaveProperty("clientSecret");
    expect(body).toEqual({ clientId: "abc123" });
  });

  it("sends the new value when touched", () => {
    const body = buildTraktCredentialsBody({
      clientId: "abc123",
      secretTouched: true,
      secretValue: "s3cr3t",
    });
    expect(body).toEqual({ clientId: "abc123", clientSecret: "s3cr3t" });
  });

  it('sends "" when touched and cleared', () => {
    const body = buildTraktCredentialsBody({
      clientId: "abc123",
      secretTouched: true,
      secretValue: "",
    });
    expect(body).toHaveProperty("clientSecret");
    expect(body.clientSecret).toBe("");
  });
});

describe("Trakt connection section", () => {
  it("saving credentials without touching the secret omits clientSecret", async () => {
    const calls = stubFetch();
    renderSettings();
    const clientIdInput = await screen.findByLabelText("Trakt client ID");
    fireEvent.input(clientIdInput, { target: { value: "my-client-id" } });
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/trakt/credentials"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/trakt/credentials"),
    )!;
    expect(put.body).toEqual({ clientId: "my-client-id" });
  });

  it("saving with a secret entered sends clientSecret", async () => {
    const calls = stubFetch();
    renderSettings();
    fireEvent.input(await screen.findByLabelText("Trakt client ID"), {
      target: { value: "my-client-id" },
    });
    fireEvent.input(screen.getByLabelText("Trakt client secret"), {
      target: { value: "my-secret" },
    });
    clickSectionSave();
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/trakt/credentials"),
    )!;
    expect(put.body).toEqual({
      clientId: "my-client-id",
      clientSecret: "my-secret",
    });
  });

  it("Connect is disabled until credentials are configured", async () => {
    stubFetch();
    renderSettings();
    expect(await screen.findByText("Connect")).toBeDisabled();
  });

  it("Connect starts the device flow and shows the user code + verification link", async () => {
    stubFetch((url, init) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: true, linked: false });
      if (url.includes("/api/trakt/device/start") && init?.method === "POST")
        return jsonResponse({
          userCode: "ABCD-1234",
          verificationUrl: "https://trakt.tv/activate",
          expiresIn: 600,
          interval: 5,
        });
      return undefined;
    });
    renderSettings();
    const connectBtn = await screen.findByText("Connect");
    fireEvent.click(connectBtn);
    expect(await screen.findByText("ABCD-1234")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "https://trakt.tv/activate" }),
    ).toHaveAttribute("href", "https://trakt.tv/activate");
  });

  it("polling picks up a completed link and shows Connected", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let statusCalls = 0;
    stubFetch((url, init) => {
      if (url.includes("/api/trakt/status")) {
        statusCalls += 1;
        // First status call (mount) is unlinked/configured; every call after
        // the device flow completes reports linked.
        return jsonResponse(
          statusCalls === 1
            ? { configured: true, linked: false }
            : { configured: true, linked: true, tokenExpiresAt: "2026-08-01T00:00:00Z" },
        );
      }
      if (url.includes("/api/trakt/device/start") && init?.method === "POST")
        return jsonResponse({
          userCode: "ABCD-1234",
          verificationUrl: "https://trakt.tv/activate",
          expiresIn: 600,
          interval: 5,
        });
      if (url.includes("/api/trakt/device/poll") && init?.method === "POST")
        return jsonResponse({ linked: true, pending: false });
      return undefined;
    });
    renderSettings();
    fireEvent.click(await screen.findByText("Connect"));
    await screen.findByText("ABCD-1234");
    // Advance past one poll interval (5s) so the scheduled poll fires.
    await vi.advanceTimersByTimeAsync(5000);
    expect(await screen.findByText("✓ Connected")).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("Disconnect calls the disconnect endpoint", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/trakt/status"))
        return jsonResponse({
          configured: true,
          linked: true,
          tokenExpiresAt: "2026-08-01T00:00:00Z",
        });
      return undefined;
    });
    renderSettings();
    fireEvent.click(await screen.findByText("Disconnect"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url.includes("/api/trakt/disconnect"),
        ),
      ).toBe(true),
    );
  });
});

// --- API Access ------------------------------------------------------------

describe("API Access", () => {
  it("regenerate reveals the one-time key", async () => {
    stubFetch((url, init) => {
      if (url.includes("/api/apikey/regenerate") && init?.method === "POST")
        return jsonResponse({ apiKey: "brand-new-key", keySuffix: "wxyz" });
      return undefined;
    });
    renderSettings();
    goToSection("Auth");
    fireEvent.click(await screen.findByText("Generate key"));
    expect(await screen.findByDisplayValue("brand-new-key")).toBeInTheDocument();
    expect(
      screen.getByText(/Shown once/i),
    ).toBeInTheDocument();
  });
});

// --- Auth mode -------------------------------------------------------------

describe("Authentication mode", () => {
  it("switching to password PUTs /api/auth/mode", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Auth");
    await screen.findByText("Switch to this mode");
    fireEvent.click(screen.getByText("Switch to this mode"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/auth/mode"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/auth/mode"),
    )!;
    expect(put.body).toEqual({ mode: "password", acknowledgeInsecure: false });
  });

  it("surfaces a server-side precondition rejection inline (no client re-implementation)", async () => {
    stubFetch((url, init) => {
      if (url.includes("/api/auth/mode") && init?.method === "PUT")
        return errorResponse(400, "oidc config is not set up");
      return undefined;
    });
    renderSettings();
    goToSection("Auth");
    const select = (await screen.findByLabelText(
      "Mode",
    )) as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "oidc" } });
    fireEvent.click(screen.getByText("Switch to this mode"));
    expect(
      await screen.findByText("oidc config is not set up"),
    ).toBeInTheDocument();
  });

  it("switching to none requires acknowledgeInsecure (after confirm)", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Auth");
    const select = (await screen.findByLabelText(
      "Mode",
    )) as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "none" } });
    fireEvent.click(screen.getByText("Switch to this mode"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/auth/mode"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/auth/mode"),
    )!;
    expect(put.body).toEqual({ mode: "none", acknowledgeInsecure: true });
  });

  it("saving OIDC config PUTs /api/auth/oidc with the full config", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Auth");
    const select = (await screen.findByLabelText(
      "Mode",
    )) as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "oidc" } });
    fireEvent.input(screen.getByPlaceholderText(/sso.example.com/), {
      target: { value: "https://idp/app/o/sakms/" },
    });
    fireEvent.click(screen.getByText("Save OIDC config"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/auth/oidc"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/auth/oidc"),
    )!;
    expect((put.body as { issuerUrl: string }).issuerUrl).toBe(
      "https://idp/app/o/sakms/",
    );
  });
});

// --- AI --------------------------------------------------------------------

describe("AI provider/model", () => {
  it("saves provider then model", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("AI");
    // Wait for the tab to finish loading before editing: the connection rows
    // render only once the fallback-enabled + connections resources resolve, so
    // their presence guarantees the form's seed-from-server effects already ran
    // (otherwise a late resolve would reset the just-edited dirty flag).
    await screen.findByLabelText("ollama URL");
    const modelInput = await screen.findByPlaceholderText(/qwen2.5vl/);
    fireEvent.input(modelInput, { target: { value: "gpt-4o-mini" } });
    // The AI tab's one section Save button commits the provider/model form
    // (provider + model + fallback toggle) in a single click.
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/api/settings/ai-model"),
        ),
      ).toBe(true),
    );
    const providerPut = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/settings/ai-provider"),
    )!;
    expect(providerPut.body).toEqual({ provider: "ollama" });
    const modelPut = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/api/settings/ai-model"),
    )!;
    expect(modelPut.body).toEqual({ model: "gpt-4o-mini" });
  });

  it("renders connection fields for the selected provider AND a separate always-visible Brave row", async () => {
    stubFetch();
    renderSettings();
    goToSection("AI");
    // The default provider (ollama) is the one whose connection fields show —
    // the other providers' fields are NOT rendered at once.
    expect(await screen.findByLabelText("ollama URL")).toBeInTheDocument();
    expect(screen.queryByLabelText("openai URL")).toBeNull();
    expect(screen.queryByLabelText("gemini URL")).toBeNull();
    expect(screen.queryByLabelText("anthropic URL")).toBeNull();
    // Brave is always visible, independent of the provider dropdown.
    expect(screen.getByLabelText("brave URL")).toBeInTheDocument();
  });

  it("switching the provider dropdown swaps which service's connection fields show", async () => {
    stubFetch();
    renderSettings();
    goToSection("AI");
    await screen.findByLabelText("ollama URL");
    const select = (await screen.findByLabelText(
      "AI provider",
    )) as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "anthropic" } });
    // The provider row remounts against the newly-selected service...
    expect(await screen.findByLabelText("anthropic URL")).toBeInTheDocument();
    expect(screen.queryByLabelText("ollama URL")).toBeNull();
    // ...while Brave stays put regardless of the dropdown.
    expect(screen.getByLabelText("brave URL")).toBeInTheDocument();
  });
});

// --- Per-mode panels -------------------------------------------------------

describe("Per-mode panels", () => {
  it("library root folder saves for the selected mode (Movies default)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/movies/library/root-folder") && url.includes("/api"))
        return jsonResponse({ path: "/media/movies" });
      return undefined;
    });
    renderSettings();
    goToSection("Library");
    const input = (await screen.findByLabelText(
      "Library root folder",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("/media/movies"));
    fireEvent.input(input, { target: { value: "/media/films" } });
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/modes/movies/library/root-folder"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/modes/movies/library/root-folder"),
    )!;
    expect(put.body).toEqual({ path: "/media/films" });
  });

  it("switching to Series refetches the per-mode panels against /series/", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Library");
    await screen.findByText("Series");
    fireEvent.click(screen.getByText("Series"));
    await waitFor(() =>
      expect(
        calls.some((c) =>
          c.url.includes("/api/modes/series/library/root-folder"),
        ),
      ).toBe(true),
    );
  });

  it("Adult keeps root folder AND quality prefs but hides naming/kids", async () => {
    stubFetch();
    renderSettings();
    goToSection("Library");
    // Movies (default mode) shows all four per-mode panels on the Library tab...
    await screen.findByLabelText("Library root folder");
    expect(screen.getByLabelText("Kids root folder path")).toBeInTheDocument();
    expect(screen.getByText(/Search quality preferences/)).toBeInTheDocument();
    // ...and switching to Adult keeps the root-folder field (Adult has its own
    // free-typed root folder, backend-wired) AND quality prefs (the Discover
    // popup's availability grid applies to Adult too now), while hiding
    // naming/kids (Adult has a fixed naming scheme, no kids classification).
    fireEvent.click(screen.getByText("Adult"));
    await screen.findByText(/no naming preferences/);
    expect(screen.getByLabelText("Library root folder")).toBeInTheDocument();
    expect(screen.queryByLabelText("Kids root folder path")).toBeNull();
    expect(screen.getByText(/Search quality preferences/)).toBeInTheDocument();
    expect(screen.queryByText(/File\/folder naming/)).toBeNull();
  });
});

// --- Advanced Settings -----------------------------------------------------

describe("Advanced Settings", () => {
  it("recheck-interval saves to the GLOBAL /api/settings/recheck-interval", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/recheck-interval") && url.includes("/api"))
        return jsonResponse({ intervalSeconds: 0 });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Background recheck interval (seconds) — global",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "3600" } });
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/recheck-interval"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" && c.url.includes("/api/settings/recheck-interval"),
    )!;
    expect(put.body).toEqual({ intervalSeconds: 3600 });
  });

  it("rejects a negative recheck-interval client-side (no PUT fired)", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Background recheck interval (seconds) — global",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "-5" } });
    clickSectionSave();
    await screen.findByText(/must be 0 or greater/i);
    expect(
      calls.some(
        (c) =>
          c.method === "PUT" && c.url.includes("/api/settings/recheck-interval"),
      ),
    ).toBe(false);
  });

  it("phash-threshold rejects a value above 64 client-side (no PUT)", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Dedup phash similarity threshold (0–64)",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "99" } });
    clickSectionSave();
    await screen.findByText(/must be between 0 and 64/i);
    expect(
      calls.some(
        (c) => c.method === "PUT" && c.url.includes("/phash-threshold"),
      ),
    ).toBe(false);
  });

  it("phash-threshold saves a valid value to /api/modes/movies/phash-threshold", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/movies/phash-threshold") && url.includes("/api"))
        return jsonResponse({ threshold: 8 });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Dedup phash similarity threshold (0–64)",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("8"));
    fireEvent.input(input, { target: { value: "12" } });
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/modes/movies/phash-threshold"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/phash-threshold"),
    )!;
    expect(put.body).toEqual({ threshold: 12 });
  });

  it("match-confidence-threshold shows for Movies but NOT Adult", async () => {
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    expect(
      await screen.findByLabelText("Rename match-confidence threshold (0–100)"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByText("Adult"));
    await waitFor(() =>
      expect(
        screen.queryByLabelText("Rename match-confidence threshold (0–100)"),
      ).toBeNull(),
    );
  });

  it("identify-enabled shows ONLY for Adult and toggles via /identify-enabled", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/adult/identify-enabled") && url.includes("/api"))
        return jsonResponse({ enabled: true });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    // Not present for Movies (default mode) — wait for a Movies-only Advanced
    // field to confirm the tab mounted before asserting the toggle's absence.
    await screen.findByLabelText("Rename match-confidence threshold (0–100)");
    expect(
      screen.queryByLabelText("Adult phash-first identification enabled"),
    ).toBeNull();
    fireEvent.click(screen.getByText("Adult"));
    const toggle = (await screen.findByLabelText(
      "Adult phash-first identification enabled",
    )) as HTMLInputElement;
    fireEvent.change(toggle, { target: { checked: false } });
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/modes/adult/identify-enabled"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url.includes("/identify-enabled"),
    )!;
    expect(put.body).toEqual({ enabled: false });
  });
});

// --- Section tabs (layout: one section on screen at a time) ----------------

describe("Section tabs", () => {
  it("defaults to Connections and hides every other section", async () => {
    stubFetch();
    renderSettings();
    // Connections table is on screen at mount (its column header is unique).
    expect(await screen.findByText("API Key / Password")).toBeInTheDocument();
    // The signature control of each other section is absent.
    expect(screen.queryByText("Switch to this mode")).toBeNull(); // Auth
    expect(screen.queryByPlaceholderText(/qwen2.5vl/)).toBeNull(); // AI
    expect(screen.queryByLabelText("Library root folder")).toBeNull(); // Library
    expect(
      screen.queryByLabelText("Background recheck interval (seconds) — global"),
    ).toBeNull(); // Advanced
  });

  it("Sliders tab shows the admin slider editor, hiding Connections", async () => {
    stubFetch();
    renderSettings();
    goToSection("Sliders");
    expect(await screen.findByText("+ New slider")).toBeInTheDocument();
    expect(screen.queryByText("API Key / Password")).toBeNull();
  });

  it("Auth tab groups Authentication mode AND API Access, hiding Connections", async () => {
    stubFetch();
    renderSettings();
    goToSection("Auth");
    expect(await screen.findByText("Switch to this mode")).toBeInTheDocument();
    // API Access's break-glass key control lives on the same tab.
    expect(screen.getByText("Generate key")).toBeInTheDocument();
    // The Connections table is no longer mounted.
    expect(screen.queryByText("API Key / Password")).toBeNull();
  });

  it("AI tab shows the provider/model panel plus its connection sub-tables", async () => {
    stubFetch();
    renderSettings();
    goToSection("AI");
    expect(await screen.findByPlaceholderText(/qwen2.5vl/)).toBeInTheDocument();
    expect(screen.queryByText("Switch to this mode")).toBeNull();
    // The connection sub-tables live on this tab now — the selected provider's
    // fields (ollama by default) and the always-visible Brave row.
    expect(await screen.findByLabelText("ollama URL")).toBeInTheDocument();
    expect(screen.getByLabelText("brave URL")).toBeInTheDocument();
  });

  it("Library tab shows per-mode panels beside its own mode selector", async () => {
    stubFetch();
    renderSettings();
    goToSection("Library");
    expect(
      await screen.findByLabelText("Library root folder"),
    ).toBeInTheDocument();
    // The section tab bar and the independent mode selector coexist: the mode
    // buttons are present (exact-text, so they don't match Card titles like
    // "Movies library").
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getByText("Series")).toBeInTheDocument();
    expect(screen.getByText("Adult")).toBeInTheDocument();
    // Advanced's global field is NOT on this tab.
    expect(
      screen.queryByLabelText("Background recheck interval (seconds) — global"),
    ).toBeNull();
  });

  it("Advanced tab shows the Advanced panel and hides Library panels", async () => {
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    expect(
      await screen.findByLabelText(
        "Background recheck interval (seconds) — global",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Library root folder")).toBeNull();
  });

  it("the selected mode persists from Library to Advanced (one shared signal, not two)", async () => {
    stubFetch();
    renderSettings();
    goToSection("Library");
    // Pick Adult on the Library tab — its naming/kids panels vanish there
    // (root folder and quality prefs stay), confirming Adult is the active
    // mode.
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText(/no naming preferences/);
    // Cross to Advanced: Adult must still be the active mode, so the Adult-only
    // identify toggle shows and the Movies/Series-only confidence field doesn't.
    goToSection("Advanced");
    expect(
      await screen.findByLabelText("Adult phash-first identification enabled"),
    ).toBeInTheDocument();
    expect(
      screen.queryByLabelText("Rename match-confidence threshold (0–100)"),
    ).toBeNull();
  });
});

// --- No bulk actions (Acceptance Criterion 6) ------------------------------

describe("Settings — no bulk-action affordances", () => {
  it("has no save-all / apply-all across the whole view", async () => {
    stubFetch();
    renderSettings();
    // Mount confirmed via the always-present section tab bar.
    await screen.findByRole("button", { name: "Connections" });
    expect(screen.queryByText(/save all/i)).toBeNull();
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/test all/i)).toBeNull();
    expect(screen.queryByText(/delete all/i)).toBeNull();
  });
});
