// Claude 2026-08-10: new file — tests for the Settings "Organize" tab (plan
// .omc/plans/autopilot-impl-scanschedule-settings-ui.md §7).
// Reason: covers all three panels rendering, each toggle round-tripping
// through its own enabled endpoint, each picker round-tripping through its own
// interval endpoint, and the decoupling guarantee (a toggle PUT never touches
// the interval endpoint). The Clean-up block is the one that MOVED here from
// the old Pruning tab's own test file along with the Card it exercises.
// Claude 2026-08-11: that file (settings/PruningRules.test.tsx) no longer
// exists — it became screens/PurgeRulesCard.test.tsx when the rules builder
// relocated onto the Clean-up screen, and the Settings > Pruning tab was
// removed outright.
// Troubleshooting: "Clean-up" here is the operator-facing label only; the
// settings key, endpoints and element id are all still `purge`
// (purge-scan-interval, fetchPurgeScanEnabled, ...).
// Related files: src/screens/PurgeRulesCard.test.tsx (the relocated rules
// CRUD), src/screens/Settings.test.tsx ("Pruning is no longer a section tab",
// the negative assertion that keeps the tab from silently returning).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { OrganizeScanScheduleSection } from "./OrganizeScanSchedule";

const jsonResponse = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const noContent = (): Response => new Response(null, { status: 204 });

type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

// The three workflows, keyed by the endpoint slug each panel is wired to —
// every test below runs against all three, since each panel is a separate
// four-endpoint wiring and a copy-paste slip in one is exactly what a
// single-workflow test would miss.
const WORKFLOWS = [
  { slug: "rename", title: "Rename" },
  { slug: "dedup", title: "Dedup" },
  { slug: "purge", title: "Clean-up" },
] as const;

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
    if (method === "GET" && url.includes("-scan-enabled"))
      return jsonResponse({ enabled: true });
    if (method === "GET" && url.includes("-scan-interval"))
      return jsonResponse({ intervalSeconds: 86400 });
    return noContent();
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const putsTo = (calls: Call[], path: string) =>
  calls.filter((c) => c.method === "PUT" && c.url.includes(path));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("OrganizeScanScheduleSection — layout", () => {
  it("renders exactly three panels — one per workflow, never one per mode", async () => {
    stubFetch();
    render(() => <OrganizeScanScheduleSection />);

    for (const wf of WORKFLOWS) {
      expect(
        await screen.findByLabelText(`${wf.title} scheduled scanning enabled`),
      ).toBeTruthy();
      expect(
        await screen.findByLabelText(`${wf.title} scan interval`),
      ).toBeTruthy();
    }
    // Three toggles total: the spec's "one control per WORKFLOW covering all
    // three modes together" constraint, asserted as a count so a future
    // per-mode expansion (nine toggles) fails here rather than silently
    // shipping.
    expect(screen.getAllByRole("switch")).toHaveLength(3);
  });

  it("shows a FETCHED false, not just the on-by-default fallback", async () => {
    // The Switch falls back to `true` while loading / on a load error, because
    // these three workflows genuinely default on. This test is what keeps the
    // other toggle assertions honest: it proves the fetched value actually
    // drives the control rather than every one of them passing on the
    // fallback.
    stubFetch((url) => {
      if (url.includes("/api/settings/rename-scan-enabled"))
        return jsonResponse({ enabled: false });
      return undefined;
    });
    render(() => <OrganizeScanScheduleSection />);

    const off = await screen.findByLabelText(
      "Rename scheduled scanning enabled",
    );
    await waitFor(() => expect(off.getAttribute("aria-checked")).toBe("false"));
    // A sibling still reading true rules out "everything renders false".
    expect(
      screen
        .getByLabelText("Dedup scheduled scanning enabled")
        .getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("shows the on-by-default 24h interval each endpoint reports for an unset key", async () => {
    stubFetch();
    render(() => <OrganizeScanScheduleSection />);
    // 86400s best-fits to 1 Day in DurationSetting's unit picker.
    const input = (await screen.findByLabelText(
      "Rename scan interval",
    )) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("1"));
  });
});

describe("OrganizeScanScheduleSection — enabled toggles", () => {
  for (const wf of WORKFLOWS) {
    it(`${wf.title}: reflects the fetched state and PUTs the flipped value immediately`, async () => {
      const calls = stubFetch();
      render(() => <OrganizeScanScheduleSection />);

      const toggle = (await screen.findByLabelText(
        `${wf.title} scheduled scanning enabled`,
      )) as HTMLButtonElement;
      // Wait on `disabled`, not on aria-checked: the Switch falls back to
      // checked=true while the resource is loading, so an aria-checked wait
      // would pass before the fetch resolved and the click would land on a
      // still-disabled button and silently do nothing.
      await waitFor(() => expect(toggle.disabled).toBe(false));
      expect(toggle.getAttribute("aria-checked")).toBe("true");

      fireEvent.click(toggle);

      // Immediate-apply: no Save button press, unlike the interval picker.
      await waitFor(() =>
        expect(putsTo(calls, `/api/settings/${wf.slug}-scan-enabled`)).toHaveLength(1),
      );
      expect(
        putsTo(calls, `/api/settings/${wf.slug}-scan-enabled`)[0]!.body,
      ).toEqual({ enabled: false });
    });
  }

  it("toggling a workflow off never writes its interval endpoint", async () => {
    const calls = stubFetch();
    render(() => <OrganizeScanScheduleSection />);

    const toggle = (await screen.findByLabelText(
      "Dedup scheduled scanning enabled",
    )) as HTMLButtonElement;
    // Wait for the enabled resource to resolve: the Switch is `disabled` while
    // it is loading, so an early click is silently a no-op.
    await waitFor(() => expect(toggle.disabled).toBe(false));
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(putsTo(calls, "/api/settings/dedup-scan-enabled")).toHaveLength(1),
    );
    // The whole point of a separate enabled key: the stored interval survives
    // an off/on cycle, so it cannot be written here.
    expect(putsTo(calls, "-scan-interval")).toHaveLength(0);
  });

  it("rolls the toggle back and surfaces the error when the PUT fails", async () => {
    stubFetch((url, init) => {
      if (
        (init?.method ?? "GET").toUpperCase() === "PUT" &&
        url.includes("/api/settings/rename-scan-enabled")
      ) {
        return new Response("boom", { status: 500 });
      }
      return undefined;
    });
    render(() => <OrganizeScanScheduleSection />);

    const toggle = (await screen.findByLabelText(
      "Rename scheduled scanning enabled",
    )) as HTMLButtonElement;
    await waitFor(() => expect(toggle.disabled).toBe(false));
    fireEvent.click(toggle);

    // The rollback restores the pre-click state; the error line is the proof
    // the PUT actually failed rather than the click never landing.
    expect(await screen.findByText(/boom/)).toBeTruthy();
    expect(toggle.getAttribute("aria-checked")).toBe("true");
  });
});

describe("OrganizeScanScheduleSection — interval pickers", () => {
  for (const wf of WORKFLOWS) {
    it(`${wf.title}: PUTs a new interval without touching the enabled endpoint`, async () => {
      const calls = stubFetch();
      render(() => <OrganizeScanScheduleSection />);

      const input = (await screen.findByLabelText(
        `${wf.title} scan interval`,
      )) as HTMLInputElement;
      await waitFor(() => expect(input.value).toBe("1"));
      // The fetched 86400 fits the "Days" unit, so 2 here means 2 days.
      fireEvent.input(input, { target: { value: "2" } });
      fireEvent.click(screen.getAllByText("Save")[WORKFLOWS.indexOf(wf)]!);

      await waitFor(() =>
        expect(
          putsTo(calls, `/api/settings/${wf.slug}-scan-interval`),
        ).toHaveLength(1),
      );
      expect(
        putsTo(calls, `/api/settings/${wf.slug}-scan-interval`)[0]!.body,
      ).toEqual({ intervalSeconds: 172800 });
      expect(putsTo(calls, "-scan-enabled")).toHaveLength(0);
    });
  }
});

// This block MOVED here from the old Pruning tab's test file together with the
// Card it exercises — kept rather than deleted, so the relocation loses no
// coverage.
describe("OrganizeScanScheduleSection — Clean-up panel (relocated from the former Pruning tab)", () => {
  it("renders the purge scan interval and PUTs a new value", async () => {
    const calls = stubFetch((url) => {
      if (url.includes("/api/settings/purge-scan-interval"))
        return jsonResponse({ intervalSeconds: 0 });
      return undefined;
    });
    render(() => <OrganizeScanScheduleSection />);
    // Value 0 defaults the picker to the "Hours" unit (same convention as
    // AdultRowAdmin's scan-interval control); typing "1" there means 1 hour
    // = 3600 seconds.
    const input = (await screen.findByLabelText(
      "Clean-up scan interval",
    )) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "1" } });
    fireEvent.click(screen.getAllByText("Save")[2]!);

    await waitFor(() =>
      expect(putsTo(calls, "/api/settings/purge-scan-interval")).toHaveLength(1),
    );
    expect(putsTo(calls, "/api/settings/purge-scan-interval")[0]!.body).toEqual({
      intervalSeconds: 3600,
    });
  });
});
