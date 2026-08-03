// Section PIN Lock panel — one operator-chosen PIN plus a checkbox per
// lockable section.
//
// PLACEMENT: this is composed into GlobalSection, NOT AdvancedSection, for
// the same reason APISection is (see Global.tsx's note): AdvancedSection
// takes a `mode: () => Mode` and is governed by the Advanced tab's mode
// selector, and a section lock is mode-INDEPENDENT. GlobalSection renders
// above that selector, inside the same `advanced` tab, so the spec's
// "Settings → Advanced" placement is satisfied without tying the panel to a
// mode that does not affect it.
//
// THE ANTI-RBAC INVARIANT this panel must not be "improved" past: ONE PIN,
// ONE unlock ticket, and the ticket unlocks EVERY locked section at once. The
// checkboxes choose WHAT is locked; they never imply per-section credentials.
//
// currentPin IS REQUIRED IN THE BODY OF EVERY WRITE once a PIN exists — even
// from a browser already holding a live unlock ticket. The backend
// (internal/api/sectionlock.go's authorizeChange) enforces that deliberately:
// the ticket means "the operator entered the PIN recently", which is the
// right bar for viewing a locked section and the wrong one for rewriting the
// lock's own configuration. That is why every action here routes through the
// one currentPin field rather than assuming the ticket is enough.

import {
  type Component,
  createEffect,
  createSignal,
  For,
  on,
  Show,
} from "solid-js";
import {
  ALL_LOCKABLE_SECTIONS,
  buildSectionsPayload,
  clearSectionLockPin,
  lockSections,
  sectionLabel,
  setSectionLockPin,
  setSectionLockSections,
} from "../../api/sectionLock";
import { SectionLockedError, SectionLockLockoutError } from "../../api/client";
import {
  Button,
  inputClass,
  labelClass,
  Muted,
  useSectionLock,
} from "../../components/ui";
import { Card, SaveStatus, useSaveStatus } from "./shared";

// describeWriteError translates the backend's PIN-refusal shapes into copy an
// operator can act on. A wrong currentPin comes back as the SAME 403
// section_locked body the path gate writes, so passing err.message straight
// through would render "section locked" on a settings save — technically the
// server's word for it, and useless to the person who mistyped their PIN.
function describeWriteError(e: unknown): Error {
  if (e instanceof SectionLockLockoutError) {
    return new Error(
      `Too many failed PIN attempts — try again in ${e.retryAfter} second${e.retryAfter === 1 ? "" : "s"}.`,
    );
  }
  if (e instanceof SectionLockedError) {
    return new Error("Current PIN is incorrect.");
  }
  return e instanceof Error ? e : new Error(String(e));
}

export const SectionLockSection: Component = () => {
  // State comes from the shell's SectionLockContext rather than a second
  // resource of this panel's own — one fetch at boot, one refetch after every
  // successful write, exactly the AdultModeSection pattern. A private
  // resource here would leave the sidebar badges and the content overlay
  // showing stale state after a save.
  const lock = useSectionLock();

  const [checked, setChecked] = createSignal<Set<string>>(new Set());
  const [currentPin, setCurrentPin] = createSignal("");
  const [newPin, setNewPin] = createSignal("");
  const sectionsStatus = useSaveStatus();
  const pinStatus = useSaveStatus();
  const [busy, setBusy] = createSignal(false);

  // Seed the checkboxes from the server's locked set, and re-seed whenever it
  // changes underneath us (a refetch after any successful write).
  createEffect(
    on(lock.lockedSections, (stored) => {
      setChecked(new Set(stored));
    }),
  );

  const toggle = (section: string, on: boolean) => {
    setChecked((prev) => {
      const next = new Set(prev);
      if (on) next.add(section);
      else next.delete(section);
      return next;
    });
  };

  // saveSections is the ONE write path, called by both the Save button and
  // "Lock everything". Both build their array with buildSectionsPayload over
  // the same canonical ordered list, so the two requests are byte-identical
  // when the same sections are selected — which is precisely what AC3's
  // "equivalent to checking every individual box" claims and what FE-2
  // compares. Do not let either caller assemble its own array.
  const saveSections = async (next: Set<string>) => {
    setBusy(true);
    sectionsStatus.set("");
    try {
      await setSectionLockSections(buildSectionsPayload(next), currentPin());
      setCurrentPin("");
      sectionsStatus.saved();
      lock.refetch();
    } catch (e) {
      sectionsStatus.failed(describeWriteError(e));
    } finally {
      setBusy(false);
    }
  };

  // lockEverything is a PURE FRONTEND CONVENIENCE (§5.3): it checks every box
  // and PUTs the same explicit array a manual all-checked save would. There is
  // deliberately NO backend "lock all" flag — a flag would make AC3's
  // equivalence claim an assertion instead of a literal fact.
  const lockEverything = async () => {
    const all = new Set(ALL_LOCKABLE_SECTIONS);
    setChecked(all);
    await saveSections(all);
  };

  const savePin = async () => {
    setBusy(true);
    pinStatus.set("");
    try {
      await setSectionLockPin(currentPin(), newPin());
      setNewPin("");
      setCurrentPin("");
      pinStatus.saved();
      lock.refetch();
    } catch (e) {
      pinStatus.failed(describeWriteError(e));
    } finally {
      setBusy(false);
    }
  };

  // removePin clears the PIN AND, in the backend's same write, the locked set
  // — a locked set with no PIN in existence is the one state no credential
  // could ever clear.
  const removePin = async () => {
    setBusy(true);
    pinStatus.set("");
    try {
      await clearSectionLockPin(currentPin());
      setCurrentPin("");
      setNewPin("");
      pinStatus.saved();
      lock.refetch();
    } catch (e) {
      pinStatus.failed(describeWriteError(e));
    } finally {
      setBusy(false);
    }
  };

  // lockNow drops the unlock ticket immediately. The ticket's 30-minute
  // lifetime is ABSOLUTE and non-sliding, so this is only ever an early exit —
  // but without it an operator who unlocked to check one thing has no way to
  // re-arm the lock before walking away from the screen, which is the exact
  // situation this whole feature exists for. It is never privileged (giving up
  // a credential needs no credential), so it takes no PIN.
  const lockNow = async () => {
    setBusy(true);
    sectionsStatus.set("");
    try {
      await lockSections();
      sectionsStatus.set("locked");
      lock.refetch();
    } catch (e) {
      sectionsStatus.failed(describeWriteError(e));
    } finally {
      setBusy(false);
    }
  };

  // THREE DIFFERENT STATES, not one. enforcementAvailable() === false is the
  // answer to "did the status fetch report enforcement as off?", and reading
  // it alone conflates that with "the fetch hasn't landed yet" and "the fetch
  // failed" — both of which used to render the alarming disabled-enforcement
  // banner below and disable the whole panel on nothing but a slow network.
  // ready/error are optional on SectionLockControl (a hand-built control in a
  // standalone test supplies neither), so default to settled-and-fine.
  const ready = () => lock.ready?.() ?? true;
  const loadFailed = () => lock.error?.() ?? false;
  // Genuinely disabled: the fetch actually succeeded AND said so.
  const enforcementOff = () =>
    ready() && !loadFailed() && !lock.enforcementAvailable();

  const disabled = () =>
    busy() || !ready() || loadFailed() || !lock.enforcementAvailable();

  return (
    <Card title="Section PIN Lock — global">
      <Muted class="mb-3">
        One PIN gates whichever sections you check below. Entering it unlocks
        every locked section at once for 30 minutes — it is a single shared
        secret, not a per-section credential or a user account. Out-of-process
        clients using an API key must additionally send the PIN as an
        X-Section-Pin header.
      </Muted>

      <Show when={!ready()}>
        <Muted class="mb-3">Loading section lock status…</Muted>
      </Show>

      <Show when={ready() && loadFailed()}>
        <p class="mb-3 text-sm text-danger">
          Couldn't load section lock status, so the controls below are disabled
          — try reloading. This says nothing about whether the lock is
          enforcing; it only means this panel could not find out.
        </p>
      </Show>

      <Show when={enforcementOff()}>
        <p class="mb-3 text-sm text-danger">
          The section lock cannot enforce anything on this instance — it is
          either disabled by SAKMS_SECTION_LOCK_DISABLE, or this instance runs
          auth mode "none", which already leaves everything open. The controls
          below are disabled.
        </p>
      </Show>

      <div class="mb-4">
        <label class={labelClass} for="section-lock-current-pin">
          Current PIN
        </label>
        <input
          id="section-lock-current-pin"
          type="password"
          class={`${inputClass} mt-1`}
          aria-label="Current PIN"
          name="section-pin"
          autocomplete="new-password"
          value={currentPin()}
          disabled={disabled()}
          onInput={(e) => setCurrentPin(e.currentTarget.value)}
        />
        <Muted class="mt-1">
          {lock.pinSet()
            ? "Required to change the PIN or the locked sections below."
            : "No PIN is set yet — leave this blank and set one below."}
        </Muted>
      </div>

      <div class="mb-4">
        <label class={labelClass} for="section-lock-new-pin">
          {lock.pinSet() ? "New PIN" : "Set a PIN"}
        </label>
        <input
          id="section-lock-new-pin"
          type="password"
          class={`${inputClass} mt-1`}
          aria-label={lock.pinSet() ? "New PIN" : "Set a PIN"}
          name="section-pin"
          autocomplete="new-password"
          value={newPin()}
          disabled={disabled()}
          onInput={(e) => setNewPin(e.currentTarget.value)}
        />
        <Muted class="mt-1">Minimum 6 characters.</Muted>
        <div class="mt-2 flex items-center gap-3">
          <Button
            variant="primary"
            disabled={disabled() || !newPin()}
            onClick={() => void savePin()}
          >
            {lock.pinSet() ? "Change PIN" : "Set PIN"}
          </Button>
          <Show when={lock.pinSet()}>
            {/* DELIBERATELY NOT disabled() — this one button is exempt from
                the enforcementAvailable() half. The backend special-cases
                PIN-clearing to work WITHOUT the normal auth requirements
                precisely when the instance runs SAKMS_SECTION_LOCK_DISABLE=1,
                because that is the only path out of a corrupt bcrypt hash
                (docs/break-glass-recovery.md). Disabling it here made the
                documented recovery path reachable only by raw curl. Note the
                other enforcementAvailable() === false case, auth mode "none",
                answers 409 — the operator sees that error instead of a dead
                button, which is the honest outcome. `busy()` still applies:
                one request at a time. */}
            <Button disabled={busy()} onClick={() => void removePin()}>
              Remove PIN
            </Button>
          </Show>
          <SaveStatus
            text={pinStatus.status().text}
            error={pinStatus.status().error}
          />
        </div>
        <Show when={lock.pinSet()}>
          <Muted class="mt-1">
            Removing the PIN also unlocks every section — a locked section with
            no PIN in existence could never be unlocked again.
          </Muted>
        </Show>
      </div>

      <div class={labelClass}>Locked sections</div>
      <div class="mb-3 mt-1 space-y-1">
        <For each={ALL_LOCKABLE_SECTIONS}>
          {(section) => (
            <label class="flex items-center gap-2">
              <input
                type="checkbox"
                aria-label={`Lock ${sectionLabel(section)}`}
                checked={checked().has(section)}
                disabled={disabled()}
                onChange={(e) => toggle(section, e.currentTarget.checked)}
              />
              <span class="text-sm text-fg">{sectionLabel(section)}</span>
            </label>
          )}
        </For>
      </div>
      {/* SCOPE CLAIM, kept deliberately narrow. This used to promise Adult
          material was locked "wherever it appears, on every tab", which the
          feature does not deliver and never claimed to: the plan's §14 accepts
          R-1 (raw /api/images/proxy URLs are mode-agnostic), R-3 (the global
          notifications stream can name an Adult title) and R-4 (outbound
          webhook payloads) as residual, unclosed risks. Word this so it does
          not contradict that list. */}
      <Muted class="mb-3">
        Adult content is not a sidebar tab — checking it locks Adult material on
        Discover and on the Movies/Series/Adult mode tabs across the app,
        without locking the Movies and Series content on those same tabs.
      </Muted>

      <div class="flex items-center gap-3">
        <Button
          variant="primary"
          disabled={disabled()}
          onClick={() => void saveSections(checked())}
        >
          Save locked sections
        </Button>
        <Button disabled={disabled()} onClick={() => void lockEverything()}>
          Lock everything
        </Button>
        <Show when={lock.unlocked()}>
          <Button disabled={disabled()} onClick={() => void lockNow()}>
            Re-lock now
          </Button>
        </Show>
        <SaveStatus
          text={sectionsStatus.status().text}
          error={sectionsStatus.status().error}
        />
      </div>
    </Card>
  );
};
