// Browser-notifications toggle tests (plan Step 9). The load-bearing behaviors:
// (1) the native Notification permission is requested ONLY when turning the
// preference on from an unresolved ("default") state; (2) the preference is
// persisted regardless of the permission outcome (preference and permission are
// separate states, per plan Principle 4); (3) an enabled-but-denied preference
// renders a distinct "Blocked" message, never a plain on-state; and (4) flipping
// the toggle updates the SAME shared browserNotificationsEnabled signal instance
// that the shell-mounted BrowserNotifications component reads — a signal-level
// integration check, not just a render.
//
// Permission-stub note: both the toggle's local mirror AND worker-2's shell
// effect read Notification.permission (the property), not requestPermission()'s
// return value — real browsers update the property synchronously when the promise
// resolves, so the stub must mutate MockNotification.permission inside
// requestPermission or the enable-from-default path would be mis-tested.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import {
  browserNotificationsEnabled,
  setBrowserNotificationsEnabled,
} from "../../api/webhooks";
import { WebhooksSection } from "./Webhooks";

// MockNotification stands in for the browser Notification API. It is constructible
// (the shell's emit path calls `new Notification(...)`) and its static
// `permission` is mutated by requestPermission the way a real browser does.
class MockNotification {
  static permission: NotificationPermission = "default";
  static requestPermission = vi.fn(async () => MockNotification.permission);
  constructor(
    public title: string,
    public options?: NotificationOptions,
  ) {}
}

// setPermissionOnRequest makes requestPermission resolve to (and set the property
// to) the given outcome, matching real synchronous property-update behavior.
const setPermissionOnRequest = (outcome: NotificationPermission) => {
  MockNotification.requestPermission.mockImplementation(async () => {
    MockNotification.permission = outcome;
    return outcome;
  });
};

type Call = { url: string; method: string; body: unknown };

type Finding = { service: string; url: string; label: string };

const json = (v: unknown) =>
  new Response(JSON.stringify(v), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

// stubFetch answers the three endpoints WebhooksSection can reach. known/host
// default to empty so the pre-existing browser-notification tests are
// unaffected; a test that cares supplies its own finding arrays.
const stubFetch = (opts: { known?: Finding[]; host?: Finding[] } = {}) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    // The LAN-discovery hint's passive fetch — only fired once the Add-webhook
    // form is open (see the gating test below).
    if (url.includes("/api/netscan/known")) return json(opts.known ?? []);
    // The manual host lookup.
    if (url.includes("/api/netscan/host")) return json(opts.host ?? []);
    // WebhooksSection mounts createResource(fetchWebhooks) → GET /api/webhooks.
    if (url.includes("/api/webhooks") && method === "GET")
      return json([]);
    // Every mutation (the PUT preference) defaults to a clean 204.
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const ntfy: Finding = {
  service: "ntfy",
  url: "http://ntfy:80",
  label: "possible ntfy instance",
};
const gotify: Finding = {
  service: "gotify",
  url: "http://gotify:80",
  label: "possible Gotify instance",
};
const nodeRed: Finding = {
  service: "node-red",
  url: "http://node-red:1880",
  label: "possible Node-RED instance",
};
const prowlarr: Finding = {
  service: "prowlarr",
  url: "http://prowlarr:9696",
  label: "possible Prowlarr instance",
};

const netscanCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/api/netscan/known"));

// openAddForm renders the section, waits for the collapsed state to settle, and
// clicks through to the open form.
const openAddForm = async () => {
  fireEvent.click(await screen.findByText("Add webhook"));
  return await screen.findByLabelText("Look up webhook target host");
};

beforeEach(() => {
  MockNotification.permission = "default";
  MockNotification.requestPermission = vi.fn(
    async () => MockNotification.permission,
  );
  vi.stubGlobal("Notification", MockNotification);
});

afterEach(() => {
  // The shared signal is module-level state that persists across tests — reset
  // it so test order can't leak an enabled preference into the next test.
  setBrowserNotificationsEnabled(false);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const toggle = () =>
  screen.getByLabelText("Enable browser notifications") as HTMLInputElement;

describe("Browser notifications toggle", () => {
  it("requests permission when turning on from the 'default' state", async () => {
    MockNotification.permission = "default";
    setPermissionOnRequest("granted");
    const calls = stubFetch();
    render(() => <WebhooksSection />);

    fireEvent.click(toggle());

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/api/settings/browser-notifications-enabled"),
        ),
      ).toBe(true),
    );
    expect(MockNotification.requestPermission).toHaveBeenCalledTimes(1);
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ enabled: true });
    await waitFor(() => expect(browserNotificationsEnabled()).toBe(true));
  });

  it("does NOT request permission when it is already granted", async () => {
    MockNotification.permission = "granted";
    const calls = stubFetch();
    render(() => <WebhooksSection />);

    fireEvent.click(toggle());

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(MockNotification.requestPermission).not.toHaveBeenCalled();
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ enabled: true });
    await waitFor(() => expect(browserNotificationsEnabled()).toBe(true));
  });

  it("persists the preference even when permission is denied on enable", async () => {
    MockNotification.permission = "default";
    setPermissionOnRequest("denied");
    const calls = stubFetch();
    render(() => <WebhooksSection />);

    fireEvent.click(toggle());

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    // Preference persisted (enabled=true) regardless of the denied permission.
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ enabled: true });
    await waitFor(() => expect(browserNotificationsEnabled()).toBe(true));
    // ...and because permission ended up denied, the blocked message shows.
    expect(
      await screen.findByText(/Blocked — enable notifications for this site/),
    ).toBeInTheDocument();
  });

  it("renders the 'blocked' state distinctly from off when enabled + denied", async () => {
    MockNotification.permission = "denied";
    stubFetch();
    // Preference already enabled (e.g. seeded from the server on another device),
    // but this browser has permanently denied permission.
    setBrowserNotificationsEnabled(true);
    render(() => <WebhooksSection />);

    expect(
      await screen.findByText(/Blocked — enable notifications for this site/),
    ).toBeInTheDocument();
    // The checkbox still reflects the enabled preference (it is not silently off).
    expect(toggle().checked).toBe(true);
  });

  it("does NOT show 'blocked' when the preference is off, even if permission is denied", async () => {
    MockNotification.permission = "denied";
    stubFetch();
    setBrowserNotificationsEnabled(false);
    render(() => <WebhooksSection />);

    await screen.findByLabelText("Enable browser notifications");
    expect(screen.queryByText(/Blocked — enable notifications/)).toBeNull();
    expect(toggle().checked).toBe(false);
  });

  it("turning off persists enabled=false and requests no permission", async () => {
    MockNotification.permission = "granted";
    const calls = stubFetch();
    setBrowserNotificationsEnabled(true);
    render(() => <WebhooksSection />);
    expect(toggle().checked).toBe(true);

    fireEvent.click(toggle());

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(MockNotification.requestPermission).not.toHaveBeenCalled();
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ enabled: false });
    await waitFor(() => expect(browserNotificationsEnabled()).toBe(false));
  });

  it("flips the SAME shared signal the shell reads (signal-level integration)", async () => {
    MockNotification.permission = "granted";
    stubFetch();
    expect(browserNotificationsEnabled()).toBe(false);
    render(() => <WebhooksSection />);

    fireEvent.click(toggle());

    // The imported accessor (the exact instance BrowserNotifications subscribes
    // to) reflects the flip — proving one shared signal, not a parallel copy.
    await waitFor(() => expect(browserNotificationsEnabled()).toBe(true));
  });
});

// The Add-webhook form's LAN-discovery hint. The first test is the ONLY thing
// pinning the createResource(open, …) gating: api() returns null on a 204
// rather than throwing, so an ungated resource breaks none of the tests above.
describe("Add-webhook LAN discovery hint", () => {
  const urlInput = () =>
    screen.getByPlaceholderText("https://example.com/hook") as HTMLInputElement;

  it("fires no LAN probe until the Add-webhook form is opened", async () => {
    const calls = stubFetch({ known: [ntfy] });
    render(() => <WebhooksSection />);

    // Settle the collapsed state first — asserting absence before an ungated
    // resource would have had a chance to fire proves nothing.
    await screen.findByText("Add webhook");
    await screen.findByLabelText("Enable browser notifications");
    expect(netscanCalls(calls)).toHaveLength(0);

    fireEvent.click(screen.getByText("Add webhook"));

    // The presence half is what proves the matcher above actually matches.
    await waitFor(() => expect(netscanCalls(calls)).toHaveLength(1));
  });

  it("renders a discovery hint for a discovered ntfy", async () => {
    stubFetch({ known: [ntfy] });
    render(() => <WebhooksSection />);
    await openAddForm();

    expect(
      await screen.findByText(/Possible ntfy at http:\/\/ntfy:80/),
    ).toBeInTheDocument();
  });

  it("ignores findings for non-webhook services", async () => {
    // The ntfy finding is the settle point: it is a POSITIVE signal that only
    // exists once the resource resolved and rendered, so the prowlarr absence
    // below cannot pass merely because nothing has rendered yet. Asserting on
    // the recorded fetch instead would NOT work — calls are recorded when the
    // request is issued, before the response is even parsed.
    stubFetch({ known: [ntfy, prowlarr] });
    render(() => <WebhooksSection />);
    await openAddForm();

    await screen.findByText(/Possible ntfy/);
    expect(screen.queryByText(/prowlarr/)).toBeNull();
    expect(screen.queryByLabelText("Use prowlarr URL")).toBeNull();
  });

  it("'Use this URL' fills the base URL and nothing else", async () => {
    stubFetch({ known: [ntfy] });
    render(() => <WebhooksSection />);
    await openAddForm();

    fireEvent.click(await screen.findByLabelText("Use ntfy URL"));

    // Exactly the base URL: no topic appended, port not elided.
    await waitFor(() => expect(urlInput().value).toBe("http://ntfy:80"));
  });

  it("manual host lookup surfaces webhook-service findings only", async () => {
    const calls = stubFetch({ known: [], host: [ntfy, prowlarr] });
    render(() => <WebhooksSection />);
    const lookup = await openAddForm();

    fireEvent.input(lookup, { target: { value: "10.1.10.4" } });
    fireEvent.click(screen.getByText("Look up"));

    expect(
      await screen.findByText(/Found ntfy at http:\/\/ntfy:80/),
    ).toBeInTheDocument();
    const probe = calls.find((c) => c.url.includes("/api/netscan/host"))!;
    expect(probe.body).toEqual({ host: "10.1.10.4" });
    expect(screen.queryByText(/prowlarr/)).toBeNull();

    // The lookup result's own "Use this URL" — a DIFFERENT button from the
    // passive hint's, hence the "from lookup" label suffix. Same AC6 contract:
    // exactly the base URL, no topic appended, port not elided.
    fireEvent.click(screen.getByLabelText("Use ntfy URL from lookup"));
    await waitFor(() => expect(urlInput().value).toBe("http://ntfy:80"));
  });

  it("renders hints for all three webhook services at once", async () => {
    stubFetch({ known: [ntfy, gotify, nodeRed] });
    render(() => <WebhooksSection />);
    await openAddForm();

    // Queried by their distinct aria-labels — this is what pins the Go/TS
    // service-string contract; a mismatch renders no hint at all.
    expect(await screen.findByLabelText("Use ntfy URL")).toBeInTheDocument();
    expect(screen.getByLabelText("Use gotify URL")).toBeInTheDocument();
    expect(screen.getByLabelText("Use node-red URL")).toBeInTheDocument();
  });
});
