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
import { createResource, createSignal, Show } from "solid-js";
import { buildConnectionUpsertBody, fetchAdultModeEnabled } from "../api/settings";
import { buildTraktCredentialsBody } from "../api/trakt";
import {
  AdultModeContext,
  ScreenTabBar,
  ScreenTabsContext,
  type ScreenTabsRegistration,
} from "../components/ui";
import { Settings } from "./Settings";
import {
  DurationSetting,
  NumberSetting,
  secondsToUnitAmount,
} from "./settings/Advanced";
import { SectionSave } from "./settings/shared";
import { jsonResponse, noContent } from "../testing/http";


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
  // Claude 2026-08-04: StashBoxDatabases (Stage 5 Wave 5.4) now mounts
  // unconditionally inside Library -> Adult, alongside LibraryConnectionsSection.
  // Without this, every existing Adult-mode Library render hits an unmocked
  // GET and falls through to the 204 default -> fetchStashBoxDatabases
  // resolves null, overwriting createResource's initialValue: [] with null.
  if (url.includes("/api/stashbox-databases")) return jsonResponse([]);
  // The multi-connection registry (migration 0053) that backs BOTH the Usenet
  // page's subscriptions and Advanced -> API Connections' media players. Two
  // separate createResource(fetchServiceConnections) calls hit this on a single
  // Advanced-tab render (SubscriptionsCard and MediaPlayersCard each own one),
  // so any call-counting assertion must filter by method, never assume one GET.
  // NOTE the ordering dependency with the line above: "/api/service-connections"
  // does NOT contain the substring "/api/connections", so the singleton-table
  // branch can't swallow it — but a future rename of either route could break
  // that, and the symptom would be an empty registry, not an error.
  if (url.includes("/api/service-connections")) return jsonResponse([]);
  // The auto-grab toggle's own setting. Answered as OFF here because off IS the
  // default and the whole point (a staged-for-approval exception must never be
  // on unless an operator turned it on). Before this line existed, a GET here
  // fell through to the 204 default -> api() returns null -> the Usenet sub-tab's
  // fetchUsenetAutoGrabEnabled threw on null.enabled and every test rendered the
  // toggle in its load-error state; the load-error tests below now opt INTO
  // that with an explicit error override instead.
  if (url.includes("/api/settings/usenet-autograb-enabled"))
    return jsonResponse({ enabled: false });
  if (url.includes("/api/settings/usenet-autograb-slots"))
    return jsonResponse({ perCycle: 20, perSeries: 5 });
  if (url.includes("/api/settings/torrent-autograb-slots"))
    return jsonResponse({ perCycle: 0, perSeries: 5 });
  // The Series-only new-season discovery toggle (Library tab, Series mode).
  // Answered as OFF because off IS the default. Without this line the GET falls
  // through to the 204 default, api() resolves null, and every Series-mode
  // Library render would show the card in its load-error state.
  if (url.includes("/api/settings/series-new-season-discovery"))
    return jsonResponse({ enabled: false });
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
  if (url.includes("/api/settings/rename-giveback-mainstream-enabled"))
    return jsonResponse({ enabled: false });
  if (url.includes("/api/settings/rename-giveback-adult-enabled"))
    return jsonResponse({ enabled: false });
  if (url.includes("/api/settings/tmdb-session"))
    return jsonResponse({ configured: false });
  if (url.includes("/api/settings/web-search-primary"))
    return jsonResponse({ primary: "searxng" });
  if (url.includes("/api/settings/ai-provider"))
    return jsonResponse({ provider: "ollama" });
  if (url.includes("/api/settings/ai-model")) return jsonResponse({ model: "" });
  if (url.includes("/api/settings/recheck-interval"))
    return jsonResponse({ intervalSeconds: 0 });
  // Claude 2026-08-03: added discover-refresh-interval default GET (FT-4,
  // discover-scheduled-refresh plan §6.1-6.3). Reason: DiscoverRefreshSection
  // now mounts unconditionally alongside RecheckSection on every Advanced-tab
  // render, same as the recheck-interval line above — without this, every
  // existing GlobalSection test hits an unmocked fetch (falls through to the
  // 204 default -> api() resolves null -> fetchDiscoverRefreshInterval's
  // `.then((r) => r.intervalSeconds)` throws on null). 86400 (24h) mirrors
  // this scheduler's real backend default (internal/api/discover_refresh.go's
  // discoverRefreshDefaultSeconds), unlike recheck-interval's off-by-default 0.
  if (url.includes("/api/settings/discover-refresh-interval"))
    return jsonResponse({ intervalSeconds: 86400 });
  // adult_mode_enabled — only fetched by whichever test wraps renderSettings
  // in an AdultModeContext.Provider harness that itself calls
  // fetchAdultModeEnabled (see renderSettingsWithAdultMode below); the plain
  // renderSettings() calls below never trigger this fetch at all (no
  // Provider -> AdultModeContext's own default, enabled=true, applies).
  // Defaults to enabled here too so a harness test that doesn't care about
  // the initial value doesn't have to override it.
  if (url.includes("/api/settings/adult-mode-enabled"))
    return jsonResponse({ enabled: true });
  if (url.includes("/api/settings/entity-sync-interval"))
    return jsonResponse({ intervalSeconds: 0 });
  // WatchFoldersSection (Advanced, global — mounts on every Advanced-tab
  // render regardless of mode) mount GETs.
  if (url.includes("/api/settings/watch-folders-poll-interval"))
    return jsonResponse({ intervalSeconds: 0 });
  if (url.includes("/api/admin/watch-folders"))
    return jsonResponse({ enabled: false, roots: {} });
  // EntityDatabaseSection (Global, unconditional) mount GET — empty cache, no
  // sources synced yet.
  if (url.includes("/api/admin/entity-sync"))
    return jsonResponse({ studioCount: 0, performerCount: 0, sources: [] });
  if (url.includes("/library/root-folder")) return jsonResponse({ path: "" });
  // undoDepth is REQUIRED on QualityPrefsResponse (a plain int, never
  // omitted — the backend substitutes rename.DefaultUndoDepth when nothing is
  // stored), and QualityPrefsSection registers a `valid` predicate on it. A
  // stub that omits it yields undefined, which is not an integer, which
  // correctly disables the WHOLE Library section's batched Save — so an
  // omission here breaks unrelated tests on other tabs rather than this one.
  if (url.includes("/quality-prefs"))
    return jsonResponse({
      tier: "high",
      maxResolution: 0,
      protocol: "",
      undoDepth: 10,
    });
  if (url.includes("/naming-preset")) return jsonResponse({ preset: "jellyfin" });
  if (url.includes("/rename/kids-root-path")) return jsonResponse({ path: "" });
  if (url.includes("/phash-threshold")) return jsonResponse({ threshold: 8 });
  if (url.includes("/rename-match-config"))
    return jsonResponse({ candidateN: 5, durationTolerancePct: 5 });
  if (url.includes("/identify-enabled")) return jsonResponse({ enabled: true });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  if (url.includes("/api/discover/sliders")) return jsonResponse([]);
  // AdultRowAdminSection (UI > Discover > Adult) mount GETs. One includes()
  // covers both the row list and the /genres picker (both arrays).
  if (url.includes("/api/settings/adult-newest-scan-interval"))
    return jsonResponse({ intervalSeconds: 0 });
  if (url.includes("/api/settings/dedup-vmaf-scan-enabled"))
    return jsonResponse({ enabled: true });
  if (url.includes("/api/modes/adult/newest-rows")) return jsonResponse([]);
  // OrganizeScanScheduleSection (Organize tab) mount GETs — Claude 2026-08-10.
  // Six endpoints: one enabled + one interval per workflow. Two suffix matches
  // cover all six; purge-scan-interval is among them (the settings KEY is still
  // `purge` even though the operator-facing label reads "Clean-up").
  if (url.includes("-scan-enabled") && !url.includes("dedup-vmaf-"))
    return jsonResponse({ enabled: true });
  if (
    url.includes("/api/settings/rename-scan-interval") ||
    url.includes("/api/settings/purge-scan-interval") ||
    url.includes("/api/settings/dedup-scan-interval")
  )
    return jsonResponse({ intervalSeconds: 86400 });
  // Claude 2026-08-10: TorrentSettingsCard's onMount GET, needed once Torrent
  // became a sub-tab of Download (plan §3.3). Without it config() stays null
  // and the Torrent panel renders "Loading torrent settings…" forever — so any
  // assertion on that panel would pass while exercising nothing. Field set
  // mirrors settings/Torrent.test.tsx's FIXTURE (all distinctive non-defaults).
  if (url.includes("/api/downloader/config"))
    return jsonResponse({
      stagingDir: "/data/staging",
      maxConcurrent: 7,
      maxConnections: 11,
      downloadRateLimitBytes: 5 * 1024 * 1024,
      dhtEnabled: false,
      pexEnabled: false,
      listenPort: 51413,
      obfuscationMode: "require",
      seedingEnabled: true,
      seedRatioLimit: 2.5,
      seedDurationMinutes: 2880,
      staleThresholdMinutes: 240,
    });
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
    // Auto-test-all fires POST /api/connections/{svc}/test-stored on mount for
    // every configured connection; default it to a clean pass so tests that
    // don't care about the tint stay green. New tests override per-service.
    if (url.includes("/test-stored")) return jsonResponse({ ok: true });
    // default for mutations (PUT/POST/DELETE) is a clean 204.
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  vi.stubGlobal("confirm", vi.fn(() => true));
  return calls;
};

const renderSettings = () => render(() => <Settings onReboot={() => {}} />);

// renderSettingsWithAdultMode mirrors AppShell's real ShellRoot wiring exactly
// (createResource(fetchAdultModeEnabled) + AdultModeContext.Provider), rather
// than a hand-rolled mock — so these tests exercise the SAME propagation path
// production uses: AdultModeSection's toggle handler calls the provided
// refetch after a successful PUT, which re-fetches through whatever
// stubFetch/override is active, and every consumer (ModeSelector, Connections,
// Advanced, UI tab) reactively sees the new value. Requires
// /api/settings/adult-mode-enabled to be answered by defaultGet or an
// override (defaultGet answers it with enabled:true by default, above).
const renderSettingsWithAdultMode = () => {
  const Harness = () => {
    const [enabled, { refetch }] = createResource(fetchAdultModeEnabled);
    return (
      <AdultModeContext.Provider
        value={{ enabled: () => enabled() ?? false, refetch: () => void refetch() }}
      >
        <Settings onReboot={() => {}} />
      </AdultModeContext.Provider>
    );
  };
  return render(() => <Harness />);
};

// goToSection clicks a top-level section tab. AI is a real SECTION_TABS entry
// again (it was briefly folded in as a Connections sub-tab); there is no
// Connections tab any more at all — its rows were redistributed to the section
// each one belongs to. Buttons are queried by role+name so they never collide
// with a Card's <legend> of the same text (legends aren't buttons) nor with the
// Movies/Series/Adult mode buttons.
// Scoped to the section tab bar itself, not the whole screen: several section
// names also occur as button labels inside a section's own body — most sharply
// "Usenet", which is both a tab and one of the quality-prefs protocol pills on
// the (default) Library tab. "Auth" is unique screen-wide, so its tab button's
// parent is a reliable handle on the bar.
const sectionTabBar = () =>
  within(screen.getByRole("button", { name: "Auth" }).parentElement!);
const goToSection = (
  name:
    | "Auth"
    | "AI"
    | "Library"
    | "Download"
    | "Advanced"
    | "UI"
    | "Organize",
) => fireEvent.click(sectionTabBar().getByRole("button", { name }));

// goToDownloadSubTab navigates to the Download section tab and then clicks one
// of its inner Usenet/Torrent sub-tabs. The sub-tab bar is a plain ScreenTabBar
// rendered in the BODY (see settings/Download.tsx), so it is deliberately NOT
// scoped to sectionTabBar() — that scope holds only the shell-registered
// section tabs. Usenet is the default sub-tab, so the click is a no-op for it
// and is issued anyway, to keep every call site honest about which panel it
// expects. Safe despite the "Usenet" collision the comment above warns about
// (it is also a quality-prefs protocol pill): that pill is on the Library tab,
// which is unmounted once Download is active.
const goToDownloadSubTab = (name: "Usenet" | "Torrent") => {
  goToSection("Download");
  fireEvent.click(screen.getByRole("button", { name }));
};

// clickSectionSave clicks the one section-level Save button per tab. The batched-
// save refactor consolidated the former per-row / per-card Save buttons into it.
// Only usable on a tab that renders exactly ONE Save button (AI, Library, and
// Download — UsenetSection wraps BOTH its cards in one SectionSave, and every
// child there is batched, so neither the subscription rows nor the auto-grab
// toggle renders a Save of its own. Download shows exactly one because its
// Usenet/Torrent sub-tabs are a <Switch>: only the active panel, hence only its
// own SectionSave, is ever mounted). The Advanced tab has several, so its connection
// rows use clickAPISectionSave / clickMediaPlayersSave below.
const clickSectionSave = () =>
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

// clickAPISectionSave scopes the click to the "API Connections" card's own
// SectionSave button. Prowlarr/Stash live on the Advanced tab now, which also
// renders the Global cards' standalone Save buttons and the per-mode Advanced
// Settings SectionSave — same disambiguation problem, and same scoped-lookup
// solution, as clickAdvancedSectionSave further down.
const clickAPISectionSave = () => {
  const apiCard = screen.getByText(/^API Connections/).closest("div")!;
  fireEvent.click(within(apiCard).getByRole("button", { name: "Save" }));
};

// clickTraktSave scopes the click to the Trakt card's own SectionSave button.
// Trakt lives on the UI tab now, beside the Discover slider/RSS editors, which
// render Save buttons of their own — same disambiguation problem, same scoped
// lookup, as clickAPISectionSave. The card and its section Save button are
// siblings inside a wrapper div (SectionSave renders children then the button),
// so the scope is the card's parent.
const clickTraktSave = () => {
  const traktCard = screen.getByText("Trakt (Watchlist)").closest("div")!;
  fireEvent.click(
    within(traktCard.parentElement!).getByRole("button", { name: "Save" }),
  );
};

// goToAPIConnections opens the Advanced tab, where the Prowlarr/Stash singleton
// connection rows now live (Advanced -> API Connections, rendered above the mode
// selector because they are global, not per-mode).
const goToAPIConnections = () => goToSection("Advanced");

// goToLibraryConnections opens the Library tab's metadata-source rows for a
// mode: TMDB under Movies, TVDB under Series, StashDB/FansDB/TPDB under Adult.
// Movies is the default mode, so it needs no second click.
const goToLibraryConnections = async (mode?: "Series" | "Adult") => {
  goToSection("Library");
  // The Adult mode button only exists once the adult-mode resource resolves, so
  // this waits rather than assuming it is on screen at first paint.
  if (mode) fireEvent.click(await screen.findByRole("button", { name: mode }));
};

// --- service_connections registry helpers (Usenet subscriptions + players) ---

// clickMediaPlayersSave scopes the click to the "Media players" card's own
// SectionSave button. MediaPlayersCard sits on the Advanced tab beside the API
// Connections card, the Global cards' standalone Saves and the per-mode Advanced
// SectionSave — same disambiguation problem, same scoped lookup, as
// clickAPISectionSave. Card renders <div><h3>{title}</h3>…</div>, so the title
// text's closest div IS the card.
const clickMediaPlayersSave = () => {
  const card = screen.getByText("Media players").closest("div")!;
  fireEvent.click(within(card).getByRole("button", { name: "Save" }));
};

const mediaPlayersSaveButton = () =>
  within(screen.getByText("Media players").closest("div")!).getByRole("button", {
    name: "Save",
  }) as HTMLButtonElement;

// registryRow scopes queries to the one subscription/player row an input belongs
// to. Both SubscriptionRow and PlayerRow wrap themselves in
// `div.rounded.border.border-border.p-3`, and neither field grid nor label
// wrapper carries `rounded`, so the nearest `div.rounded` ancestor is the row.
// Needed because Test/Delete render once PER ROW — a bare getByText("Delete")
// is ambiguous the moment a second subscription or player exists, which is the
// entire point of this registry.
const registryRow = (input: HTMLElement) =>
  within(input.closest("div.rounded") as HTMLElement);

// usenetConn / playerConn build ServiceConnectionSummary fixtures. Every field
// the components read at MOUNT is spelled out (rather than left to `?? default`)
// because both row types seed their local draft from props.conn exactly once —
// an implicit undefined would silently become a different value than the one the
// assertion later reads back. `modes` is always explicit on a player for the
// same reason: sameModes() decides whether the second /modes request fires at
// all, so an ambiguous baseline makes that assertion unreadable.
const usenetConn = (over: Record<string, unknown> = {}) => ({
  id: 1,
  kind: "usenet",
  provider: "nntp",
  label: "Eweka",
  enabled: true,
  sortOrder: 0,
  host: "news.eweka.nl",
  port: 563,
  tls: true,
  username: "wade",
  maxConns: 8,
  hasSecret: true,
  secretSuffix: "6789",
  modes: [] as string[],
  createdAt: "2026-07-30T00:00:00Z",
  updatedAt: "2026-07-30T00:00:00Z",
  ...over,
});

const playerConn = (over: Record<string, unknown> = {}) => ({
  id: 7,
  kind: "player",
  provider: "jellyfin",
  label: "Living room",
  enabled: true,
  sortOrder: 0,
  url: "http://jellyfin:8096",
  hasSecret: true,
  secretSuffix: "wxyz",
  modes: ["movies"],
  createdAt: "2026-07-30T00:00:00Z",
  updatedAt: "2026-07-30T00:00:00Z",
  ...over,
});

// registryFetch answers GET /api/service-connections with a fixed list. Scoped
// with `!url.includes("/test")` so the POST test routes
// (/api/service-connections/test and /{id}/test) still fall through to
// stubFetch's own handling, mirroring the `/api/connections` overrides above.
const registryFetch =
  (rows: unknown[]): Override =>
  (url) => {
    if (url.includes("/api/service-connections") && !url.includes("/test"))
      return jsonResponse(rows);
    return undefined;
  };

// registryPuts / registryModePuts split the two requests a player save makes.
// `/api/service-connections/7` is a prefix of `/api/service-connections/7/modes`,
// so a plain includes() would count the mode request as a field request too —
// the endsWith split is what keeps "exactly one field PUT, zero mode PUTs"
// (the sameModes guard) an honest assertion rather than a tautology.
const registryPuts = (calls: Call[]) =>
  calls.filter(
    (c) =>
      c.method === "PUT" &&
      c.url.includes("/api/service-connections/") &&
      !c.url.endsWith("/modes"),
  );
const registryModePuts = (calls: Call[]) =>
  calls.filter((c) => c.method === "PUT" && c.url.endsWith("/modes"));

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
    goToAPIConnections();
    const urlInput = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    // The configured connection loads its URL; the key input is blank (the real
    // key is never sent to the client). Edit only the URL, then Save this row.
    fireEvent.input(urlInput, { target: { value: "http://prowlarr:9999" } });
    // One section Save button commits the dirty row; only prowlarr was edited,
    // so only its PUT fires — still built by that row's own body logic.
    clickAPISectionSave();

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
    goToAPIConnections();
    const keyInput = await screen.findByLabelText("prowlarr API key");
    fireEvent.input(keyInput, { target: { value: "sk-rotated" } });
    clickAPISectionSave();

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
    // Both rows live in Advanced -> API Connections, under one SectionSave.
    goToAPIConnections();
    // Edit ONLY prowlarr's URL (key untouched) and ONLY stash's key.
    const prowlarrUrl = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    fireEvent.input(prowlarrUrl, { target: { value: "http://prowlarr:9999" } });
    const stashKey = await screen.findByLabelText("stash API key");
    fireEvent.input(stashKey, { target: { value: "sk-stash" } });

    // ONE section Save commits both dirty rows in a single click.
    clickAPISectionSave();
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
    goToAPIConnections();
    // A still-listed service confirms the section mounted.
    expect(await screen.findByLabelText("prowlarr URL")).toBeInTheDocument();
    for (const moved of ["ollama", "openai", "gemini", "anthropic", "brave"]) {
      expect(screen.queryByLabelText(`${moved} URL`)).toBeNull();
    }
  });

  it("renders no URL input for fixed-public-API rows (tmdb/stashdb/fansdb/tpdb), only their API Key", async () => {
    stubFetch();
    renderSettings();
    // The fixed-URL metadata sources are split across Library's modes now:
    // tmdb under Movies (the default), stashdb/fansdb/tpdb under Adult.
    await goToLibraryConnections();
    expect(await screen.findByLabelText("tmdb API key")).toBeInTheDocument();
    expect(screen.queryByLabelText("tmdb URL")).toBeNull();

    // Series gets TVDB and ONLY TVDB — the assertion that tmdb is gone is what
    // proves the per-mode split, rather than merely that a row rendered.
    await goToLibraryConnections("Series");
    expect(await screen.findByLabelText("tvdb API key")).toBeInTheDocument();
    expect(screen.queryByLabelText("tvdb URL")).toBeNull();
    expect(screen.queryByLabelText("tmdb API key")).toBeNull();

    // Claude 2026-08-04: Adult's fixed-URL set is TPDB alone now (Stage 5
    // Wave 5, plan .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md §5.4).
    // StashDB and FansDB are rows of the configurable stash-box registry and
    // render in their own panel with a REAL, editable endpoint field — the
    // opposite of what this test asserts — so their absence here is the
    // criterion (AC2/AC13), not a regression.
    // for (const fixed of ["stashdb", "fansdb", "tpdb"]) {   // ← was
    await goToLibraryConnections("Adult");
    expect(screen.queryByLabelText("tpdb URL")).toBeNull();
    expect(await screen.findByLabelText("tpdb API key")).toBeInTheDocument();
    for (const relocated of ["stashdb", "fansdb"]) {
      expect(screen.queryByLabelText(`${relocated} API key`)).toBeNull();
      expect(screen.queryByLabelText(`${relocated} URL`)).toBeNull();
    }

    // A URL-required service still shows a URL input.
    goToAPIConnections();
    expect(await screen.findByLabelText("prowlarr URL")).toBeInTheDocument();
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
    goToAPIConnections();
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
    goToAPIConnections();
    // The hint block renders inside the prowlarr row.
    const urlInput = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    const row = urlInput.closest("tr")!;
    fireEvent.click(within(row).getByText("Use this URL"));
    expect(urlInput.value).toBe("http://prowlarr:9696");
    clickAPISectionSave();
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
    goToAPIConnections();
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
    clickAPISectionSave();
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    // The fetched key survives the three-state gate because Fetch marks touched.
    expect(put.body).toEqual({
      url: "http://prowlarr:9696",
      apiKey: "fetched-key",
    });
  });
});

// --- Connections auto-test-all + red-tint (section 2b) ----------------------
//
// On mount (and after every save) every CONFIGURED connection is tested against
// the stored-connection endpoint; a failing result red-tints that row's inputs.
// A manual per-row Test drives the SAME shared tint. The tint is asserted via
// className (jsdom can't see rendered color, and a mis-specified Tailwind class
// would silently no-op), so `border-danger` present/absent is the real check.

const oneConn = (over: Record<string, unknown> = {}) => ({
  service: "prowlarr",
  url: "http://prowlarr:9696",
  hasApiKey: true,
  keySuffix: "abcd",
  updatedAt: "2026-07-13T00:00:00Z",
  ...over,
});

describe("Connections — auto-test-all on mount + red-tint", () => {
  it("(a) red-tints a configured connection whose stored test fails, with no click", async () => {
    stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([oneConn()]);
      if (url.includes("/api/connections/prowlarr/test-stored"))
        return jsonResponse({ ok: false, error: "connection test failed" });
      return undefined;
    });
    renderSettings();
    goToAPIConnections();
    const keyInput = (await screen.findByLabelText(
      "prowlarr API key",
    )) as HTMLInputElement;
    // Tint appears purely from the mount-time auto-test — no operator action.
    await waitFor(() => expect(keyInput.className).toContain("border-danger"));
    // The URL input is tinted too (prowlarr is not a fixed-URL service).
    expect(
      (screen.getByLabelText("prowlarr URL") as HTMLInputElement).className,
    ).toContain("border-danger");
  });

  it("(a2) leaves a passing configured connection untinted", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([oneConn()]);
      if (url.includes("/api/connections/prowlarr/test-stored"))
        return jsonResponse({ ok: true });
      return undefined;
    });
    renderSettings();
    goToAPIConnections();
    const keyInput = (await screen.findByLabelText(
      "prowlarr API key",
    )) as HTMLInputElement;
    // Give the mount auto-test time to resolve, then assert no tint.
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/prowlarr/test-stored")),
      ).toBe(true),
    );
    expect(keyInput.className).not.toContain("border-danger");
  });

  it("(b) never tests or tints a connection with no stored key", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([oneConn({ hasApiKey: false, keySuffix: "" })]);
      return undefined;
    });
    renderSettings();
    goToAPIConnections();
    const keyInput = (await screen.findByLabelText(
      "prowlarr API key",
    )) as HTMLInputElement;
    // The row mounts only after conns() resolves (so the effect has run); a
    // keyless service is skipped entirely — no test-stored call, no tint.
    expect(keyInput.className).not.toContain("border-danger");
    expect(calls.some((c) => c.url.includes("/test-stored"))).toBe(false);
  });

  it("(c) a manual Test failure red-tints, and a passing manual Test clears it", async () => {
    const manualResults = [
      { ok: false, error: "connection failed" },
      { ok: true },
    ];
    let i = 0;
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([oneConn()]);
      // Stored test passes at mount so the row starts untinted.
      if (url.includes("/test-stored")) return jsonResponse({ ok: true });
      // Manual test toggles fail → pass across the two clicks.
      if (url.endsWith("/api/connections/test") && init?.method === "POST") {
        const r = manualResults[Math.min(i, manualResults.length - 1)];
        i += 1;
        return jsonResponse(r);
      }
      return undefined;
    });
    renderSettings();
    goToAPIConnections();
    const keyInput = (await screen.findByLabelText(
      "prowlarr API key",
    )) as HTMLInputElement;
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/prowlarr/test-stored")),
      ).toBe(true),
    );
    expect(keyInput.className).not.toContain("border-danger");
    const row = keyInput.closest("tr")!;
    // Manual Test #1 fails → tint.
    fireEvent.click(within(row).getByText("Test"));
    await waitFor(() => expect(keyInput.className).toContain("border-danger"));
    // Manual Test #2 passes → tint clears.
    fireEvent.click(within(row).getByText("Test"));
    await waitFor(() =>
      expect(keyInput.className).not.toContain("border-danger"),
    );
  });

  it("(e) re-runs the auto-test after a batched Save (fires per conns() re-resolution)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([oneConn()]);
      if (url.includes("/test-stored")) return jsonResponse({ ok: true });
      return undefined;
    });
    renderSettings();
    goToAPIConnections();
    const urlInput = (await screen.findByLabelText(
      "prowlarr URL",
    )) as HTMLInputElement;
    const countStored = () =>
      calls.filter((c) => c.url.includes("/prowlarr/test-stored")).length;
    await waitFor(() => expect(countStored()).toBeGreaterThanOrEqual(1));
    const before = countStored();
    // Edit + batched Save → onChanged → refetch → conns() re-resolves → the
    // auto-test effect runs again (proving it's hooked off conns(), not mount).
    fireEvent.input(urlInput, { target: { value: "http://prowlarr:9999" } });
    clickAPISectionSave();
    await waitFor(() => expect(countStored()).toBeGreaterThan(before));
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
    goToSection("UI");
    const clientIdInput = await screen.findByLabelText("Trakt client ID");
    fireEvent.input(clientIdInput, { target: { value: "my-client-id" } });
    clickTraktSave();
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
    goToSection("UI");
    fireEvent.input(await screen.findByLabelText("Trakt client ID"), {
      target: { value: "my-client-id" },
    });
    fireEvent.input(screen.getByLabelText("Trakt client secret"), {
      target: { value: "my-secret" },
    });
    clickTraktSave();
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
    goToSection("UI");
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
    goToSection("UI");
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
    goToSection("UI");
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
    goToSection("UI");
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

  // Settings is the SECOND call site for PUT /api/auth/mode — OidcLogin.tsx's
  // pre-session recovery form is the first, and only that one was wired for the
  // section PIN. §4.4 gates this route on the PIN because switching to "none"
  // disarms the section lock; the backend refuses with 400 "a PIN is required",
  // NOT the 403 section_locked shape the PIN overlay reacts to, so an unwired
  // screen shows an error it offers the operator no way to satisfy.
  it("sends the section PIN as X-Section-Pin when one is entered", async () => {
    let sent: Record<string, string> | undefined;
    stubFetch((url, init) => {
      if (url.includes("/api/auth/mode") && init?.method === "PUT")
        sent = init.headers as Record<string, string>;
      return undefined;
    });
    renderSettings();
    goToSection("Auth");
    const pin = await screen.findByLabelText("Section PIN (only if one is set)");
    fireEvent.input(pin, { target: { value: "hunter2" } });
    fireEvent.click(screen.getByText("Switch to this mode"));
    await waitFor(() => expect(sent).toBeDefined());
    expect(sent!["X-Section-Pin"]).toBe("hunter2");
    // api()'s options merge is shallow, so passing headers at all replaces the
    // default object — the JSON content type has to be restated or the PUT
    // body stops being parsed server-side.
    expect(sent!["Content-Type"]).toBe("application/json");
  });

  it("omits X-Section-Pin entirely when the field is left blank", async () => {
    // An empty header is not a WRONG pin, and the backend's brute-force counter
    // must never see it as a failed attempt.
    let sent: Record<string, string> | undefined;
    stubFetch((url, init) => {
      if (url.includes("/api/auth/mode") && init?.method === "PUT")
        sent = init.headers as Record<string, string>;
      return undefined;
    });
    renderSettings();
    goToSection("Auth");
    await screen.findByText("Switch to this mode");
    fireEvent.click(screen.getByText("Switch to this mode"));
    await waitFor(() => expect(sent).toBeDefined());
    expect(sent!["X-Section-Pin"]).toBeUndefined();
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
    const calls = stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "ollama",
            url: "http://ollama:11434",
            hasApiKey: false,
            keySuffix: "",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      if (url.includes("/api/ollama/models"))
        return jsonResponse(["llama3", "qwen2.5vl:7b"]);
      return undefined;
    });
    renderSettings();
    goToSection("AI");
    // Wait for the tab to finish loading before editing: the connection rows
    // render only once the fallback-enabled + connections resources resolve, so
    // their presence guarantees the form's seed-from-server effects already ran
    // (otherwise a late resolve would reset the just-edited dirty flag).
    await screen.findByLabelText("ollama URL");
    const modelSelect = (await screen.findByLabelText(
      "Ollama model",
    )) as HTMLSelectElement;
    // Wait for the live-fetched options (from the saved ollama connection URL
    // above) to populate before picking one.
    await waitFor(() =>
      expect(within(modelSelect).getByText("qwen2.5vl:7b")).toBeInTheDocument(),
    );
    fireEvent.change(modelSelect, { target: { value: "qwen2.5vl:7b" } });
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
    expect(modelPut.body).toEqual({ model: "qwen2.5vl:7b" });
  });

  it("an unreachable Ollama instance shows an inline error instead of crashing the tab", async () => {
    stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "ollama",
            url: "http://unreachable:11434",
            hasApiKey: false,
            keySuffix: "",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      if (url.includes("/api/ollama/models"))
        return errorResponse(502, "couldn't reach ollama");
      return undefined;
    });
    renderSettings();
    goToSection("AI");
    await screen.findByLabelText("ollama URL");
    // The tab stays on screen — no uncaught render throw from reading the
    // errored resource (see ollamaOptions' doc comment on why it guards on
    // ollamaModels.error before calling the resource accessor).
    expect(await screen.findByText(/Couldn't reach Ollama/)).toBeInTheDocument();
    expect(
      (await screen.findByLabelText("Ollama model")) as HTMLSelectElement,
    ).toBeInTheDocument();
  });

  it("keeps the Ollama model select in sync with the stored model once the live list resolves after mount (regression)", async () => {
    // Reproduces the exact race that caused a real bug: the stored model
    // ("modelB") resolves and settles BEFORE the live /api/tags-backed list
    // does. ollamaOptions() re-renders its <option>s (unkeyed, since each
    // call returns fresh objects) once the list finally arrives — a plain
    // `value={mdl()}` binding never re-fires on that (only mdl() changing
    // re-fires it), so the browser silently auto-selects the first newly
    // inserted option ("modelA") instead of the stored one. Asserting the
    // select's real DOM `.value`, not just that some option text renders, is
    // the point — a rendered label doesn't prove it's actually selected.
    let resolveModels!: (models: string[]) => void;
    const modelsPromise = new Promise<string[]>((resolve) => {
      resolveModels = resolve;
    });
    stubFetch((url) => {
      if (url.includes("/api/connections") && !url.includes("/test"))
        return jsonResponse([
          {
            service: "ollama",
            url: "http://ollama:11434",
            hasApiKey: false,
            keySuffix: "",
            updatedAt: "2026-07-13T00:00:00Z",
          },
        ]);
      if (url.includes("/api/settings/ai-model"))
        return jsonResponse({ model: "modelB" });
      if (url.includes("/api/ollama/models"))
        return modelsPromise.then((models) => jsonResponse(models));
      return undefined;
    });
    renderSettings();
    goToSection("AI");
    const modelSelect = (await screen.findByLabelText(
      "Ollama model",
    )) as HTMLSelectElement;
    // The stored model has settled (mdl() === "modelB") and the live list is
    // still loading — the select is disabled while ollamaModels.loading.
    await waitFor(() => expect(modelSelect.disabled).toBe(true));
    resolveModels(["modelA", "modelB"]);
    await waitFor(() => expect(modelSelect.disabled).toBe(false));
    await waitFor(() =>
      expect(within(modelSelect).getByText("modelA")).toBeInTheDocument(),
    );
    expect(modelSelect.value).toBe("modelB");
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
    // Brave is always visible, independent of the provider dropdown — its URL
    // field is hidden too (fixed-URL service), so its API Key field is the
    // presence signal instead.
    expect(screen.getByLabelText("brave API key")).toBeInTheDocument();
    expect(screen.queryByLabelText("brave URL")).toBeNull();
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
    // The provider row remounts against the newly-selected service — its URL
    // field stays hidden (fixed-URL service), so its API Key field is the
    // presence signal instead.
    expect(
      await screen.findByLabelText("anthropic API key"),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("anthropic URL")).toBeNull();
    expect(screen.queryByLabelText("ollama URL")).toBeNull();
    // ...while Brave stays put regardless of the dropdown.
    expect(screen.getByLabelText("brave API key")).toBeInTheDocument();
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

// --- Library root-folder path test (section 2b) ----------------------------

describe("Library root-folder Test button", () => {
  it("(d) tests the current typed path, red-tints on failure, and clears on a pass", async () => {
    const results = [
      { ok: false, error: "path does not exist" },
      { ok: true },
    ];
    let i = 0;
    const calls = stubFetch((url, init) => {
      if (url.includes("/library/root-folder/test") && init?.method === "POST") {
        const r = results[Math.min(i, results.length - 1)];
        i += 1;
        return jsonResponse(r);
      }
      return undefined;
    });
    renderSettings();
    goToSection("Library");
    const input = (await screen.findByLabelText(
      "Library root folder",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "/media/movies" } });
    // Scoped to the "<Mode> library" card: the Metadata sources card on this
    // same tab gives every connection row its own "Test" button, so a bare
    // "Test" query is no longer unique here.
    const libraryCard = screen.getByText(/^Movies library/).closest("div")!;
    const clickRootFolderTest = () =>
      fireEvent.click(within(libraryCard).getByRole("button", { name: "Test" }));

    // Test #1 fails → red-tint AND the endpoint's human-readable error shows
    // (this endpoint's errors are safe to surface, unlike the connection test).
    clickRootFolderTest();
    await waitFor(() => expect(input.className).toContain("border-danger"));
    expect(await screen.findByText("path does not exist")).toBeInTheDocument();
    const testCall = calls.find(
      (c) =>
        c.method === "POST" &&
        c.url.includes("/api/modes/movies/library/root-folder/test"),
    )!;
    expect(testCall.body).toEqual({ path: "/media/movies" });

    // Test #2 passes → tint clears.
    clickRootFolderTest();
    await waitFor(() =>
      expect(input.className).not.toContain("border-danger"),
    );
  });
});

// --- DurationSetting's pure conversion helper (exhaustive edge cases) ------

describe("secondsToUnitAmount", () => {
  it("0 (or unset) always shows as off", () => {
    expect(secondsToUnitAmount(0)).toEqual({ unit: "hours", amount: 0 });
  });

  it("picks the exact-fit unit for values this picker itself produces", () => {
    expect(secondsToUnitAmount(3600)).toEqual({ unit: "hours", amount: 1 });
    expect(secondsToUnitAmount(86400)).toEqual({ unit: "days", amount: 1 });
    expect(secondsToUnitAmount(60)).toEqual({ unit: "minutes", amount: 1 });
    expect(secondsToUnitAmount(23 * 3600)).toEqual({
      unit: "hours",
      amount: 23,
    });
    expect(secondsToUnitAmount(30 * 86400)).toEqual({
      unit: "days",
      amount: 30,
    });
  });

  it("a legacy odd-seconds value (pre-dating this picker) never collapses to 0/off", () => {
    // 90s and 45s divide evenly into no unit — the old free-typed "seconds"
    // NumberSetting could store either. Must round to a non-zero minutes
    // amount, never silently read/save as "0 = off".
    expect(secondsToUnitAmount(90)).toEqual({ unit: "minutes", amount: 2 });
    expect(secondsToUnitAmount(45)).toEqual({ unit: "minutes", amount: 1 });
  });

  it("an odd value too large for minutes' bound escalates to hours, then days", () => {
    // 59*60 + 30 = 3570s: not an exact minutes/hours/days fit, and rounding
    // to minutes (60) exceeds the 59 max, so it must escalate to hours.
    expect(secondsToUnitAmount(3570)).toEqual({ unit: "hours", amount: 1 });
  });

  it("a huge/legacy value beyond the 30-day bound clamps, without NaN or negative", () => {
    const result = secondsToUnitAmount(Number.MAX_SAFE_INTEGER);
    expect(result.unit).toBe("days");
    expect(result.amount).toBe(30);
    expect(Number.isNaN(result.amount)).toBe(false);
  });
});

// --- DurationSetting/NumberSetting registration id (not label-derived) ----

describe("DurationSetting / NumberSetting registration — id, not label", () => {
  it("two DurationSettings sharing the same label save independently, keyed by id", async () => {
    // Regression test for a real bug: SectionSave's registry replaces any
    // existing entry with the same id on (re-)register
    // (`prev.filter(i => i.id !== item.id)` then append) — when id was
    // derived from label, two same-labeled fields would silently evict one
    // another and only the LAST-registered field's save would ever fire,
    // with no visible error. Explicit, caller-supplied ids fix this
    // structurally rather than relying on every label staying unique.
    const saveA = vi.fn().mockResolvedValue(undefined);
    const saveB = vi.fn().mockResolvedValue(undefined);
    render(() => (
      <SectionSave>
        <DurationSetting
          id="field-a"
          label="Same label"
          help="help a"
          value={() => 3600}
          onSave={saveA}
        />
        <DurationSetting
          id="field-b"
          label="Same label"
          help="help b"
          value={() => 7200}
          onSave={saveB}
        />
      </SectionSave>
    ));
    const inputs = (await screen.findAllByLabelText(
      "Same label",
    )) as HTMLInputElement[];
    expect(inputs).toHaveLength(2);
    fireEvent.input(inputs[0]!, { target: { value: "2" } }); // 2 hours = 7200s
    fireEvent.input(inputs[1]!, { target: { value: "3" } }); // 3 hours = 10800s
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(saveA).toHaveBeenCalledWith(7200);
      expect(saveB).toHaveBeenCalledWith(10800);
    });
  });

  it("two NumberSettings sharing the same label save independently, keyed by id", async () => {
    const saveA = vi.fn().mockResolvedValue(undefined);
    const saveB = vi.fn().mockResolvedValue(undefined);
    render(() => (
      <SectionSave>
        <NumberSetting
          id="field-a"
          label="Same label"
          help="help a"
          value={() => 1}
          min={0}
          onSave={saveA}
        />
        <NumberSetting
          id="field-b"
          label="Same label"
          help="help b"
          value={() => 2}
          min={0}
          onSave={saveB}
        />
      </SectionSave>
    ));
    const inputs = (await screen.findAllByLabelText(
      "Same label",
    )) as HTMLInputElement[];
    expect(inputs).toHaveLength(2);
    fireEvent.input(inputs[0]!, { target: { value: "10" } });
    fireEvent.input(inputs[1]!, { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(saveA).toHaveBeenCalledWith(10);
      expect(saveB).toHaveBeenCalledWith(20);
    });
  });

  it("one NumberSetting out of range disables the WHOLE section's Save button, blocking a separate valid field too", async () => {
    // The section's Save button is shared, not per-field — so an invalid
    // field doesn't just block its own save, it blocks everything currently
    // dirty in the same SectionSave until it's fixed.
    const saveValid = vi.fn().mockResolvedValue(undefined);
    const saveInvalid = vi.fn().mockResolvedValue(undefined);
    render(() => (
      <SectionSave>
        <NumberSetting
          id="valid-field"
          label="Valid field"
          help="help"
          value={() => 5}
          min={0}
          max={10}
          onSave={saveValid}
        />
        <NumberSetting
          id="bounded-field"
          label="Bounded field"
          help="help"
          value={() => 5}
          min={0}
          max={10}
          onSave={saveInvalid}
        />
      </SectionSave>
    ));
    const validInput = (await screen.findByLabelText(
      "Valid field",
    )) as HTMLInputElement;
    const boundedInput = (await screen.findByLabelText(
      "Bounded field",
    )) as HTMLInputElement;
    const saveButton = screen.getByRole("button", {
      name: "Save",
    }) as HTMLButtonElement;

    fireEvent.input(validInput, { target: { value: "7" } }); // still valid
    fireEvent.input(boundedInput, { target: { value: "99" } }); // out of range
    await waitFor(() => expect(saveButton.disabled).toBe(true));
    fireEvent.click(saveButton);
    expect(saveValid).not.toHaveBeenCalled();
    expect(saveInvalid).not.toHaveBeenCalled();

    // Fixing the bounded field re-enables the button and BOTH dirty fields save.
    fireEvent.input(boundedInput, { target: { value: "3" } });
    await waitFor(() => expect(saveButton.disabled).toBe(false));
    fireEvent.click(saveButton);
    await waitFor(() => {
      expect(saveValid).toHaveBeenCalledWith(7);
      expect(saveInvalid).toHaveBeenCalledWith(3);
    });
  });
});

// --- DurationSetting's number input select-on-focus ------------------------

describe("DurationSetting — select-all-on-focus", () => {
  it("focusing the amount field selects its current text, so typing replaces rather than appends", async () => {
    render(() => (
      <DurationSetting
        id="focus-test"
        label="Focus test field"
        help="help"
        value={() => 20 * 3600} // 20 hours — near the 23-hour max
        onSave={vi.fn().mockResolvedValue(undefined)}
      />
    ));
    const input = (await screen.findByLabelText(
      "Focus test field",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("20"));
    fireEvent.focus(input);
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe(input.value.length);
  });

  it("typing a non-numeric character is ignored, not saved as NaN", async () => {
    // The field is type="text" (needed for select() to work at all — see
    // the component's own doc comment), so unlike a native type="number"
    // input nothing stops a stray letter from reaching onInput.
    const save = vi.fn().mockResolvedValue(undefined);
    render(() => (
      <DurationSetting
        id="nan-test"
        label="NaN test field"
        help="help"
        value={() => 3600}
        onSave={save}
      />
    ));
    const input = (await screen.findByLabelText(
      "NaN test field",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("1"));
    fireEvent.input(input, { target: { value: "x" } });
    // Ignored: neither the displayed value nor the underlying amount moves.
    expect(input.value).toBe("x"); // raw DOM text isn't rewritten mid-typing
    fireEvent.blur(input);
    expect(input.value).toBe("1"); // blur re-syncs to the untouched amount
  });
});

// clickAdvancedSectionSave scopes the click to the "Advanced Settings" card's
// own SectionSave button. The Advanced tab leads with the Global cards
// (Adult Mode, recheck-interval, Entity Database, Watch Folders — each with
// its own standalone Save button) before the per-mode SectionSave-batched
// fields (phash-threshold, match-confidence-threshold, identify-enabled), so
// several "Save" buttons co-render on this tab — scoping to the Advanced
// Settings card is what keeps this click unambiguous.
const clickAdvancedSectionSave = () => {
  const advancedCard = screen.getByText(/^Advanced Settings/).closest("div")!;
  fireEvent.click(within(advancedCard).getByRole("button", { name: "Save" }));
};

// clickStandaloneSave scopes a click to a single standalone-save field's own
// Save button — the same scoped-lookup pattern the pre-existing
// watch-folders-poll-interval test uses (`.closest("div.mb-3")` on the input,
// then find "Save" within that scope), needed now that recheck-interval is a
// standalone (non-SectionSave-batched) field on the Global tab with its own
// always-visible Save button, alongside Entity Database's and Watch Folders'
// own standalone Save buttons on the same tab.
const clickStandaloneSave = (input: HTMLElement) => {
  const container = input.closest("div.mb-3") as HTMLElement;
  fireEvent.click(within(container).getByRole("button", { name: "Save" }));
};

// Claude 2026-08-03: added refreshNowButtonIn (discover-scheduled-refresh
// plan §6.1-6.3, FE-2/FT-2/FT-3). Reason: DiscoverRefreshTriggerButton's
// "Refresh now" button shares the exact same accessible name as
// RecheckTriggerButton's — once DiscoverRefreshSection mounts alongside
// RecheckSection on the same Advanced-tab render, an unscoped
// `screen.getByRole("button", { name: "Refresh now" })` throws "multiple
// elements found" for either button. Scopes to the named Card's own
// wrapper div (Card's `class="mb-4 ..."`, components/ui.tsx) the same way
// clickStandaloneSave above scopes to a field's own `div.mb-3`.
// Troubleshooting: this is why the three pre-existing recheck "Refresh now"
// assertions below were changed from a bare screen.getByRole to this.
// Review if: either button's accessible name changes so they no longer collide.
const refreshNowButtonIn = (cardTitle: string): HTMLElement => {
  const card = screen.getByText(cardTitle).closest("div.mb-4") as HTMLElement;
  return within(card).getByRole("button", { name: "Refresh now" });
};

describe("Global Settings", () => {
  it("recheck-interval (Days/Hours/Minutes picker) saves to the GLOBAL /api/settings/recheck-interval as seconds", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/recheck-interval") && url.includes("/api"))
        return jsonResponse({ intervalSeconds: 0 });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    // Value 0 defaults the picker to the "Hours" unit; typing "1" there means
    // 1 hour = 3600 seconds.
    const input = (await screen.findByLabelText(
      "Monitored title refresh interval — global",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "1" } });
    // recheck-interval is a standalone (non-batched) field on the Global tab
    // now — it has its own Save button, same as watch-folders-poll-interval.
    clickStandaloneSave(input);
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

  it("clamps a negative recheck-interval amount to 0 client-side (the picker can't submit out-of-range)", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/recheck-interval") && url.includes("/api"))
        return jsonResponse({ intervalSeconds: 0 });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Monitored title refresh interval — global",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "-5" } });
    await waitFor(() => expect(input.value).toBe("0"));
    clickStandaloneSave(input);
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
    expect(put.body).toEqual({ intervalSeconds: 0 });
  });

  it("clearing the recheck-interval amount stays blank while editing, not forced back to 0", async () => {
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Monitored title refresh interval — global",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "" } });
    // Must stay blank so the operator can clear-then-retype — snapping back
    // to a visible "0" on every Backspace would make that impossible.
    expect(input.value).toBe("");
  });

  it("an emptied recheck-interval field re-syncs to 0 on blur, and still submits 0 correctly", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/recheck-interval") && url.includes("/api"))
        return jsonResponse({ intervalSeconds: 3600 });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Monitored title refresh interval — global",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("1")); // 3600s = 1 hour
    fireEvent.input(input, { target: { value: "" } });
    fireEvent.blur(input);
    expect(input.value).toBe("0");
    clickStandaloneSave(input);
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
    expect(put.body).toEqual({ intervalSeconds: 0 });
  });

  it("'Refresh now' fires the manual recheck trigger and confirms it started", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Monitored title refresh interval — global");
    // Claude 2026-08-03: was `screen.getByRole("button", { name: "Refresh
    // now" })` — DiscoverRefreshSection's own "Refresh now" button now
    // shares this accessible name, so the unscoped query throws "multiple
    // elements found" (see refreshNowButtonIn's doc comment above).
    fireEvent.click(refreshNowButtonIn("Monitored Title Refresh — global"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.includes("/api/admin/recheck/trigger"),
        ),
      ).toBe(true),
    );
    expect(
      await screen.findByText(/refresh started/i),
    ).toBeInTheDocument();
  });

  it("'Refresh now' surfaces an error instead of a false success", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/admin/recheck/trigger") &&
        (init?.method ?? "GET").toUpperCase() === "POST"
      ) {
        return new Response("prowlarr not configured", { status: 400 });
      }
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Monitored title refresh interval — global");
    // Claude 2026-08-03: was `screen.getByRole("button", { name: "Refresh
    // now" })` — see refreshNowButtonIn's doc comment above (same collision
    // as the "confirms it started" test right above this one).
    fireEvent.click(refreshNowButtonIn("Monitored Title Refresh — global"));
    expect(
      await screen.findByText(/prowlarr not configured/i),
    ).toBeInTheDocument();
  });

  // Claude 2026-08-03: added the three discover-refresh-interval tests below
  // (FT-1/FT-2/FT-3, discover-scheduled-refresh plan §6.1-6.3), modelled on
  // the recheck-interval cases directly above.
  it("discover-refresh-interval (Days/Hours/Minutes picker) saves to the GLOBAL /api/settings/discover-refresh-interval as seconds, with the custom zeroLabel copy", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/discover-refresh-interval"))
        return jsonResponse({ intervalSeconds: 0 });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Discover refresh interval — global",
    )) as HTMLInputElement;
    // Confirms the custom zeroLabel actually took effect (plan §6.2 Critic
    // pass 2 M5) — this scheduler's default is 86400 (24h), not 0, and 0
    // also clears the cache, so the shared "(0 = off, the default)" fallback
    // every other DurationSetting call site uses would assert something
    // false here.
    expect(
      screen.getByText(
        /0 = off — clears the cache and returns Discover to fetching live; default is 24h/i,
      ),
    ).toBeInTheDocument();
    fireEvent.input(input, { target: { value: "1" } });
    clickStandaloneSave(input);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/discover-refresh-interval"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/settings/discover-refresh-interval"),
    )!;
    expect(put.body).toEqual({ intervalSeconds: 3600 });
  });

  it("Discover 'Refresh now' fires the manual discover-refresh trigger and confirms it started", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Discover refresh interval — global");
    fireEvent.click(refreshNowButtonIn("Discover — background refresh"));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url.includes("/api/admin/discover-refresh/trigger"),
        ),
      ).toBe(true),
    );
    // No scoping needed here (unlike the button click above): only Discover's
    // trigger fired, so only its own <Show> renders the started text — Recheck's
    // sibling copy of this same text stays hidden since its state is still "idle".
    expect(await screen.findByText(/refresh started/i)).toBeInTheDocument();
  });

  it("Discover 'Refresh now' renders \"A refresh is already running\" on a 409, not a generic error", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/admin/discover-refresh/trigger") &&
        (init?.method ?? "GET").toUpperCase() === "POST"
      ) {
        return new Response("a discover refresh is already running", {
          status: 409,
        });
      }
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Discover refresh interval — global");
    fireEvent.click(refreshNowButtonIn("Discover — background refresh"));
    expect(
      await screen.findByText("A refresh is already running"),
    ).toBeInTheDocument();
  });

  it("watch-folders poll interval saves to the GLOBAL /api/settings/watch-folders-poll-interval as seconds", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/watch-folders-poll-interval"))
        return jsonResponse({ intervalSeconds: 0 });
      if (url.includes("/api/admin/watch-folders"))
        return jsonResponse({ enabled: false, roots: {} });
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    // Value 0 defaults the picker to the "Hours" unit; typing "1" there means
    // 1 hour = 3600 seconds — same convention as recheck-interval above.
    const input = (await screen.findByLabelText(
      "Config poll interval — global",
    )) as HTMLInputElement;
    // Zero-state help text uses this control's own zeroLabel override, NOT
    // the shared "(0 = off, the default)" fallback the other DurationSetting
    // call sites use — confirms the new zeroLabel prop actually took effect
    // for this call site.
    expect(
      screen.getByText(/0 = use the default 30-second cadence/i),
    ).toBeInTheDocument();
    fireEvent.input(input, { target: { value: "1" } });
    // WatchFoldersSection is a standalone card (not wrapped in any
    // SectionSave), so this DurationSetting has its own Save button — same
    // shape as EntityDatabaseSection's entity-sync-interval and
    // recheck-interval above. Scope to the field's own container to avoid
    // colliding with the Watch Folders enabled-toggle's own standalone Save
    // button in the same card.
    clickStandaloneSave(input);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/watch-folders-poll-interval"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/settings/watch-folders-poll-interval"),
    )!;
    expect(put.body).toEqual({ intervalSeconds: 3600 });
  });

  // The new tab's own render-through-relocation smoke test: everything that
  // moved off Advanced actually renders under the Global tab, in one place.
  it("renders the recheck-interval field, 'Refresh now' trigger, watch-folders poll-interval field, and Entity Database section", async () => {
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    expect(
      await screen.findByLabelText("Monitored title refresh interval — global"),
    ).toBeInTheDocument();
    // Claude 2026-08-03: was `screen.getByRole("button", { name: "Refresh
    // now" })` — see refreshNowButtonIn's doc comment above; this smoke
    // test's un-scoped lookup would now match both RecheckSection's and
    // DiscoverRefreshSection's identically-named buttons.
    expect(
      refreshNowButtonIn("Monitored Title Refresh — global"),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText("Config poll interval — global"),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Entity Database — background sync"),
    ).toBeInTheDocument();
  });
});

// --- Adult mode disable switch (ralplan-adult-disable-switch.md) -----------
//
// Covers: the Global tab's always-rendered master switch, the disable
// confirmation dialog's checkbox behavior (including the Critic-restored
// Cancel-fires-zero-requests criterion), live propagation to Connections/
// Library (no page reload — the load-bearing refetch requirement), Advanced's
// compound gate, the UI tab's no-dangling-tab-bar requirement, and
// EntityDatabaseSection staying visible regardless of switch state.
describe("Adult mode disable switch", () => {
  // adultModeFetch wires a STATEFUL override — GET returns the current value,
  // PUT updates it — the same "live toggle" shape AppShell's real Provider
  // exercises. This lets these tests assert on propagation through
  // renderSettingsWithAdultMode's refetch, not just on the outgoing PUT body.
  const adultModeFetch = (initialEnabled: boolean) => {
    let enabled = initialEnabled;
    let scanInterval = 300; // nonzero so "set to 0" is observable
    const override: Override = (url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/settings/adult-mode-enabled")) {
        if (method === "PUT") {
          enabled = JSON.parse(init!.body as string).enabled;
          return noContent();
        }
        return jsonResponse({ enabled });
      }
      if (url.includes("/api/settings/adult-newest-scan-interval")) {
        if (method === "PUT") {
          scanInterval = JSON.parse(init!.body as string).intervalSeconds;
          return noContent();
        }
        return jsonResponse({ intervalSeconds: scanInterval });
      }
      return undefined;
    };
    return {
      override,
      getEnabled: () => enabled,
      getScanInterval: () => scanInterval,
    };
  };

  const openDisableDialog = async () => {
    const checkbox = (await screen.findByLabelText(
      "Enable Adult mode",
    )) as HTMLInputElement;
    // The checkbox EXISTS as soon as AdultModeSection mounts, but its
    // `checked` state only reflects the real value once the resource
    // resolves — clicking before then would toggle it TO checked (firing the
    // enable path, not disable). Wait for the actual enabled=true state.
    await waitFor(() => expect(checkbox.checked).toBe(true));
    fireEvent.click(checkbox);
    await screen.findByText("Disable Adult mode?");
  };

  it("the master switch always renders when the underlying value is disabled", async () => {
    stubFetch(adultModeFetch(false).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    const checkbox = (await screen.findByLabelText(
      "Enable Adult mode",
    )) as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
  });

  it("the master switch always renders when the underlying value is enabled", async () => {
    stubFetch(adultModeFetch(true).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    const checkbox = (await screen.findByLabelText(
      "Enable Adult mode",
    )) as HTMLInputElement;
    // The checkbox exists immediately at mount; its checked state only
    // reflects the real value once the resource resolves.
    await waitFor(() => expect(checkbox.checked).toBe(true));
  });

  it("enabling is a plain, immediate single PUT with no confirmation dialog", async () => {
    const calls = stubFetch(adultModeFetch(false).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    const checkbox = (await screen.findByLabelText(
      "Enable Adult mode",
    )) as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    fireEvent.click(checkbox);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/adult-mode-enabled"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/settings/adult-mode-enabled"),
    )!;
    expect(put.body).toEqual({ enabled: true });
    expect(screen.queryByText("Disable Adult mode?")).toBeNull();
    expect(
      calls.some((c) =>
        c.url.includes("/api/settings/adult-newest-scan-interval"),
      ),
    ).toBe(false);
  });

  it("disabling opens a confirmation dialog instead of firing a request immediately", async () => {
    const calls = stubFetch(adultModeFetch(true).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    await openDisableDialog();
    expect(
      calls.some(
        (c) =>
          c.method === "PUT" &&
          c.url.includes("/api/settings/adult-mode-enabled"),
      ),
    ).toBe(false);
  });

  it("Cancel fires zero requests and changes nothing", async () => {
    const { override, getEnabled, getScanInterval } = adultModeFetch(true);
    const calls = stubFetch(override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    await openDisableDialog();
    fireEvent.click(
      screen.getByLabelText("Also stop the Adult Newest background scanner"),
    );
    const callsBeforeCancel = calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByText("Disable Adult mode?")).toBeNull();
    expect(calls.length).toBe(callsBeforeCancel);
    expect(getEnabled()).toBe(true);
    expect(getScanInterval()).toBe(300);
    expect(
      (screen.getByLabelText("Enable Adult mode") as HTMLInputElement)
        .checked,
    ).toBe(true);
  });

  it("confirming WITHOUT the scanner checkbox fires only the adult-mode-enabled PUT", async () => {
    const calls = stubFetch(adultModeFetch(true).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    await openDisableDialog();
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/adult-mode-enabled"),
        ),
      ).toBe(true),
    );
    expect(
      calls.some(
        (c) =>
          c.method === "PUT" &&
          c.url.includes("/api/settings/adult-newest-scan-interval"),
      ),
    ).toBe(false);
  });

  it("confirming WITH the scanner checkbox fires two sequential PUTs in order: adult-mode-enabled then adult-newest-scan-interval", async () => {
    const calls = stubFetch(adultModeFetch(true).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    await openDisableDialog();
    fireEvent.click(
      screen.getByLabelText("Also stop the Adult Newest background scanner"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/adult-newest-scan-interval"),
        ),
      ).toBe(true),
    );
    const putCalls = calls.filter(
      (c) =>
        c.method === "PUT" &&
        (c.url.includes("/api/settings/adult-mode-enabled") ||
          c.url.includes("/api/settings/adult-newest-scan-interval")),
    );
    expect(putCalls).toHaveLength(2);
    expect(putCalls[0]!.url).toContain("/api/settings/adult-mode-enabled");
    expect(putCalls[0]!.body).toEqual({ enabled: false });
    expect(putCalls[1]!.url).toContain(
      "/api/settings/adult-newest-scan-interval",
    );
    expect(putCalls[1]!.body).toEqual({ intervalSeconds: 0 });
  });

  it("propagates live (no page reload): disabling from Global hides stash, and takes StashDB/FansDB/TPDB with the Adult mode itself", async () => {
    stubFetch(adultModeFetch(true).override);
    renderSettingsWithAdultMode();
    // Claude 2026-08-04: stashdb/fansdb moved out of the connection rows
    // (Stage 5 Wave 5, §5.4). The Adult-only surfaces are now: tpdb as a
    // connection row under Library -> Adult, the stash-box registry panel on
    // that same tab, and stash under Advanced -> API Connections. The two
    // relocated names are asserted through the registry panel's own heading
    // rather than as connection rows, because that is where they live now.
    // expect(await screen.findByText("stashdb")).toBeInTheDocument();   // ← was
    await goToLibraryConnections("Adult");
    expect(await screen.findByText("tpdb")).toBeInTheDocument();
    expect(screen.getByText("Stash-box databases")).toBeInTheDocument();

    goToAPIConnections();
    expect(await screen.findByText("stash")).toBeInTheDocument();

    await openDisableDialog();
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(
        (screen.getByLabelText("Enable Adult mode") as HTMLInputElement)
          .checked,
      ).toBe(false),
    );

    // stash disappears from the very tab the switch was flipped on — live, with
    // no remount and no page reload. prowlarr, which is not Adult-only, stays.
    await waitFor(() => expect(screen.queryByText("stash")).toBeNull());
    expect(screen.getByText("prowlarr")).toBeInTheDocument();

    // The other three are unreachable rather than merely hidden: Library's mode
    // selector drops the Adult mode entirely, so there is no longer a route to
    // the card that holds them. Non-Adult-exclusive rows stay.
    goToSection("Library");
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Adult" })).toBeNull(),
    );
    expect(await screen.findByText("tmdb")).toBeInTheDocument();
    expect(screen.queryByText("tpdb")).toBeNull();
    // The registry panel goes with the mode too — it is rendered inside the
    // Adult branch, so losing the mode loses the whole card, not just its rows.
    expect(screen.queryByText("Stash-box databases")).toBeNull();
  });

  it("mode-fallback: a screen with Adult selected falls back to Movies when disabled from elsewhere", async () => {
    stubFetch(adultModeFetch(true).override);
    renderSettingsWithAdultMode();
    goToSection("Library");
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText(/no naming preferences/);

    goToSection("Advanced");
    await openDisableDialog();
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(
        (screen.getByLabelText("Enable Adult mode") as HTMLInputElement)
          .checked,
      ).toBe(false),
    );

    goToSection("Library");
    // Falls back to Movies — the naming-preferences panel (Movies/Series
    // only) is back, and Adult is no longer a selectable tab.
    expect(
      await screen.findByText("File/folder naming (Movies)"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Adult")).toBeNull();
  });

  it("EntityDatabaseSection stays visible regardless of switch state", async () => {
    stubFetch(adultModeFetch(false).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    expect(
      await screen.findByText("Entity Database — background sync"),
    ).toBeInTheDocument();
  });

  it("Advanced's Adult-only IdentifyEnabledSetting never renders when disabled", async () => {
    stubFetch(adultModeFetch(false).override);
    renderSettingsWithAdultMode();
    goToSection("Advanced");
    await screen.findByText(/^Advanced Settings/);
    expect(screen.queryByText("Adult")).toBeNull();
    expect(
      screen.queryByLabelText("Adult phash-first identification enabled"),
    ).toBeNull();
  });

  it("UI tab renders no dangling tab bar when disabled — SliderAdminSection shows directly, no Mainstream/Adult pills", async () => {
    stubFetch(adultModeFetch(false).override);
    renderSettingsWithAdultMode();
    goToSection("UI");
    expect(
      await screen.findByText("Custom Discover sliders"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mainstream" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Adult" })).toBeNull();
  });
});

describe("Advanced Settings", () => {
  it("phash-threshold above 256 disables the section Save button (blocked before clicking, not after)", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Dedup phash similarity threshold (0–256)",
    )) as HTMLInputElement;
    // Scoped to the Advanced Settings card's own SectionSave button — the
    // Watch Folders card below also renders its own (always-visible,
    // non-disabling) "Save" button, so a bare "Save" query is no longer
    // unique on this tab.
    const advancedCard = screen.getByText(/^Advanced Settings/).closest("div")!;
    const saveButton = within(advancedCard).getByRole("button", {
      name: "Save",
    }) as HTMLButtonElement;
    fireEvent.input(input, { target: { value: "300" } });
    await waitFor(() => expect(saveButton.disabled).toBe(true));
    // A disabled button ignores clicks at the DOM level — confirms this
    // isn't just visually greyed out, nothing fires even if clicked.
    fireEvent.click(saveButton);
    expect(
      calls.some(
        (c) => c.method === "PUT" && c.url.includes("/phash-threshold"),
      ),
    ).toBe(false);
    // Fixing the value re-enables the button and the save goes through.
    fireEvent.input(input, { target: { value: "12" } });
    await waitFor(() => expect(saveButton.disabled).toBe(false));
    fireEvent.click(saveButton);
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url.includes("/phash-threshold"),
        ),
      ).toBe(true),
    );
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
      "Dedup phash similarity threshold (0–256)",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("8"));
    fireEvent.input(input, { target: { value: "12" } });
    clickAdvancedSectionSave();
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
      await screen.findByLabelText("Rename match candidate count (1–20)"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByText("Adult"));
    await waitFor(() =>
      expect(
        screen.queryByLabelText("Rename match candidate count (1–20)"),
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
    await screen.findByLabelText("Rename match candidate count (1–20)");
    expect(
      screen.queryByLabelText("Adult phash-first identification enabled"),
    ).toBeNull();
    fireEvent.click(screen.getByText("Adult"));
    const toggle = (await screen.findByLabelText(
      "Adult phash-first identification enabled",
    )) as HTMLInputElement;
    fireEvent.change(toggle, { target: { checked: false } });
    // The Advanced tab leads with the Global cards (Adult Mode, Monitored
    // Title Refresh, Entity Database, Watch Folders — each with its own
    // standalone Save button) before the per-mode SectionSave-batched fields
    // (phash-threshold, match-confidence-threshold, identify-enabled), so a
    // bare "Save" role query is NOT unique on this tab — scope to the Advanced
    // Settings card, same as clickAdvancedSectionSave.
    const advancedCard = screen.getByText(/^Advanced Settings/).closest("div")!;
    fireEvent.click(within(advancedCard).getByRole("button", { name: "Save" }));
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

  it("phash-threshold keeps an in-progress edit when the GET resolves late (regression)", async () => {
    let resolvePhash!: (threshold: number) => void;
    const phashPromise = new Promise<number>((resolve) => {
      resolvePhash = resolve;
    });
    stubFetch((url) => {
      if (url.includes("/movies/phash-threshold") && url.includes("/api"))
        return phashPromise.then((threshold) =>
          jsonResponse({ threshold }),
        );
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const input = (await screen.findByLabelText(
      "Dedup phash similarity threshold (0–256)",
    )) as HTMLInputElement;
    const advancedCard = screen.getByText(/^Advanced Settings/).closest("div")!;
    const saveButton = within(advancedCard).getByRole("button", {
      name: "Save",
    }) as HTMLButtonElement;
    fireEvent.input(input, { target: { value: "12" } });
    expect(input.value).toBe("12");
    await waitFor(() => expect(saveButton.disabled).toBe(false));
    resolvePhash(8);
    await waitFor(() => expect(input.value).toBe("12"));
    expect(saveButton.disabled).toBe(false);
  });

  it("identify-enabled keeps an in-progress toggle when the GET resolves late (regression)", async () => {
    let resolveIdentify!: (enabled: boolean) => void;
    const identifyPromise = new Promise<boolean>((resolve) => {
      resolveIdentify = resolve;
    });
    stubFetch((url) => {
      if (url.includes("/adult/identify-enabled") && url.includes("/api"))
        return identifyPromise.then((enabled) =>
          jsonResponse({ enabled }),
        );
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Rename match candidate count (1–20)");
    fireEvent.click(screen.getByText("Adult"));
    const toggle = (await screen.findByLabelText(
      "Adult phash-first identification enabled",
    )) as HTMLInputElement;
    const advancedCard = screen.getByText(/^Advanced Settings/).closest("div")!;
    const saveButton = within(advancedCard).getByRole("button", {
      name: "Save",
    }) as HTMLButtonElement;
    fireEvent.change(toggle, { target: { checked: false } });
    expect(toggle.checked).toBe(false);
    await waitFor(() => expect(saveButton.disabled).toBe(false));
    resolveIdentify(true);
    await waitFor(() => expect(toggle.checked).toBe(false));
    expect(saveButton.disabled).toBe(false);
  });
});

// --- DurationSetting resync race (isolated) --------------------------------

describe("DurationSetting — resync race", () => {
  it("keeps an in-progress edit when value() resolves late (regression)", async () => {
    const save = vi.fn().mockResolvedValue(undefined);
    let setServerVal: ((v: number) => void) | undefined;
    const Harness = () => {
      const [val, setVal] = createSignal<number | undefined>(undefined);
      setServerVal = setVal;
      return (
        <DurationSetting
          id="race-test"
          label="Race interval"
          help="help"
          value={() => val()}
          onSave={save}
        />
      );
    };
    render(() => <Harness />);
    const input = (await screen.findByLabelText(
      "Race interval",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "5" } });
    expect(input.value).toBe("5");
    setServerVal!(7200); // 2 hours — would reseed to amount 2 if dirty guard failed
    await waitFor(() => expect(input.value).toBe("5"));
    expect(save).not.toHaveBeenCalled();
  });
});

// --- Section tabs (layout: one section on screen at a time) ----------------

describe("Section tabs", () => {
  it("defaults to Library and hides every other section", async () => {
    stubFetch();
    renderSettings();
    // Library is the default tab now that Connections is gone: its root-folder
    // field is on screen at mount, alongside the metadata-source connection
    // rows that moved into it.
    expect(
      await screen.findByLabelText("Library root folder"),
    ).toBeInTheDocument();
    expect(await screen.findByLabelText("tmdb API key")).toBeInTheDocument();
    // The signature control of each other section is absent.
    expect(screen.queryByText("Switch to this mode")).toBeNull(); // Auth
    expect(screen.queryByPlaceholderText(/qwen2.5vl/)).toBeNull(); // AI
    expect(screen.queryByLabelText("prowlarr URL")).toBeNull(); // Advanced -> API
    expect(screen.queryByText("Subscriptions")).toBeNull(); // Usenet
    expect(
      screen.queryByLabelText("Monitored title refresh interval — global"),
    ).toBeNull(); // Global
  });

  it("has no Connections tab at all", async () => {
    stubFetch();
    renderSettings();
    await screen.findByLabelText("Library root folder");
    expect(screen.queryByRole("button", { name: "Connections" })).toBeNull();
  });

  it("Download is its own top-level tab, with Usenet and Torrent as sub-tabs", async () => {
    stubFetch();
    renderSettings();
    // Usenet and Torrent are no longer top-level section tabs; Download took
    // the slot Usenet held and Torrent's slot is gone entirely.
    expect(sectionTabBar().queryByRole("button", { name: "Usenet" })).toBeNull();
    expect(sectionTabBar().queryByRole("button", { name: "Torrent" })).toBeNull();
    expect(sectionTabBar().getByRole("button", { name: "Download" })).toBeInTheDocument();

    // Usenet is the default sub-tab, so clicking Download alone renders it.
    goToSection("Download");
    expect(await screen.findByText("Subscriptions")).toBeInTheDocument();
    expect(screen.getByText("Auto-grab")).toBeInTheDocument();
    // Library is no longer mounted.
    expect(screen.queryByLabelText("Library root folder")).toBeNull();

    // The Torrent sub-tab swaps the panel. Asserted on a REAL rendered control
    // rather than the "Torrent behavior" Card title: the title would render even
    // while the config fetch is unresolved and the body says "Loading torrent
    // settings…", making the assertion hollow (defaultGet answers
    // /api/downloader/config for exactly this reason).
    fireEvent.click(screen.getByRole("button", { name: "Torrent" }));
    expect(await screen.findByLabelText("Staging directory")).toBeInTheDocument();
    // <Switch> unmounts the inactive sub-tab, so Usenet's content is gone.
    expect(screen.queryByText("Subscriptions")).toBeNull();
  });

  it("UI tab shows the Discover subsection with Mainstream/Adult sub-tabs, plus Trakt, hiding Library", async () => {
    stubFetch();
    renderSettings();
    goToSection("UI");
    // Mainstream is the default inner sub-tab: the slider editor is on screen.
    expect(await screen.findByText("+ New slider")).toBeInTheDocument();
    // Both inner sub-tabs render; the Discover subsection heading is present.
    expect(screen.getByText("Discover")).toBeInTheDocument();
    expect(screen.getByText("Mainstream")).toBeInTheDocument();
    expect(screen.getByText("Adult")).toBeInTheDocument();
    // Trakt moved here, next to the other Discover-shaping controls.
    expect(screen.getByLabelText("Trakt client ID")).toBeInTheDocument();
    // Library is no longer mounted.
    expect(screen.queryByLabelText("Library root folder")).toBeNull();
  });

  it("UI tab's inner Adult sub-tab swaps the slider editor for the Adult-newest-row editor", async () => {
    stubFetch();
    renderSettings();
    goToSection("UI");
    await screen.findByText("+ New slider");
    // Switching the inner sub-tab to Adult swaps in the Adult-newest-row editor
    // and drops the Mainstream slider editor.
    fireEvent.click(screen.getByText("Adult"));
    expect(await screen.findByText("+ New row")).toBeInTheDocument();
    expect(screen.queryByText("+ New slider")).toBeNull();
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
    expect(await screen.findByLabelText("Ollama model")).toBeInTheDocument();
    expect(screen.queryByText("Switch to this mode")).toBeNull();
    // The connection sub-tables live on this tab now — the selected provider's
    // fields (ollama by default) and the always-visible Brave row (its URL
    // field hidden, fixed-URL service — API Key is the presence signal).
    expect(await screen.findByLabelText("ollama URL")).toBeInTheDocument();
    expect(screen.getByLabelText("brave API key")).toBeInTheDocument();
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
    // Global's field is NOT on this tab.
    expect(
      screen.queryByLabelText("Monitored title refresh interval — global"),
    ).toBeNull();
  });

  it("Advanced tab shows the Advanced panel and hides Library panels", async () => {
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    // phash-threshold is the signature per-mode Advanced field; asserting the
    // Library root folder is absent confirms Library's panels didn't leak here.
    expect(
      await screen.findByLabelText(
        "Dedup phash similarity threshold (0–256)",
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
      screen.queryByLabelText("Rename match candidate count (1–20)"),
    ).toBeNull();
  });
});

// --- inner tab bars must not steal the shell's tab slot --------------------
//
// The regression this guards: Settings' own SECTION_TABS register with the app
// shell's single global tab slot (ScreenTabsContext). Settings has TWO
// second-level tab bars — the UI tab's Mainstream/Adult switch and (since
// 2026-08-10) the Download tab's Usenet/Torrent switch. Both are deliberately a
// plain ScreenTabBar, NOT ScreenTabs —
// if it were ScreenTabs it would call the shell's registration setter and
// REPLACE the section tabs with Mainstream/Adult, wiping Settings' top-level nav.
// A bare render() can't catch this (with no shell context ScreenTabs falls back
// to inline and never registers), so this suite mounts Settings inside a
// ScreenTabsContext.Provider exactly the way AppShell does — rendering the ONE
// registered tab set in the shell's slot — and asserts that slot keeps holding
// the section tabs even after the inner sub-tab is clicked.

describe("second-level sub-tabs do not hijack the shell tab slot", () => {
  // renderSettingsInShell mirrors AppShell's ShellRoot: it provides the
  // ScreenTabsContext setter and renders whatever tab set is registered inside a
  // testid'd container (the shell's one slot). Assertions scoped to that
  // container see ONLY the shell-registered tabs, never the body's inline bars.
  const renderSettingsInShell = () => {
    const Harness = () => {
      const [reg, setReg] = createSignal<ScreenTabsRegistration | null>(null);
      return (
        <ScreenTabsContext.Provider value={setReg}>
          <Show when={reg()}>
            {(r) => (
              <div data-testid="shell-slot">
                <ScreenTabBar
                  tabs={r().tabs}
                  current={r().current}
                  onSelect={r().onSelect}
                  trailing={r().trailing}
                />
              </div>
            )}
          </Show>
          <Settings onReboot={() => {}} />
        </ScreenTabsContext.Provider>
      );
    };
    return render(() => <Harness />);
  };

  it("keeps the section tabs in the shell slot when the inner Mainstream/Adult sub-tab changes", async () => {
    stubFetch();
    const { getByTestId } = renderSettingsInShell();
    const shellSlot = () => within(getByTestId("shell-slot"));

    // Settings registers SECTION_TABS with the shell slot at mount: the section
    // tabs — NOT any Mainstream/Adult — are what the shell draws. Scoped to the
    // shell slot specifically, because the body renders inner tab bars of its
    // own (the UI tab's Mainstream/Adult bar, the Library/Advanced mode
    // selector) whose buttons an unscoped query could pick up.
    expect(
      await shellSlot().findByRole("button", { name: "Download" }),
    ).toBeInTheDocument();
    expect(shellSlot().getByText("UI")).toBeInTheDocument();
    expect(shellSlot().getByText("Library")).toBeInTheDocument();
    // AI's promotion out of the old Connections nesting is a claim about the
    // SHELL-registered tab set specifically, not just about some button being
    // on screen — so it is asserted here rather than only in the body.
    expect(shellSlot().getByText("AI")).toBeInTheDocument();
    expect(shellSlot().queryByText("Connections")).toBeNull();
    expect(shellSlot().queryByText("Mainstream")).toBeNull();

    // Navigate to the UI tab via the shell-registered section tab. Its inner
    // Mainstream/Adult bar mounts in the body, NOT the shell slot.
    fireEvent.click(shellSlot().getByText("UI"));
    await screen.findByText("+ New slider");
    expect(shellSlot().getByText("Library")).toBeInTheDocument();
    expect(shellSlot().queryByText("Mainstream")).toBeNull();

    // The load-bearing click: switching the INNER sub-tab must not touch the
    // shell registration. If UISection used ScreenTabs, this click would replace
    // the shell slot's contents with Mainstream/Adult and drop the section tabs.
    fireEvent.click(screen.getByText("Adult"));
    await screen.findByText("+ New row");
    // Shell slot still holds the section tabs, unchanged...
    expect(shellSlot().getByText("UI")).toBeInTheDocument();
    expect(shellSlot().getByText("Library")).toBeInTheDocument();
    expect(shellSlot().getByText("Download")).toBeInTheDocument();
    // ...and never adopted the inner sub-tab labels.
    expect(shellSlot().queryByText("Mainstream")).toBeNull();
  });

  // The Download tab's Usenet/Torrent switch is the SECOND second-level tab bar
  // in Settings, added 2026-08-10 when the standalone Usenet and Torrent tabs
  // were folded into one Download tab. It is a plain ScreenTabBar for exactly
  // the same reason UISection's is, so it needs exactly the same guard: this is
  // the only test that would catch a future refactor swapping it for
  // ScreenTabs, which would wipe Settings' top-level nav the moment Download
  // mounts.
  it("keeps the section tabs in the shell slot when the inner Usenet/Torrent sub-tab changes", async () => {
    stubFetch();
    const { getByTestId } = renderSettingsInShell();
    const shellSlot = () => within(getByTestId("shell-slot"));

    // The shell slot holds Download, and never the inner sub-tab labels.
    expect(
      await shellSlot().findByRole("button", { name: "Download" }),
    ).toBeInTheDocument();
    expect(shellSlot().queryByText("Usenet")).toBeNull();
    expect(shellSlot().queryByText("Torrent")).toBeNull();

    // Navigate to Download via the shell-registered section tab. Its inner
    // Usenet/Torrent bar mounts in the body, NOT the shell slot.
    fireEvent.click(shellSlot().getByText("Download"));
    await screen.findByText("Subscriptions");
    expect(shellSlot().getByText("Library")).toBeInTheDocument();
    expect(shellSlot().queryByText("Usenet")).toBeNull();

    // The load-bearing click: switching the INNER sub-tab must not touch the
    // shell registration. Asserted against a real rendered Torrent control, not
    // the card title, so an unresolved config fetch can't make it pass hollow.
    fireEvent.click(screen.getByRole("button", { name: "Torrent" }));
    expect(await screen.findByLabelText("Staging directory")).toBeInTheDocument();
    // Shell slot still holds the section tabs, unchanged...
    expect(shellSlot().getByText("Library")).toBeInTheDocument();
    expect(shellSlot().getByText("Download")).toBeInTheDocument();
    expect(shellSlot().getByText("UI")).toBeInTheDocument();
    // ...and still never adopted the inner sub-tab labels.
    expect(shellSlot().queryByText("Usenet")).toBeNull();
    expect(shellSlot().queryByText("Torrent")).toBeNull();
  });
});

// --- No bulk actions (Acceptance Criterion 6) ------------------------------

// --- Download > Usenet sub-tab: multi-subscription CRUD (AC 10) ------------
//
// The Usenet subscriptions half of the `service_connections` registry
// (migration 0053), which replaced the singleton `connections` PRIMARY
// KEY(service) shape for the two service classes an operator can have more than
// one of. What these tests are really pinning down is that "more than one" is
// real: several subscriptions co-exist, each with its OWN draft state, its own
// PUT to its own id route, and its own Delete — never a merged payload and
// never a shared row.

describe("Usenet subscriptions — multi-subscription CRUD", () => {
  const twoSubscriptions = [
    usenetConn(),
    usenetConn({
      id: 2,
      label: "Newshosting",
      host: "news.newshosting.com",
      port: 119,
      tls: false,
      username: "w2",
      maxConns: 0,
      hasSecret: false,
      secretSuffix: "",
    }),
  ];

  it("renders every configured subscription, each seeded from its own row", async () => {
    stubFetch(registryFetch(twoSubscriptions));
    renderSettings();
    goToDownloadSubTab("Usenet");
    const host1 = (await screen.findByLabelText(
      "Subscription 1 host",
    )) as HTMLInputElement;
    const host2 = screen.getByLabelText(
      "Subscription 2 host",
    ) as HTMLInputElement;
    expect(host1.value).toBe("news.eweka.nl");
    expect(host2.value).toBe("news.newshosting.com");
    // The full field set is what earns this page its own tab rather than a
    // ConnectionRow — so assert the fields ConnectionRow does NOT have, per row.
    expect(
      (screen.getByLabelText("Subscription 1 port") as HTMLInputElement).value,
    ).toBe("563");
    expect(
      (screen.getByLabelText("Subscription 2 port") as HTMLInputElement).value,
    ).toBe("119");
    expect(
      (screen.getByLabelText("Subscription 1 TLS") as HTMLInputElement).checked,
    ).toBe(true);
    expect(
      (screen.getByLabelText("Subscription 2 TLS") as HTMLInputElement).checked,
    ).toBe(false);
    expect(
      (screen.getByLabelText("Subscription 1 max connections") as HTMLInputElement)
        .value,
    ).toBe("8");
    expect(
      (screen.getByLabelText("Subscription 1 username") as HTMLInputElement).value,
    ).toBe("wade");
  });

  it("says there is no priority order, and offers no priority field or reorder control", async () => {
    // Usenet.tsx's #1 "must never grow" invariant: every enabled subscription is
    // tried with NO priority ordering, so a ranked chain would be a behaviour
    // change, not a convenience. The explanatory copy is asserted alongside the
    // absent controls because the copy is what stops a future session from
    // "fixing" the missing reorder buttons.
    stubFetch(registryFetch(twoSubscriptions));
    renderSettings();
    goToDownloadSubTab("Usenet");
    await screen.findByLabelText("Subscription 1 host");
    expect(screen.getByText(/there is no priority order/i)).toBeInTheDocument();
    // No priority/order form control anywhere on the page...
    expect(screen.queryByLabelText(/priority/i)).toBeNull();
    expect(screen.queryByLabelText(/sort order/i)).toBeNull();
    // ...and no reorder affordance, under any of the shapes one would take.
    for (const name of [
      /move up/i,
      /move down/i,
      /reorder/i,
      /^↑$/,
      /^↓$/,
    ] as const) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });

  it("Add subscription POSTs the whole NNTP field set with a plain secret", async () => {
    const calls = stubFetch(registryFetch([]));
    renderSettings();
    goToDownloadSubTab("Usenet");
    // The empty-state copy confirms the list resolved before the Add form opens.
    expect(
      await screen.findByText("No subscriptions configured yet."),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Add subscription" }),
    );
    fireEvent.input(await screen.findByLabelText("New subscription label"), {
      target: { value: "Newshosting" },
    });
    fireEvent.input(screen.getByLabelText("New subscription host"), {
      target: { value: "news.newshosting.com" },
    });
    // Add's own "Add subscription" button replaces the card's opener (they are
    // the two branches of one <Show>), so this stays unambiguous.
    fireEvent.click(screen.getByRole("button", { name: "Add subscription" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url.endsWith("/api/service-connections"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.url.endsWith("/api/service-connections"),
    )!;
    // Create takes a PLAIN secret — a brand-new row has no stored one to
    // preserve, so "" here means "no password", never "clear the stored one".
    expect(post.body).toEqual({
      kind: "usenet",
      provider: "nntp",
      label: "Newshosting",
      enabled: true,
      host: "news.newshosting.com",
      port: 563,
      tls: true,
      maxConns: 0,
      username: "",
      secret: "",
    });
  });

  it("the batched Save PUTs only the edited subscription, to its own id route", async () => {
    const calls = stubFetch(registryFetch(twoSubscriptions));
    renderSettings();
    goToDownloadSubTab("Usenet");
    const host1 = (await screen.findByLabelText(
      "Subscription 1 host",
    )) as HTMLInputElement;
    fireEvent.input(host1, { target: { value: "news2.eweka.nl" } });
    // One Save button on this tab drives both cards (see clickSectionSave).
    clickSectionSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    const put = registryPuts(calls)[0]!;
    expect(put.url).toContain("/api/service-connections/1");
    expect(put.body).toEqual({
      kind: "usenet",
      provider: "nntp",
      label: "Eweka",
      enabled: true,
      host: "news2.eweka.nl",
      port: 563,
      tls: true,
      maxConns: 8,
      username: "wade",
    });
    // Subscription 2 was untouched, so it never fired at all.
    expect(
      calls.some((c) => c.url.includes("/api/service-connections/2")),
    ).toBe(false);
  });

  it("Delete removes exactly the row it was clicked in", async () => {
    const calls = stubFetch(registryFetch(twoSubscriptions));
    renderSettings();
    goToDownloadSubTab("Usenet");
    await screen.findByLabelText("Subscription 1 host");
    const host2 = screen.getByLabelText("Subscription 2 host");
    // Delete/Test render once PER ROW — the scoped lookup is the whole point.
    fireEvent.click(registryRow(host2).getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url.endsWith("/api/service-connections/2"),
        ),
      ).toBe(true),
    );
    expect(
      calls.some(
        (c) =>
          c.method === "DELETE" && c.url.endsWith("/api/service-connections/1"),
      ),
    ).toBe(false);
  });
});

// --- Usenet subscriptions: three-state secret through the UI ---------------
//
// The registry's own copy of Guardrail #5 / Acceptance Criterion 5, asserted
// exactly the way the singleton Connections table's version above is: the
// property must be ABSENT from the parsed request body, not merely undefined.
// A configured subscription's password input is blank (the stored secret is
// never sent back to the client), so an untouched blank that reached the wire
// as `secret: ""` would silently WIPE a working provider password.

describe("Usenet subscriptions — three-state secret semantics through the UI", () => {
  it("saving after editing ONLY the host OMITS secret from the PUT body", async () => {
    const calls = stubFetch(registryFetch([usenetConn()]));
    renderSettings();
    goToDownloadSubTab("Usenet");
    const host = (await screen.findByLabelText(
      "Subscription 1 host",
    )) as HTMLInputElement;
    // The stored password is redacted to hasSecret/secretSuffix, so the input
    // shows the "unchanged (••••6789)" placeholder and holds no value.
    expect(
      (screen.getByLabelText("Subscription 1 password") as HTMLInputElement)
        .placeholder,
    ).toContain("6789");
    fireEvent.input(host, { target: { value: "news2.eweka.nl" } });
    clickSectionSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    expect(registryPuts(calls)[0]!.body).not.toHaveProperty("secret");
  });

  it("typing a password sends it as the replacement secret", async () => {
    const calls = stubFetch(registryFetch([usenetConn()]));
    renderSettings();
    goToDownloadSubTab("Usenet");
    const secret = await screen.findByLabelText("Subscription 1 password");
    fireEvent.input(secret, { target: { value: "rotated-pass" } });
    clickSectionSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    expect(
      (registryPuts(calls)[0]!.body as { secret?: string }).secret,
    ).toBe("rotated-pass");
  });

  it("typing then clearing the password sends an explicit empty secret (the deliberate clear)", async () => {
    const calls = stubFetch(registryFetch([usenetConn()]));
    renderSettings();
    goToDownloadSubTab("Usenet");
    const secret = await screen.findByLabelText("Subscription 1 password");
    fireEvent.input(secret, { target: { value: "x" } });
    fireEvent.input(secret, { target: { value: "" } });
    clickSectionSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    const body = registryPuts(calls)[0]!.body as { secret?: string };
    // Present-and-empty, which is the backend's "clear it" signal — the one
    // case where "" on the wire is correct rather than catastrophic.
    expect(body).toHaveProperty("secret");
    expect(body.secret).toBe("");
  });

  it("a subscription with no stored secret sends a blank one, so a no-auth provider can still save", async () => {
    const calls = stubFetch(
      registryFetch([usenetConn({ hasSecret: false, secretSuffix: "" })]),
    );
    renderSettings();
    goToDownloadSubTab("Usenet");
    const host = (await screen.findByLabelText(
      "Subscription 1 host",
    )) as HTMLInputElement;
    fireEvent.input(host, { target: { value: "news2.eweka.nl" } });
    clickSectionSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    const body = registryPuts(calls)[0]!.body as { secret?: string };
    expect(body).toHaveProperty("secret");
    expect(body.secret).toBe("");
  });
});

// --- Download > Usenet sub-tab: the auto-grab toggle (AC 12) ---------------
//
// The UI surface of a deliberate staged-for-approval exception, so the copy is
// as load-bearing as the behaviour and gets asserted alongside it.

describe("Usenet auto-grab toggle", () => {
  // goToUsenet returns the toggle itself. Note the checkbox EXISTS the moment
  // AutoGrabCard mounts, but its checked/disabled state only reflects the
  // fetched value once onMount's promise settles — so every caller below waits
  // on the specific state it cares about rather than reading it immediately.
  const goToUsenet = async () => {
    goToDownloadSubTab("Usenet");
    return (await screen.findByLabelText(
      "Enable auto-grab",
    )) as HTMLInputElement;
  };

  it("is OFF by default", async () => {
    stubFetch();
    renderSettings();
    const cb = await goToUsenet();
    // Not merely unchecked — also NOT disabled, which is what distinguishes a
    // real "off" from the load-error state further down (where it is also
    // unchecked, but for an entirely different reason).
    await waitFor(() => expect(cb.disabled).toBe(false));
    expect(cb.checked).toBe(false);
  });

  it("reflects a stored ON value", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/settings/usenet-autograb-enabled") &&
        (init?.method ?? "GET").toUpperCase() === "GET"
      )
        return jsonResponse({ enabled: true });
      return undefined;
    });
    renderSettings();
    const cb = await goToUsenet();
    await waitFor(() => expect(cb.checked).toBe(true));
  });

  it("turning it on fires exactly ONE PUT and never touches the retry-interval route", async () => {
    // The cadence coupling (on -> 86400s, off -> 0) is server-side, inside this
    // same PUT, precisely so the toggle and the interval can't disagree. A
    // second client request to /api/settings/usenet-retry-interval would break
    // that single source of truth — so its ABSENCE is the real assertion here.
    const calls = stubFetch();
    renderSettings();
    const cb = await goToUsenet();
    await waitFor(() => expect(cb.disabled).toBe(false));
    fireEvent.click(cb);
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/usenet-autograb-enabled"),
        ),
      ).toBe(true),
    );
    const puts = calls.filter(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/settings/usenet-autograb-enabled"),
    );
    expect(puts).toHaveLength(1);
    expect(puts[0]!.body).toEqual({ enabled: true });
    expect(
      calls.some((c) => c.url.includes("/api/settings/usenet-retry-interval")),
    ).toBe(false);
  });

  it("slot fields PUT usenet-autograb-slots on save", async () => {
    const calls = stubFetch();
    renderSettings();
    await goToUsenet();
    const cycle = (await screen.findByLabelText(
      "Usenet slots per cycle",
    )) as HTMLInputElement;
    await waitFor(() => expect(cycle.value).toBe("20"));
    fireEvent.input(cycle, { target: { value: "40" } });
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/usenet-autograb-slots") &&
            JSON.stringify(c.body).includes('"perCycle":40'),
        ),
      ).toBe(true),
    );
  });

  it("turning it back off fires the off PUT, same single-request shape", async () => {
    const calls = stubFetch((url, init) => {
      if (
        url.includes("/api/settings/usenet-autograb-enabled") &&
        (init?.method ?? "GET").toUpperCase() === "GET"
      )
        return jsonResponse({ enabled: true });
      return undefined;
    });
    renderSettings();
    const cb = await goToUsenet();
    await waitFor(() => expect(cb.checked).toBe(true));
    fireEvent.click(cb);
    clickSectionSave();
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/usenet-autograb-enabled"),
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) =>
        c.method === "PUT" &&
        c.url.includes("/api/settings/usenet-autograb-enabled"),
    )!;
    expect(put.body).toEqual({ enabled: false });
    expect(
      calls.some((c) => c.url.includes("/api/settings/usenet-retry-interval")),
    ).toBe(false);
  });

  it("a failed setting fetch disables the toggle and surfaces the real error, leaving it off", async () => {
    // loadError is scoped to THIS card: the subscriptions list beside it must
    // still render, and the toggle must show its honest default (off) rather
    // than an optimistic or indeterminate state. The route itself is always
    // registered (internal/api/handler.go), so this simulates a generic
    // transient failure — not a "feature doesn't exist yet" response — and the
    // UI is expected to surface that real error message, not a canned one.
    stubFetch((url, init) => {
      if (
        url.includes("/api/settings/usenet-autograb-enabled") &&
        (init?.method ?? "GET").toUpperCase() === "GET"
      )
        return errorResponse(500, "temporary failure in name resolution");
      if (url.includes("/api/service-connections") && !url.includes("/test"))
        return jsonResponse([usenetConn()]);
      return undefined;
    });
    renderSettings();
    const cb = await goToUsenet();
    await waitFor(() => expect(cb.disabled).toBe(true));
    expect(cb.checked).toBe(false);
    expect(
      screen.getByText(/temporary failure in name resolution/i),
    ).toBeInTheDocument();
    // The sibling card is unaffected.
    expect(screen.getByLabelText("Subscription 1 host")).toBeInTheDocument();
  });

  it("states plainly that it downloads without review, and that the retry loop needs a restart", async () => {
    stubFetch();
    renderSettings();
    await goToUsenet();
    // The no-review sentence is the documented obligation for this exception:
    // it must not be softened into "automatically" or "hands-free".
    expect(
      screen.getByText(/SAK grabs it without asking you first/i),
    ).toBeInTheDocument();
    // ...and the honest caveat that toggling on does NOT start the 24-hour
    // retry loop in the running process.
    expect(
      screen.getByText(/only starts after the next restart/i),
    ).toBeInTheDocument();
  });
});

// --- Media player registry (AC 7 + AC 8) -----------------------------------
//
// The player half of the same registry. Two things are specific to it and
// covered nowhere else: the FIXED Jellyfin/Emby/Plex provider enum (never a
// freeform type field), and per-connection mode assignment, which needs a
// SECOND request because ServiceConnectionUpdateRequest has no modes field and
// the backend's Store.Update ignores incoming modes by contract.

describe("Media players — registry CRUD", () => {
  const threePlayers = [
    playerConn(),
    playerConn({
      id: 8,
      provider: "emby",
      label: "Bedroom",
      url: "http://emby:8096",
      modes: ["series"],
    }),
    playerConn({
      id: 9,
      provider: "plex",
      label: "Office",
      url: "http://plex:32400",
      modes: ["movies", "adult"],
      hasSecret: false,
      secretSuffix: "",
    }),
  ];

  it("renders one row per player with its own provider and mode assignment", async () => {
    stubFetch(registryFetch(threePlayers));
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Player 7 URL");
    // All three providers coexist — the registry is many-rows, not one-per-kind.
    expect(
      (screen.getByLabelText("Player 7 provider") as HTMLSelectElement).value,
    ).toBe("jellyfin");
    expect(
      (screen.getByLabelText("Player 8 provider") as HTMLSelectElement).value,
    ).toBe("emby");
    expect(
      (screen.getByLabelText("Player 9 provider") as HTMLSelectElement).value,
    ).toBe("plex");
    // Mode assignment is many-to-many with no exclusivity: Movies has two
    // players here (7 and 9) and Adult has one, which must not warn or conflict.
    expect(
      (screen.getByLabelText("Player 7 Movies") as HTMLInputElement).checked,
    ).toBe(true);
    expect(
      (screen.getByLabelText("Player 7 Series") as HTMLInputElement).checked,
    ).toBe(false);
    expect(
      (screen.getByLabelText("Player 8 Series") as HTMLInputElement).checked,
    ).toBe(true);
    expect(
      (screen.getByLabelText("Player 9 Movies") as HTMLInputElement).checked,
    ).toBe(true);
    expect(
      (screen.getByLabelText("Player 9 Adult") as HTMLInputElement).checked,
    ).toBe(true);
  });

  it("Add player POSTs kind=player with the chosen provider and its modes in ONE request", async () => {
    const calls = stubFetch(registryFetch([]));
    renderSettings();
    goToSection("Advanced");
    expect(
      await screen.findByText("No media players configured yet."),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add player" }));
    fireEvent.input(await screen.findByLabelText("New player label"), {
      target: { value: "Basement" },
    });
    fireEvent.change(screen.getByLabelText("New player provider"), {
      target: { value: "emby" },
    });
    fireEvent.input(screen.getByLabelText("New player URL"), {
      target: { value: "http://emby:8096" },
    });
    fireEvent.click(screen.getByLabelText("New player Movies"));
    fireEvent.click(screen.getByRole("button", { name: "Add player" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url.endsWith("/api/service-connections"),
        ),
      ).toBe(true),
    );
    const post = calls.find(
      (c) => c.method === "POST" && c.url.endsWith("/api/service-connections"),
    )!;
    // modes is honoured on CREATE only; every later change goes through the
    // /modes route (see the two-request tests below).
    expect(post.body).toEqual({
      kind: "player",
      provider: "emby",
      label: "Basement",
      enabled: true,
      url: "http://emby:8096",
      secret: "",
      modes: ["movies"],
    });
    // No second request: create doesn't need the /modes route.
    expect(registryModePuts(calls)).toHaveLength(0);
  });

  it("Add player refuses a blank URL client-side, firing no request", async () => {
    const calls = stubFetch(registryFetch([]));
    renderSettings();
    goToSection("Advanced");
    await screen.findByText("No media players configured yet.");
    fireEvent.click(screen.getByRole("button", { name: "Add player" }));
    fireEvent.input(await screen.findByLabelText("New player label"), {
      target: { value: "Nowhere" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add player" }));
    expect(await screen.findByText("URL is required")).toBeInTheDocument();
    expect(
      calls.some(
        (c) => c.method === "POST" && c.url.endsWith("/api/service-connections"),
      ),
    ).toBe(false);
  });

  it("editing a Plex row PUTs only that row, to its own id route", async () => {
    const calls = stubFetch(registryFetch(threePlayers));
    renderSettings();
    goToSection("Advanced");
    const label = (await screen.findByLabelText(
      "Player 9 label",
    )) as HTMLInputElement;
    fireEvent.input(label, { target: { value: "Office (Plex)" } });
    clickMediaPlayersSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    const put = registryPuts(calls)[0]!;
    expect(put.url).toContain("/api/service-connections/9");
    expect(put.body).toEqual({
      kind: "player",
      provider: "plex",
      label: "Office (Plex)",
      enabled: true,
      url: "http://plex:32400",
      // No stored secret on this fixture, so a blank one is sent rather than
      // omitted — a no-auth row must stay savable.
      secret: "",
    });
    expect(
      calls.some((c) => c.url.includes("/api/service-connections/7")),
    ).toBe(false);
  });

  it("Delete removes exactly the player row it was clicked in", async () => {
    const calls = stubFetch(registryFetch(threePlayers));
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("Player 7 URL");
    const embyUrl = screen.getByLabelText("Player 8 URL");
    fireEvent.click(registryRow(embyUrl).getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url.endsWith("/api/service-connections/8"),
        ),
      ).toBe(true),
    );
    for (const other of ["7", "9"]) {
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url.endsWith(`/api/service-connections/${other}`),
        ),
      ).toBe(false);
    }
  });
});

// --- Media players: the two-request mode-assignment pattern (AC 8) ---------

describe("Media players — mode assignment is a SECOND request", () => {
  it("a field-only edit fires the field PUT and NO /modes request (the sameModes guard)", async () => {
    // Without this guard every save would re-POST an unchanged assignment. It's
    // asserted as a negative because a redundant /modes write is invisible in
    // the UI — the checkboxes look identical either way.
    const calls = stubFetch(registryFetch([playerConn({ modes: ["movies"] })]));
    renderSettings();
    goToSection("Advanced");
    const label = (await screen.findByLabelText(
      "Player 7 label",
    )) as HTMLInputElement;
    fireEvent.input(label, { target: { value: "Den" } });
    clickMediaPlayersSave();
    // Waiting on the row's own success status (not just the field PUT) proves
    // save() ran PAST the modes branch — otherwise this would race and pass
    // even if the /modes request were about to fire.
    expect(await screen.findByText("✓ saved")).toBeInTheDocument();
    expect(registryPuts(calls)).toHaveLength(1);
    expect(registryModePuts(calls)).toHaveLength(0);
  });

  it("a mode change fires the field PUT FIRST, then the FULL replacement assignment to /modes", async () => {
    const calls = stubFetch(registryFetch([playerConn({ modes: ["movies"] })]));
    renderSettings();
    goToSection("Advanced");
    const series = (await screen.findByLabelText(
      "Player 7 Series",
    )) as HTMLInputElement;
    fireEvent.click(series);
    clickMediaPlayersSave();
    await waitFor(() => expect(registryModePuts(calls).length).toBe(1));
    // The field PUT always fires; the modes PUT is the conditional extra.
    expect(registryPuts(calls)).toHaveLength(1);
    // A FULL replace, never a per-mode toggle — matching Store.SetModes.
    expect(registryModePuts(calls)[0]!.body).toEqual({
      modes: ["movies", "series"],
    });
    expect(registryModePuts(calls)[0]!.url).toContain(
      "/api/service-connections/7/modes",
    );
    // Order matters: the field update must land before the assignment, so a
    // failed field write never leaves modes pointing at a half-saved row.
    expect(calls.indexOf(registryPuts(calls)[0]!)).toBeLessThan(
      calls.indexOf(registryModePuts(calls)[0]!),
    );
  });

  it("unchecking every mode sends an empty assignment rather than skipping the request", async () => {
    const calls = stubFetch(registryFetch([playerConn({ modes: ["movies"] })]));
    renderSettings();
    goToSection("Advanced");
    const movies = (await screen.findByLabelText(
      "Player 7 Movies",
    )) as HTMLInputElement;
    fireEvent.click(movies);
    clickMediaPlayersSave();
    await waitFor(() => expect(registryModePuts(calls).length).toBe(1));
    // 0 players for a mode is a valid state, so "notify nobody" must be
    // persistable — sameModes() must not read [] as "unchanged".
    expect(registryModePuts(calls)[0]!.body).toEqual({ modes: [] });
  });

  it("PARTIAL FAILURE: the field update succeeds, /modes fails, and the row keeps its unpersisted checkboxes", async () => {
    // The honest-failure case the two-request pattern makes possible. There is
    // no transaction across the two calls, so a /modes failure leaves the row's
    // fields persisted and its assignment NOT persisted. What must not happen
    // is the row quietly reporting success or snapping the checkboxes back to
    // the stored value as if nothing was attempted: save() rethrows before
    // props.onChanged(), so no refetch runs and the local (unsaved) checkbox
    // state stays on screen next to a visible error.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/service-connections") && !url.includes("/test")) {
        if (url.endsWith("/modes") && init?.method === "PUT")
          return errorResponse(500, "modes update failed");
        if ((init?.method ?? "GET").toUpperCase() === "GET")
          return jsonResponse([playerConn({ modes: ["movies"] })]);
      }
      return undefined;
    });
    renderSettings();
    goToSection("Advanced");
    const series = (await screen.findByLabelText(
      "Player 7 Series",
    )) as HTMLInputElement;
    fireEvent.click(series);
    clickMediaPlayersSave();

    // The row surfaces the real server message, not a generic failure.
    expect(await screen.findByText("modes update failed")).toBeInTheDocument();
    // Both requests were attempted, in order — the field one really did land.
    expect(registryPuts(calls)).toHaveLength(1);
    expect(registryModePuts(calls)).toHaveLength(1);
    // The checkbox still shows what the operator asked for, NOT the stored
    // ["movies"] — no refetch ran, because save() threw first.
    expect(series.checked).toBe(true);
    // And the row is still dirty, so the section Save stays clickable to retry.
    expect(mediaPlayersSaveButton().disabled).toBe(false);
    // The section-level summary names the row that failed rather than swallowing
    // it. Awaited separately from the row's own status above: the row sets its
    // message inside the catch, BEFORE rethrowing, so SectionSave's allSettled
    // summary lands a microtask later.
    expect(await screen.findByText(/failed: Living room/)).toBeInTheDocument();
  });
});

// --- Media players: three-state secret through the UI ----------------------

describe("Media players — three-state secret semantics through the UI", () => {
  it("saving after editing ONLY the URL OMITS secret from the PUT body", async () => {
    const calls = stubFetch(registryFetch([playerConn()]));
    renderSettings();
    goToSection("Advanced");
    const url = (await screen.findByLabelText("Player 7 URL")) as HTMLInputElement;
    expect(
      (screen.getByLabelText("Player 7 API key") as HTMLInputElement).placeholder,
    ).toContain("wxyz");
    fireEvent.input(url, { target: { value: "http://jellyfin:8920" } });
    clickMediaPlayersSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    const body = registryPuts(calls)[0]!.body;
    expect(body).not.toHaveProperty("secret");
    expect(body).toEqual({
      kind: "player",
      provider: "jellyfin",
      label: "Living room",
      enabled: true,
      url: "http://jellyfin:8920",
    });
  });

  it("typing an API key sends it as the replacement secret", async () => {
    const calls = stubFetch(registryFetch([playerConn()]));
    renderSettings();
    goToSection("Advanced");
    const key = await screen.findByLabelText("Player 7 API key");
    fireEvent.input(key, { target: { value: "jf-rotated" } });
    clickMediaPlayersSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    expect((registryPuts(calls)[0]!.body as { secret?: string }).secret).toBe(
      "jf-rotated",
    );
  });

  it("typing then clearing the API key sends an explicit empty secret (the deliberate clear)", async () => {
    const calls = stubFetch(registryFetch([playerConn()]));
    renderSettings();
    goToSection("Advanced");
    const key = await screen.findByLabelText("Player 7 API key");
    fireEvent.input(key, { target: { value: "x" } });
    fireEvent.input(key, { target: { value: "" } });
    clickMediaPlayersSave();
    await waitFor(() => expect(registryPuts(calls).length).toBe(1));
    const body = registryPuts(calls)[0]!.body as { secret?: string };
    expect(body).toHaveProperty("secret");
    expect(body.secret).toBe("");
  });
});

// --- Trakt's new home: UI -> Discover, and nowhere else (AC 5) -------------
//
// The Trakt component's OWN logic (device flow, three-state secret, disconnect)
// is covered by the "Trakt connection section" suite above, which already runs
// through goToSection("UI"). What was untested is the relocation itself — that
// Trakt is reachable ONLY from there, and is not a leftover connection row in
// any of the sections the old Connections table's contents were split across.

describe("Trakt lives in UI -> Discover, not in any connections table", () => {
  it("renders on the UI tab and on no other section", async () => {
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("prowlarr URL");
    expect(screen.queryByLabelText("Trakt client ID")).toBeNull();
    expect(screen.queryByText("Trakt (Watchlist)")).toBeNull();

    goToDownloadSubTab("Usenet");
    await screen.findByText("Subscriptions");
    expect(screen.queryByLabelText("Trakt client ID")).toBeNull();

    goToSection("Library");
    await screen.findByLabelText("Library root folder");
    expect(screen.queryByLabelText("Trakt client ID")).toBeNull();

    goToSection("UI");
    expect(
      await screen.findByLabelText("Trakt client ID"),
    ).toBeInTheDocument();
    expect(screen.getByText("Trakt (Watchlist)")).toBeInTheDocument();
  });

  it("is not a `trakt` row in the singleton connections table anywhere", async () => {
    // Trakt has its own credential/OAuth surface, never a ConnectionRow — so
    // neither the Advanced API table nor Library's metadata sources may grow a
    // "trakt URL"/"trakt API key" pair as a side effect of the redistribution.
    stubFetch();
    renderSettings();
    goToSection("Advanced");
    await screen.findByLabelText("prowlarr URL");
    expect(screen.queryByLabelText("trakt URL")).toBeNull();
    expect(screen.queryByLabelText("trakt API key")).toBeNull();
    await goToLibraryConnections();
    expect(await screen.findByLabelText("tmdb API key")).toBeInTheDocument();
    expect(screen.queryByLabelText("trakt API key")).toBeNull();
  });

  it("still saves credentials from its new home (the relocation changed no behaviour)", async () => {
    const calls = stubFetch();
    renderSettings();
    goToSection("UI");
    fireEvent.input(await screen.findByLabelText("Trakt client ID"), {
      target: { value: "relocated-client-id" },
    });
    fireEvent.input(screen.getByLabelText("Trakt client secret"), {
      target: { value: "relocated-secret" },
    });
    clickTraktSave();
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
    expect(put.body).toEqual({
      clientId: "relocated-client-id",
      clientSecret: "relocated-secret",
    });
  });
});

// --- AI promoted out of the Connections nesting (AC 6) ---------------------

describe("AI is its own top-level section tab", () => {
  it("sits in the section tab bar, with no Connections tab or sub-tab to reach it through", async () => {
    stubFetch();
    renderSettings();
    await screen.findByLabelText("Library root folder");
    // A real SECTION_TABS entry, reachable in ONE click from any other section.
    expect(
      sectionTabBar().getByRole("button", { name: "AI" }),
    ).toBeInTheDocument();
    expect(
      sectionTabBar().queryByRole("button", { name: "Connections" }),
    ).toBeNull();
    goToSection("AI");
    expect(await screen.findByLabelText("AI provider")).toBeInTheDocument();
    // Exactly one "AI" button screen-wide: the section tab. If AI were still a
    // sub-tab, its inner Connections/AI pill bar would render a second one.
    expect(screen.getAllByRole("button", { name: "AI" })).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Connections" })).toBeNull();
  });

  it("carries its own connection rows with it, without an intermediate sub-tab click", async () => {
    stubFetch();
    renderSettings();
    goToSection("AI");
    // The AI-provider and Brave rows are on the tab itself — one click, no
    // nested navigation — while the API-section rows stay behind on Advanced.
    expect(await screen.findByLabelText("ollama URL")).toBeInTheDocument();
    expect(screen.getByLabelText("brave API key")).toBeInTheDocument();
    expect(screen.queryByLabelText("prowlarr URL")).toBeNull();
    expect(screen.queryByText("Media players")).toBeNull();
  });
});

// --- Pruning tab REMOVAL (Claude 2026-08-11) --------------------------------
//
// The "Pruning is its own top-level section tab" describe block that lived here
// is deleted along with the tab itself: its only content, the rules CRUD, moved
// onto the Clean-up screen as a collapsible Rules card
// (screens/PurgeRulesCard.tsx), leaving the tab empty.
// Reason: matching is configured on the screen that shows its results.
// Troubleshooting: this replacement is a NEGATIVE assertion on purpose — it is
//   the only thing that would fail if a future change re-added a "Pruning"
//   entry to SECTION_TABS, since every other tab test asserts presence.
// Review if: the rules builder ever moves back into Settings.

describe("Pruning is no longer a section tab", () => {
  it("renders no Pruning tab and no rules panel anywhere in Settings", async () => {
    stubFetch();
    renderSettings();
    // Wait for Settings' own first paint before asserting an absence, so this
    // cannot pass merely by running before anything rendered.
    await screen.findByLabelText("Library root folder");

    expect(sectionTabBar().queryByRole("button", { name: "Pruning" })).toBeNull();
    expect(screen.queryByText("Pruning rules")).toBeNull();
    expect(screen.queryByText("+ New rule")).toBeNull();
  });
});

describe("Organize is its own top-level section tab", () => {
  it("renders all three scan-schedule panels when selected", async () => {
    stubFetch();
    renderSettings();
    await screen.findByLabelText("Library root folder");
    expect(
      sectionTabBar().getByRole("button", { name: "Organize" }),
    ).toBeInTheDocument();
    goToSection("Organize");

    // One toggle + one interval picker per WORKFLOW (all three modes
    // together), never one per mode.
    for (const wf of ["Rename", "Dedup", "Clean-up"]) {
      expect(
        await screen.findByLabelText(`${wf} scheduled scanning enabled`),
      ).toBeInTheDocument();
      expect(
        screen.getByLabelText(`${wf} scan interval`),
      ).toBeInTheDocument();
    }
    expect(screen.getAllByRole("switch")).toHaveLength(4);
    expect(
      screen.getByLabelText("Include VMAF in scheduled Dedup"),
    ).toBeInTheDocument();
  });
});

describe("Settings — no bulk-action affordances", () => {
  it("has no save-all / apply-all across the whole view", async () => {
    stubFetch();
    renderSettings();
    // Mount confirmed via the always-present Settings heading. (Not the
    // "Connections" button: the Connections tab's own inline Connections/AI
    // sub-tab bar also renders a "Connections" button, so that query would be
    // ambiguous here — this test isn't scoped to a shell-slot container the
    // way the shell-harness test above is.)
    await screen.findByRole("heading", { name: "Settings" });
    expect(screen.queryByText(/save all/i)).toBeNull();
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/test all/i)).toBeNull();
    expect(screen.queryByText(/delete all/i)).toBeNull();
  });
});
