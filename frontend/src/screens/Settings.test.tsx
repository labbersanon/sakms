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
  if (url.includes("/api/settings/ai-provider"))
    return jsonResponse({ provider: "ollama" });
  if (url.includes("/api/settings/ai-model")) return jsonResponse({ model: "" });
  if (url.includes("/api/settings/recheck-interval"))
    return jsonResponse({ intervalSeconds: 0 });
  if (url.includes("/library/root-folder")) return jsonResponse({ path: "" });
  if (url.includes("/quality-prefs"))
    return jsonResponse({ tier: "high", maxResolution: 0 });
  if (url.includes("/naming-preset")) return jsonResponse({ preset: "jellyfin" });
  if (url.includes("/rename/kids-root-path")) return jsonResponse({ path: "" });
  if (url.includes("/phash-threshold")) return jsonResponse({ threshold: 8 });
  if (url.includes("/match-confidence-threshold"))
    return jsonResponse({ threshold: 70 });
  if (url.includes("/identify-enabled")) return jsonResponse({ enabled: true });
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
const goToSection = (name: "Connections" | "Auth" | "AI" | "Library" | "Advanced") =>
  fireEvent.click(screen.getByRole("button", { name }));

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
    const row = urlInput.closest("tr")!;
    fireEvent.click(within(row).getByText("Save"));

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
    const row = (keyInput as HTMLElement).closest("tr")!;
    fireEvent.click(within(row).getByText("Save"));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ url: "http://prowlarr:9696", apiKey: "sk-rotated" });
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
    fireEvent.click(within(row).getByText("Save"));
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
    fireEvent.click(within(row).getByText("Save"));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    // The fetched key survives the three-state gate because Fetch marks touched.
    expect(put.body).toEqual({
      url: "http://prowlarr:9696",
      apiKey: "fetched-key",
    });
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
    const modelInput = await screen.findByPlaceholderText(/qwen2.5vl/);
    fireEvent.input(modelInput, { target: { value: "gpt-4o-mini" } });
    // The AI panel's Save is the first "Save" in a form with a Model field.
    const modelRow = (modelInput as HTMLElement).closest("form")!;
    fireEvent.click(within(modelRow).getByText("Save"));
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
    const form = input.closest("form")!;
    fireEvent.click(within(form).getByText("Save"));
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

  it("Adult hides Movies/Series-only panels (no library/quality/naming/kids)", async () => {
    stubFetch();
    renderSettings();
    goToSection("Library");
    // Movies (default mode) shows the per-mode panels on the Library tab...
    await screen.findByLabelText("Library root folder");
    // ...and switching to Adult hides them (the mode logic, verified while the
    // Library tab is active — not because the tab itself is hidden).
    fireEvent.click(screen.getByText("Adult"));
    await waitFor(() =>
      expect(screen.queryByLabelText("Library root folder")).toBeNull(),
    );
    expect(screen.queryByLabelText("Kids root folder path")).toBeNull();
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
    const wrap = input.closest("div")!;
    fireEvent.click(within(wrap).getByText("Save"));
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
    const wrap = input.closest("div")!;
    fireEvent.click(within(wrap).getByText("Save"));
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
    const wrap = input.closest("div")!;
    fireEvent.click(within(wrap).getByText("Save"));
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
    const wrap = input.closest("div")!;
    fireEvent.click(within(wrap).getByText("Save"));
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
    const wrap = toggle.closest("div")!;
    fireEvent.click(within(wrap).getByText("Save"));
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

  it("AI tab shows the provider/model panel only", async () => {
    stubFetch();
    renderSettings();
    goToSection("AI");
    expect(await screen.findByPlaceholderText(/qwen2.5vl/)).toBeInTheDocument();
    expect(screen.queryByText("Switch to this mode")).toBeNull();
    expect(screen.queryByText("API Key / Password")).toBeNull();
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
    // Pick Adult on the Library tab — its per-mode panels vanish there.
    fireEvent.click(await screen.findByText("Adult"));
    await waitFor(() =>
      expect(screen.queryByLabelText("Library root folder")).toBeNull(),
    );
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
