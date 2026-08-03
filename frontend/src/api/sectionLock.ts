// Section PIN lock data access — the six /api/section-lock/* routes plus the
// canonical list of lockable sections the Settings panel renders checkboxes
// for.
//
// Every call goes through api() (./client) so it inherits the session cookie
// and, now, the SectionLockedError branch that keeps a 403 section refusal off
// the 401 reboot path. Request/response shapes are the generated DTOs (@dto),
// never hand-duplicated — the one exception is the 429 lockout body, which has
// no apidto mirror and is typed in client.ts where it is parsed.
//
// This module is deliberately a LEAF: it imports ./client and @dto and nothing
// else. See LOCKABLE_TAB_SECTIONS below for why that matters.

import { api } from "./client";
import type {
  SectionLockPinRequest,
  SectionLockSectionsRequest,
  SectionLockStatusResponse,
  SectionLockUnlockRequest,
} from "@dto";

// LOCKABLE_TAB_SECTIONS is the eight sidebar tabs, as section ids (each
// NAV_ITEM's href minus its leading "/"), in sidebar order.
//
// WHY THIS IS AUTHORED HERE RATHER THAN DERIVED FROM NAV_ITEMS. Deriving it
// (`NAV_ITEMS.map(i => i.href.slice(1))`) needs an import of
// screens/AppShell.tsx, which imports this module back — AppShell owns the
// SectionLockContext resource, and components/ui.tsx's unlock overlay imports
// this module too. That is a hard ESM cycle, and it is not theoretical: the
// derived form was written, run, and it fails with
//
//   TypeError: Cannot read properties of undefined (reading 'map')
//     at src/api/sectionLock.ts  (from src/components/ui.tsx)
//
// whenever AppShell is the module entered FIRST — which is every real boot
// (index.tsx -> App -> AppShell) and, under test, App.test.tsx,
// routing.test.ts and this list's own drift test. AppShell's evaluation
// reaches ui.tsx -> this module -> back into the still-evaluating AppShell,
// whose NAV_ITEMS binding is not initialised yet. The reverse import order
// happens to work, which is exactly what makes the bug so easy to ship.
//
// It ALSO makes the drift test vacuous, since it would then compare a list
// against itself.
//
// The drift guard the plan (§5.4) actually asks for is the SET COMPARISON in
// sectionLock.nav.test.ts, and that test only means anything while these two
// lists are authored independently. So: adding a sidebar tab to NAV_ITEMS
// without adding its id here FAILS that test, which is the point.
//
// Go is authoritative for the canonical list (internal/sectionlock/sections.go);
// its own TestTabSections is the other half of the same guard.
export const LOCKABLE_TAB_SECTIONS = [
  "dashboard",
  "discover",
  "library",
  "queue",
  "organize",
  "tag",
  "collections",
  "settings",
] as const;

// ADULT_CONTENT_SECTION is the ninth lockable section and is NOT a sidebar
// tab: it cuts across every tab that can show Adult material, so it is gated
// independently of whichever tab that material appears under.
export const ADULT_CONTENT_SECTION = "adult-content";

// ALL_LOCKABLE_SECTIONS is the full checkbox set for the Settings panel: the
// eight tabs plus adult-content, in that fixed order.
//
// ORDER IS LOAD-BEARING, not cosmetic. Both the per-checkbox save and the
// "lock everything" convenience build their request array by filtering THIS
// list (see buildSectionsPayload), so the two produce byte-identical bodies —
// which is exactly what AC8's "equivalent to checking every individual box"
// asserts, and what FE-2 compares.
export const ALL_LOCKABLE_SECTIONS: string[] = [
  ...LOCKABLE_TAB_SECTIONS,
  ADULT_CONTENT_SECTION,
];

// SECTION_LABELS is the display name per section id. Kept beside the id list
// so a new section can never render as a bare slug.
const SECTION_LABELS: Record<string, string> = {
  dashboard: "Dashboard",
  discover: "Discover",
  library: "Library",
  queue: "Queue",
  organize: "Organize",
  tag: "Tag",
  collections: "Collections",
  settings: "Settings",
  "adult-content": "Adult content",
};

// sectionLabel renders a section id for display, falling back to the raw id
// for an unrecognised one (the backend preserves unknown stored ids rather
// than dropping them — see §5.2 — so one can legitimately come back).
export function sectionLabel(id: string): string {
  return SECTION_LABELS[id] ?? id;
}

// buildSectionsPayload turns a checked-set into the ordered array PUT
// /sections takes. Single builder on purpose — see ALL_LOCKABLE_SECTIONS.
export function buildSectionsPayload(checked: Set<string>): string[] {
  return ALL_LOCKABLE_SECTIONS.filter((s) => checked.has(s));
}

// fetchSectionLockStatus is the only route the frontend may call while
// everything else is locked: the backend exempts it from the gate precisely
// so the sidebar can draw lock badges (and the overlay can offer a PIN box)
// while `settings` itself is locked.
export function fetchSectionLockStatus(): Promise<SectionLockStatusResponse> {
  return api<SectionLockStatusResponse>("/api/section-lock/status");
}

// unlockSections exchanges the PIN for the sakms_unlock ticket cookie. ONE
// ticket unlocks EVERY locked section at once — it carries no per-section
// scope, by design (the anti-RBAC invariant). A wrong PIN comes back 403
// section_locked (SectionLockedError); too many wrong PINs come back 429
// section_lock_lockout (SectionLockLockoutError).
export function unlockSections(pin: string): Promise<void> {
  const body: SectionLockUnlockRequest = { pin };
  return api<void>("/api/section-lock/unlock", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// lockSections drops the ticket cookie. Never privileged — giving up a
// credential needs no credential — so it takes no PIN.
export function lockSections(): Promise<void> {
  return api<void>("/api/section-lock/lock", { method: "POST" });
}

// setSectionLockPin sets or changes the PIN (minimum 6 characters,
// enforced server-side).
//
// currentPin is required from the BODY whenever a PIN already exists, EVEN
// when the browser already holds a live unlock ticket: the ticket says "the
// operator entered the PIN recently", which is the right bar for viewing a
// locked section and the wrong one for rewriting the lock's own config. Pass
// "" only when no PIN is set yet.
export function setSectionLockPin(
  currentPin: string,
  newPin: string,
): Promise<void> {
  const body: SectionLockPinRequest = { currentPin, newPin };
  return api<void>("/api/section-lock/pin", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

// clearSectionLockPin removes the PIN AND, in the same backend write, the
// locked-section set — a locked set with no PIN in existence is the one state
// no credential could ever clear.
export function clearSectionLockPin(currentPin: string): Promise<void> {
  const body: SectionLockPinRequest = { currentPin, newPin: "" };
  return api<void>("/api/section-lock/pin", {
    method: "DELETE",
    body: JSON.stringify(body),
  });
}

// setSectionLockSections replaces the locked set wholesale (it is a full
// replacement, never a delta). A non-empty array while no PIN is set is
// rejected 400 by the backend — that is a recovery invariant, not input
// validation.
export function setSectionLockSections(
  sections: string[],
  currentPin: string,
): Promise<void> {
  const body: SectionLockSectionsRequest = { currentPin, sections };
  return api<void>("/api/section-lock/sections", {
    method: "PUT",
    body: JSON.stringify(body),
  });
}
