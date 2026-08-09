// SourcePreview — the shared source-file video preview conventions, and the
// two caller-facing affordances that mount them.
//
// Spec: .omc/plans/autopilot-impl-rename-preview.md §4. Two real callers on
// day one (Rename's queue-row popout, SearchTakeover's inline disclosure)
// share a set of *attribute decisions* on the <video> element — not layout.
// The containers stay separate (a grid does not fit a table cell, and a
// modal must not open inside a non-modal takeover), so this file exports
// three small pieces instead of one all-purpose component:
//
//   SourcePreviewVideo      the <video> conventions, defined once
//   SourcePreviewPopout     Rename's row control — icon button -> Modal
//   SourcePreviewDisclosure SearchTakeover's control — inline toggle
//
// Imports are deliberately limited to Modal, Button, and lucide-solid — NOT
// Rename.tsx, Dedup.tsx, or SearchTakeover.tsx. That independence is what
// makes this a parallelizable track: nothing here can accidentally couple
// to any one caller's internals.
//
// Route shape this component's `src` is expected to point at (never
// constructed here — see proposalVideoUrl, frontend/src/api/organize.ts):
// `/api/modes/{mode}/proposals/{id}/video`. The `/modes/{mode}/` segment is
// a deliberate security decision, not a naming one — it's what keeps the
// Adult-content section lock's path-level classification reachable for this
// route (see internal/api/proposal_video.go and
// internal/sectionlock/routes.go). Any caller wiring a `src` into these
// components must build it with `proposalVideoUrl`, never by hand, so that
// route shape isn't silently violated later.
import { type Component, createSignal, Show } from "solid-js";
import MonitorPlay from "lucide-solid/icons/monitor-play";
import { Modal } from "../screens/discover/shared";
import { Button } from "./ui";

// SourcePreviewVideo is THE definition of this feature's video conventions
// and the only place they live.
//
//   preload="none"  no bytes are fetched until the operator hits play.
//   controls        native browser controls; no custom scrubber (spec Non-Goal).
//   NOT muted       DELIBERATE DIVERGENCE from Dedup's tile (Dedup.tsx, the
//                   card-view candidate tiles), which stays `muted` because a
//                   page there can show N tiles simultaneously. This is a
//                   single pop-out/disclosure, not a page of N, so the
//                   muted-by-default that prevents audio chaos there is
//                   actively wrong here (spec Round 7). Do NOT "align" the
//                   two — a future "consistency" pass that adds `muted` here
//                   is reversing a considered decision, not fixing a bug.
//   no autoplay     spec Non-Goal — no `autoplay`, no `.play()` call.
export const SourcePreviewVideo: Component<{ src: string; label: string }> = (
  props,
) => (
  <video
    class="w-full rounded bg-surface-2"
    controls
    preload="none"
    src={props.src}
    aria-label={`Preview of ${props.label}`}
  />
);

// SourcePreviewPopout — Rename's row control: an icon button that opens the
// shared Modal (screens/discover/shared.tsx) with the video inside.
//
// The button and the video carry DIFFERENT aria-labels, and this is
// load-bearing for the tests, not cosmetic: "Open preview for <label>" on
// the button, "Preview of <label>" on the video (SourcePreviewVideo above).
// Both are mounted simultaneously while the modal is open, so one shared
// string would make getByLabelText throw on multiple matches, and would
// make "closing unmounts the video" assertions silently match the
// still-present button instead — passing for the wrong reason.
//
// The video is only mounted while the modal is open (<Show when={open()}>).
// Combined with preload="none", closing the modal unmounts the element and
// stops playback — no pause bookkeeping, no ref, no HTMLMediaElement API
// calls anywhere.
export const SourcePreviewPopout: Component<{ src: string; label: string }> = (
  props,
) => {
  const [open, setOpen] = createSignal(false);
  return (
    <>
      <button
        type="button"
        class="rounded p-1 text-muted hover:text-fg"
        aria-label={`Open preview for ${props.label}`}
        onClick={() => setOpen(true)}
      >
        <MonitorPlay size={16} />
      </button>
      <Show when={open()}>
        <Modal title={props.label} onClose={() => setOpen(false)}>
          <SourcePreviewVideo src={props.src} label={props.label} />
        </Modal>
      </Show>
    </>
  );
};

// SourcePreviewDisclosure — SearchTakeover's affordance: a text button that
// toggles the video INLINE. Never a Modal: SearchTakeover is deliberately
// not a dialog, and both Modal and DetailPopup are `fixed inset-0 z-50` with
// a backdrop click that closes whatever is underneath — the nested-modal
// hazard CLAUDE.md documents for the season/episode picker. An inline
// disclosure stacks nothing.
//
// Collapsed default ("Preview source file") is the spec's click-to-expand
// only, not always-visible constraint — a persistent always-shown preview
// panel was explicitly rejected (Round 2). Same split-label rule as
// SourcePreviewPopout: "Preview source file" / "Hide preview" on the toggle,
// "Preview of <label>" on the video.
export const SourcePreviewDisclosure: Component<{
  src: string;
  label: string;
}> = (props) => {
  const [open, setOpen] = createSignal(false);
  return (
    <div>
      <Button onClick={() => setOpen((v) => !v)}>
        {open() ? "Hide preview" : "Preview source file"}
      </Button>
      <Show when={open()}>
        <div class="mt-2 max-w-xl">
          <SourcePreviewVideo src={props.src} label={props.label} />
        </div>
      </Show>
    </div>
  );
};
