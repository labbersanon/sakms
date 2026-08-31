// Torrent settings tests. The property that matters most here is NOT that each
// control renders — it is that editing ONE control PUTs the whole stored
// document back with exactly that one field changed. The endpoint is a
// full-document replace with no pointer/omitempty three-state, so a body built
// from the rendered controls (rather than from the loaded document) silently
// zeroes every field that happens to have no control — including maxConcurrent,
// which has none by design and whose zero value is a 400 on EVERY save.
//
// Hence the shape of the round-trip test: a fixture where every field holds a
// distinctive non-default value, one interaction, then a DEEP-EQUAL against the
// fixture with only the edited key overridden. Asserting the edited field alone
// would still pass while an untouched sibling got clobbered.
//
// The other assertions cover the three distinct save outcomes the PUT has —
// 200/live, 200/rebuilt, and 409/refused — because a 409 means nothing was
// saved and the operator should retry in a moment, which must not read like a
// rejected setting.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { DownloaderConfig } from "@dto";
import { TorrentSection } from "./Torrent";

// Every field is a distinctive non-default so a clobbered sibling can't hide
// behind a value that happens to match what the code would have invented.
const FIXTURE: DownloaderConfig = {
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
};

const LIVE_RESULT = {
  applied: "live",
  restartRequired: false,
  message: "Saved and applied immediately. No downloads were interrupted.",
};

const REBUILT_RESULT = {
  applied: "rebuilt",
  restartRequired: true,
  message:
    "Saved. Applying these settings restarted the torrent engine — any downloads in flight were briefly interrupted and have resumed.",
};

interface PutCall {
  body: DownloaderConfig;
}

// stubFetch serves the two GETs the card issues and records every PUT. put()
// decides the PUT's response, so each test can pick 200-live / 200-rebuilt /
// 409 without restubbing the GETs. Note the PUT answers 200 + JSON, NOT 204:
// a 204 would make api() resolve with null and every apply-result assertion
// below would silently test nothing.
//
// opts.config swaps the loaded document (for bounds cases that need a stored
// value no control can produce), and opts.get replaces the GET response
// wholesale (for the initial-load-failure case). Both are additive: the
// round-trip cases above still deep-equal against FIXTURE.
const stubFetch = (
  put: () => Response,
  opts: {
    autoGrab?: boolean;
    config?: DownloaderConfig;
    get?: () => Response;
  } = {},
) => {
  const puts: PutCall[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.includes("/api/downloader/config")) {
      if (method === "PUT") {
        puts.push({ body: JSON.parse(init!.body as string) as DownloaderConfig });
        return put();
      }
      return opts.get ? opts.get() : json(opts.config ?? FIXTURE);
    }
    if (url.includes("/api/settings/usenet-autograb-enabled"))
      return json({ enabled: opts.autoGrab ?? true });
    if (url.includes("/api/settings/torrent-autograb-slots"))
      return json({ perCycle: 0, perSeries: 5 });
    if (url.includes("/api/settings/usenet-autograb-slots"))
      return json({ perCycle: 20, perSeries: 5 });
    throw new Error(`unexpected request: ${method} ${url}`);
  });
  vi.stubGlobal("fetch", fn);
  return puts;
};

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Torrent auto-grab slots", () => {
  it("loads defaults and PUTs dedicated torrent slots", async () => {
    const puts: { url: string; body: unknown }[] = [];
    const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.includes("/api/downloader/config"))
        return json(FIXTURE);
      if (url.includes("/api/settings/usenet-autograb-enabled"))
        return json({ enabled: true });
      if (url.includes("/api/settings/torrent-autograb-slots")) {
        if (method === "PUT") {
          puts.push({ url, body: init?.body ? JSON.parse(init.body as string) : undefined });
          return new Response(null, { status: 204 });
        }
        return json({ perCycle: 0, perSeries: 5 });
      }
      throw new Error(`unexpected request: ${method} ${url}`);
    });
    vi.stubGlobal("fetch", fn);

    render(() => <TorrentSection />);
    const cycle = (await screen.findByLabelText(
      "Torrent slots per cycle",
    )) as HTMLInputElement;
    await waitFor(() => expect(cycle.value).toBe("0"));
    fireEvent.input(cycle, { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(puts.length).toBe(1));
    expect(puts[0]!.body).toEqual({ perCycle: 10, perSeries: 5 });
  });
});

// mountLoaded renders the section and waits for the initial GET to land.
const mountLoaded = async () => {
  render(() => <TorrentSection />);
  await screen.findByLabelText("Staging directory");
};

const saveButton = () =>
  screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;

describe("Torrent settings — whole-document round trip", () => {
  // One case per control in the canonical settings table. maxConcurrent is
  // deliberately absent (it renders no control) and there is deliberately no
  // local-peer-discovery control — the engine has no LPD support to toggle.
  const cases: {
    name: string;
    act: () => void;
    changed: Partial<DownloaderConfig>;
  }[] = [
    {
      name: "staging directory",
      act: () =>
        fireEvent.input(screen.getByLabelText("Staging directory"), {
          target: { value: "/mnt/other" },
        }),
      changed: { stagingDir: "/mnt/other" },
    },
    {
      name: "listen port",
      act: () =>
        fireEvent.input(screen.getByLabelText("Listen port"), {
          target: { value: "6881" },
        }),
      changed: { listenPort: 6881 },
    },
    {
      name: "DHT toggle",
      act: () => fireEvent.click(screen.getByLabelText("Enable DHT")),
      changed: { dhtEnabled: true },
    },
    {
      name: "PEX toggle",
      act: () => fireEvent.click(screen.getByLabelText("Enable PEX")),
      changed: { pexEnabled: true },
    },
    {
      name: "obfuscation mode",
      act: () =>
        fireEvent.change(screen.getByLabelText("Protocol obfuscation"), {
          target: { value: "off" },
        }),
      changed: { obfuscationMode: "off" },
    },
    {
      name: "max connections",
      act: () =>
        fireEvent.input(screen.getByLabelText("Max connections per torrent"), {
          target: { value: "3" },
        }),
      changed: { maxConnections: 3 },
    },
    {
      // The unit selector is display-only: 3 MB/s must reach the wire as
      // bytes/sec, and the unit must never appear in the document.
      name: "download rate limit",
      act: () =>
        fireEvent.input(screen.getByLabelText("Download rate limit"), {
          target: { value: "3" },
        }),
      changed: { downloadRateLimitBytes: 3 * 1024 * 1024 },
    },
    {
      name: "seeding master switch",
      act: () => fireEvent.click(screen.getByLabelText("Enable seeding")),
      changed: { seedingEnabled: false },
    },
    {
      name: "seed ratio limit",
      act: () =>
        fireEvent.input(screen.getByLabelText("Seed ratio limit"), {
          target: { value: "1.5" },
        }),
      changed: { seedRatioLimit: 1.5 },
    },
    {
      // 2880 stored minutes fits days exactly, so the control shows 2 days;
      // typing 3 must send 4320 minutes, not 3.
      name: "seed duration limit",
      act: () =>
        fireEvent.input(screen.getByLabelText("Seed duration limit"), {
          target: { value: "3" },
        }),
      changed: { seedDurationMinutes: 3 * 1440 },
    },
    {
      name: "stale torrent threshold",
      act: () =>
        fireEvent.input(screen.getByLabelText("Stale torrent threshold"), {
          target: { value: "6" },
        }),
      changed: { staleThresholdMinutes: 6 * 60 },
    },
  ];

  for (const c of cases) {
    it(`${c.name}: PUTs the whole document with only that field changed`, async () => {
      const puts = stubFetch(() => json(LIVE_RESULT));
      await mountLoaded();

      c.act();
      fireEvent.click(saveButton());

      await waitFor(() => expect(puts).toHaveLength(1));
      // The load-bearing assertion: everything else is byte-for-byte the
      // document that was loaded, including the fields with no control.
      expect(puts[0]!.body).toEqual({ ...FIXTURE, ...c.changed });
    });
  }

  it("maxConcurrent survives a save even though it renders no control", async () => {
    const puts = stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();

    // No control exists for it, by design (the field is reserved/unimplemented
    // server-side, so a knob for it would do nothing).
    expect(screen.queryByLabelText(/max concurrent/i)).toBeNull();

    fireEvent.input(screen.getByLabelText("Seed ratio limit"), {
      target: { value: "1" },
    });
    fireEvent.click(saveButton());

    await waitFor(() => expect(puts).toHaveLength(1));
    // Echoed back verbatim. A dropped/zeroed maxConcurrent is a 400 on every
    // save, and the field is invisible, so the error would look like it came
    // from the seed ratio.
    expect(puts[0]!.body.maxConcurrent).toBe(7);
  });

  it("switching a unit re-expresses the same duration rather than changing it", async () => {
    const puts = stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();

    // 2880 stored minutes shows as 2 days; switching to hours must show 48 and
    // save the SAME 2880 — a re-interpretation ("2 hours") would silently cut
    // the operator's seed time by 24x on a save they thought was cosmetic.
    fireEvent.change(screen.getByLabelText("Seed duration limit unit"), {
      target: { value: "hours" },
    });
    expect(
      (screen.getByLabelText("Seed duration limit") as HTMLInputElement).value,
    ).toBe("48");

    fireEvent.click(saveButton());
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.body).toEqual(FIXTURE);
  });

  it("renders the stale threshold's single unit as a suffix, not a dropdown", async () => {
    stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();
    // A <select> with exactly one option is a dropdown that can't do anything.
    expect(screen.queryByLabelText("Stale torrent threshold unit")).toBeNull();
  });

  it("has no local peer discovery control", async () => {
    stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();
    expect(screen.queryByLabelText(/local peer discovery/i)).toBeNull();
  });

  it("labels obfuscation Off as rejecting encrypted peers", async () => {
    stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();
    // "off" is the STRICTEST mode, not the most permissive one — a bare "Off"
    // would promise softer behavior than what ships.
    const select = screen.getByLabelText(
      "Protocol obfuscation",
    ) as HTMLSelectElement;
    const off = [...select.options].find((o) => o.value === "off");
    expect(off?.text).toBe("Off (reject encrypted peers)");
  });
});

describe("Torrent settings — save-effect copy", () => {
  const editSomething = () =>
    fireEvent.input(screen.getByLabelText("Seed ratio limit"), {
      target: { value: "1" },
    });

  it("a live apply says nothing was interrupted", async () => {
    stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();
    editSomething();
    fireEvent.click(saveButton());

    // The server's message is rendered verbatim — it is the only place the
    // consequence is described, so a client-side paraphrase could drift.
    await screen.findByText(LIVE_RESULT.message);
  });

  it("styles a live apply as ordinary status, not as a warning", async () => {
    stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();
    editSomething();
    fireEvent.click(saveButton());

    const note = await screen.findByText(LIVE_RESULT.message);
    // The rebuilt case below asserts text-warn; this asserts its absence, and
    // both directions are needed. Each message-text assertion on its own would
    // still pass an implementation that painted every outcome amber — and amber
    // on a change that interrupted nothing teaches the operator to ignore the
    // colour on the one outcome where it means something.
    expect(note.className).toContain("text-muted");
    expect(note.className).not.toContain("text-warn");
  });

  it("a rebuilt apply says the engine restarted and downloads were interrupted", async () => {
    stubFetch(() => json(REBUILT_RESULT));
    await mountLoaded();
    editSomething();
    fireEvent.click(saveButton());

    const note = await screen.findByText(REBUILT_RESULT.message);
    // Rendered as a warning, not an error: the save DID succeed.
    expect(note.className).toContain("text-warn");
  });

  it("a 409 refusal reads as retryable, not as a failed save", async () => {
    stubFetch(
      () =>
        new Response("downloader: engine rebuild refused: Some Movie is still fetching metadata", {
          status: 409,
          headers: { "Content-Type": "text/plain" },
        }),
    );
    await mountLoaded();
    editSomething();
    fireEvent.click(saveButton());

    const note = await screen.findByText(/Try saving again once/i);
    // Honest about what happened: nothing persisted, nothing applied...
    expect(note.textContent).toMatch(/nothing was saved or changed/i);
    // ...and the server's reason (which names the blocking download) is kept.
    expect(note.textContent).toMatch(/Some Movie is still fetching metadata/);
    // Not styled as an error — this is a "not right now", not a bad setting.
    expect(note.className).toContain("text-warn");

    // The SECTION's own summary must not contradict the note above. A rejected
    // save prints a red "failed: torrent settings" there, which is exactly the
    // scary framing the amber note exists to avoid — so the card reports the
    // refusal as a non-failure outcome and the summary says so calmly instead.
    // Awaited rather than merely queried: the card sets its note inside the
    // catch, BEFORE returning, so the summary lands a microtask later — asking
    // whether the red banner is absent any earlier passes no matter what.
    const summary = await screen.findByText(/not applied: torrent settings/i);
    expect(summary.className).not.toContain("text-danger");
    expect(screen.queryByText(/failed: torrent settings/i)).toBeNull();
    // And nothing was persisted, so the card stays dirty and Save stays live
    // for the retry the note asks for.
    expect(saveButton().disabled).toBe(false);
  });

  it("a 400 validation failure surfaces the server's message as an error", async () => {
    stubFetch(
      () =>
        new Response("listenPort must be between 1024 and 65535", {
          status: 400,
          headers: { "Content-Type": "text/plain" },
        }),
    );
    await mountLoaded();
    editSomething();
    fireEvent.click(saveButton());

    await screen.findByText("listenPort must be between 1024 and 65535");
    // The retryable framing must NOT appear for a genuine validation failure.
    expect(screen.queryByText(/Try saving again once/i)).toBeNull();
    // The other half of the 409 case's contract: de-escalating a refusal must
    // not have de-escalated real failures too. A 400 still rejects, so the
    // section still names this row in red.
    expect(
      (await screen.findByText(/failed: torrent settings/i)).className,
    ).toContain("text-danger");
  });
});

describe("Torrent settings — client-side bounds gate the Save button", () => {
  // The bounds mirror the PUT handler's own validation so an out-of-range field
  // blocks the click rather than producing a 400 after it. Nothing tested this
  // predicate or its registration, so either could have been deleted silently.
  it("blocks Save while the listen port is out of range, and frees it once corrected", async () => {
    stubFetch(() => json(LIVE_RESULT));
    await mountLoaded();
    const port = screen.getByLabelText("Listen port");

    // Editing makes the card dirty, so `valid` is the ONLY thing that can still
    // hold the button disabled here — which is what makes this cover the wiring
    // and not just the predicate: drop `valid` from the useSectionSaveItem
    // registration and SectionSave's anyInvalid() goes false, enabling the
    // button while the port is 80.
    fireEvent.input(port, { target: { value: "80" } });
    expect(saveButton().disabled).toBe(true);

    fireEvent.input(port, { target: { value: "65536" } });
    expect(saveButton().disabled).toBe(true);

    fireEvent.input(port, { target: { value: "6881" } });
    expect(saveButton().disabled).toBe(false);
  });

  it("blocks Save on an out-of-range field that renders no control at all", async () => {
    // maxConcurrent has no control by design, but the handler rejects anything
    // below 1 — so a stored 0 must disable Save rather than let the operator
    // click it and read a 400 naming a field that is nowhere on screen.
    stubFetch(() => json(LIVE_RESULT), {
      config: { ...FIXTURE, maxConcurrent: 0 },
    });
    await mountLoaded();

    fireEvent.input(screen.getByLabelText("Seed ratio limit"), {
      target: { value: "1" },
    });
    expect(saveButton().disabled).toBe(true);
  });
});

describe("Torrent settings — initial load failure", () => {
  it("shows the error alone, with no lingering Loading line", async () => {
    stubFetch(() => json(LIVE_RESULT), {
      get: () =>
        new Response("downloader unavailable", {
          status: 500,
          headers: { "Content-Type": "text/plain" },
        }),
    });
    render(() => <TorrentSection />);

    await screen.findByText(/Couldn't load the torrent settings/);
    // "Not loaded" is two states — still in flight, or failed — sharing one
    // fallback slot. Rendering both at once told the operator something was
    // still loading that had already failed, and that bug was fixed once
    // already; this guards the <Show when={!loadError()}> gate that fixed it.
    expect(screen.queryByText(/Loading torrent settings/)).toBeNull();
  });
});

describe("Torrent settings — stale handling copy", () => {
  it("warns that a requeued title waits when unattended auto-grab is off", async () => {
    stubFetch(() => json(LIVE_RESULT), { autoGrab: false });
    await mountLoaded();
    await screen.findByText(/waits in Requests/i);
  });

  it("shows no auto-grab caveat when auto-grab is on", async () => {
    stubFetch(() => json(LIVE_RESULT), { autoGrab: true });
    await mountLoaded();
    await waitFor(() =>
      expect(screen.queryByText(/waits in Requests/i)).toBeNull(),
    );
  });
});
