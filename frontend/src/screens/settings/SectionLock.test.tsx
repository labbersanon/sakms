// FE-2 (AC8) — "lock everything" must PUT an array BYTE-EQUAL to the one a
// manual all-boxes-checked save produces.
//
// The comparison is on the raw request body STRING, not a deep-equal of the
// parsed objects, deliberately: AC3 claims the convenience is "equivalent to
// checking every individual box", and only a byte comparison makes that a
// literal fact rather than an assertion about JSON semantics. It is also what
// forces both paths through one array builder — if "lock everything" spread a
// literal while manual checking accumulated in click order, the parsed objects
// would still be deep-equal while the bodies differed.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { SectionLockSection } from "./SectionLock";
import {
  SectionLockContext,
  type SectionLockControl,
} from "../../components/ui";
import { ALL_LOCKABLE_SECTIONS, sectionLabel } from "../../api/sectionLock";

afterEach(() => vi.unstubAllGlobals());

// control is a fixed, fully-unlocked-but-armed state: a PIN exists (so the
// currentPin field is required, as the backend demands on every write) and
// nothing is locked yet, so every checkbox starts clear.
const control = (over: Partial<SectionLockControl> = {}): SectionLockControl => ({
  isLocked: () => false,
  lockedSections: () => [],
  pinSet: () => true,
  unlocked: () => true,
  enforcementAvailable: () => true,
  refetch: () => {},
  ...over,
});

const stubFetch = () => {
  const fn = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(null, { status: 204 }),
  );
  vi.stubGlobal("fetch", fn);
  return fn;
};

const renderPanel = () =>
  render(() => (
    <SectionLockContext.Provider value={control()}>
      <SectionLockSection />
    </SectionLockContext.Provider>
  ));

const typeCurrentPin = (pin: string) => {
  const input = screen.getByLabelText("Current PIN") as HTMLInputElement;
  fireEvent.input(input, { target: { value: pin } });
};

describe("FE-2 — lock everything equals checking every box", () => {
  it("both paths PUT a byte-identical body", async () => {
    // Path A: check all nine boxes by hand, then Save.
    const fetchA = stubFetch();
    const viewA = renderPanel();
    typeCurrentPin("hunter2");
    for (const section of ALL_LOCKABLE_SECTIONS) {
      fireEvent.click(screen.getByLabelText(`Lock ${sectionLabel(section)}`));
    }
    fireEvent.click(screen.getByText("Save locked sections"));
    await waitFor(() => expect(fetchA).toHaveBeenCalled());
    const [urlA, initA] = fetchA.mock.calls[0]!;
    viewA.unmount();

    // Path B: one click on the convenience.
    const fetchB = stubFetch();
    renderPanel();
    typeCurrentPin("hunter2");
    fireEvent.click(screen.getByText("Lock everything"));
    await waitFor(() => expect(fetchB).toHaveBeenCalled());
    const [urlB, initB] = fetchB.mock.calls[0]!;

    expect(String(urlA)).toBe("/api/section-lock/sections");
    expect(String(urlB)).toBe(String(urlA));
    expect(initB?.method).toBe("PUT");
    expect(initB?.method).toBe(initA?.method);
    // The byte comparison.
    expect(initB?.body).toBe(initA?.body);
    // ...and it is genuinely the full explicit array, not an empty one that
    // happens to match: no backend "lock all" flag exists (§5.3).
    expect(JSON.parse(String(initA?.body)).sections).toEqual(
      ALL_LOCKABLE_SECTIONS,
    );
  });

  it("only one request fires, and exactly one PUT — no separate 'lock all' route", async () => {
    const fetchMock = stubFetch();
    renderPanel();
    typeCurrentPin("hunter2");
    fireEvent.click(screen.getByText("Lock everything"));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  });
});

describe("SectionLock panel", () => {
  it("renders a checkbox per lockable section, including Adult content", () => {
    renderPanel();
    for (const section of ALL_LOCKABLE_SECTIONS) {
      expect(
        screen.getByLabelText(`Lock ${sectionLabel(section)}`),
      ).toBeInTheDocument();
    }
    // The ninth entry is the mode-scoped pseudo-section, not a sidebar tab.
    expect(screen.getByLabelText("Lock Adult content")).toBeInTheDocument();
  });

  it("sends currentPin in the body — the ticket alone is never enough", async () => {
    const fetchMock = stubFetch();
    renderPanel();
    typeCurrentPin("hunter2");
    fireEvent.click(screen.getByLabelText("Lock Organize"));
    fireEvent.click(screen.getByText("Save locked sections"));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const body = JSON.parse(String(fetchMock.mock.calls[0]![1]?.body));
    expect(body.currentPin).toBe("hunter2");
    expect(body.sections).toEqual(["organize"]);
  });

  it("maps a wrong currentPin to actionable copy, not the server's 'section locked'", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              error: "section locked",
              code: "section_locked",
              section: "",
            }),
            { status: 403, headers: { "Content-Type": "application/json" } },
          ),
      ),
    );
    renderPanel();
    typeCurrentPin("wrong1");
    fireEvent.click(screen.getByText("Save locked sections"));
    expect(
      await screen.findByText("Current PIN is incorrect."),
    ).toBeInTheDocument();
  });

  it("offers a re-lock only while a ticket is actually held", () => {
    // POST /lock is the operator's only way to drop the unlock ticket before
    // its absolute 30-minute lifetime elapses.
    renderPanel();
    expect(screen.getByText("Re-lock now")).toBeInTheDocument();
    render(() => (
      <SectionLockContext.Provider value={control({ unlocked: () => false })}>
        <SectionLockSection />
      </SectionLockContext.Provider>
    ));
    // Still exactly one — the second render (locked) contributes none.
    expect(screen.getAllByText("Re-lock now").length).toBe(1);
  });

  it("disables every control when enforcement is unavailable", () => {
    render(() => (
      <SectionLockContext.Provider
        value={control({ enforcementAvailable: () => false })}
      >
        <SectionLockSection />
      </SectionLockContext.Provider>
    ));
    expect(screen.getByLabelText("Lock Organize")).toBeDisabled();
    expect(screen.getByText("Lock everything")).toBeDisabled();
    expect(screen.getByText("Save locked sections")).toBeDisabled();
  });

  // Remove PIN is the ONE control exempt from the enforcement-unavailable
  // disable: SAKMS_SECTION_LOCK_DISABLE=1 is exactly the situation the
  // backend's PIN-clear special case exists for, and it is the documented only
  // way out of a corrupt bcrypt hash. See docs/break-glass-recovery.md.
  it("keeps Remove PIN clickable when enforcement is unavailable", () => {
    render(() => (
      <SectionLockContext.Provider
        value={control({ enforcementAvailable: () => false })}
      >
        <SectionLockSection />
      </SectionLockContext.Provider>
    ));
    expect(screen.getByText("Remove PIN")).not.toBeDisabled();
  });
});

// The three states enforcementAvailable() === false used to collapse into one.
// Only the third deserves the alarming banner; the other two are "we don't
// know yet" and "we couldn't find out".
describe("the enforcement-status banner distinguishes loading / failed / off", () => {
  const bannerText = /cannot enforce anything on this instance/;

  it("shows a neutral loading note, not the disabled banner, before the fetch settles", () => {
    render(() => (
      <SectionLockContext.Provider
        value={control({
          ready: () => false,
          enforcementAvailable: () => false,
        })}
      >
        <SectionLockSection />
      </SectionLockContext.Provider>
    ));
    expect(screen.getByText(/Loading section lock status/)).toBeInTheDocument();
    expect(screen.queryByText(bannerText)).not.toBeInTheDocument();
    expect(screen.getByText("Save locked sections")).toBeDisabled();
  });

  it("shows a distinct load-failure message, not the disabled banner", () => {
    render(() => (
      <SectionLockContext.Provider
        value={control({
          ready: () => true,
          error: () => true,
          enforcementAvailable: () => false,
        })}
      >
        <SectionLockSection />
      </SectionLockContext.Provider>
    ));
    expect(
      screen.getByText(/Couldn't load section lock status/),
    ).toBeInTheDocument();
    expect(screen.queryByText(bannerText)).not.toBeInTheDocument();
    expect(screen.getByText("Save locked sections")).toBeDisabled();
  });

  it("shows the disabled banner only when the fetch succeeded and reported it", () => {
    render(() => (
      <SectionLockContext.Provider
        value={control({
          ready: () => true,
          error: () => false,
          enforcementAvailable: () => false,
        })}
      >
        <SectionLockSection />
      </SectionLockContext.Provider>
    ));
    expect(screen.getByText(bannerText)).toBeInTheDocument();
  });
});
