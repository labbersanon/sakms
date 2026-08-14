// TrackedPlayback — in-app playback for a Library Movies file.
//
// Spec constraints this file owns:
//   - Inline, never a Modal. Library detail is already a Modal; nesting
//     SourcePreviewPopout would stack two fixed inset-0 dialogs.
//   - preload="none"  no bytes until the operator hits the native play control.
//   - controls        native scrubber; no custom timeline.
//   - NOT muted       this is watching a title, not a poster still.
//   - no autoplay     the Play button only mounts the element.
//   - Fullscreen      explicit button via requestFullscreen / webkitEnterFullscreen
//                     so the option is visible even when native controls hide it.
//
// Split labels are load-bearing for tests (same reason as SourcePreview):
//   Play / Hide player for {label}  on the toggle
//   Play {label} fullscreen         on the Fullscreen button
//   Video player for {label}        on the <video>
import { type Component, createSignal, Show } from "solid-js";
import Maximize2 from "lucide-solid/icons/maximize-2";
import { Button } from "./ui";

type VideoWithWebkit = HTMLVideoElement & {
  webkitEnterFullscreen?: () => void;
  webkitRequestFullscreen?: () => Promise<void>;
};

export async function enterVideoFullscreen(el: HTMLVideoElement): Promise<void> {
  const v = el as VideoWithWebkit;
  if (typeof v.requestFullscreen === "function") {
    try {
      await v.requestFullscreen();
      return;
    } catch {
      // iOS and some embedded WebViews reject Element.requestFullscreen
      // on <video>; fall through to webkitEnterFullscreen.
    }
  }
  if (typeof v.webkitEnterFullscreen === "function") {
    v.webkitEnterFullscreen();
    return;
  }
  if (typeof v.webkitRequestFullscreen === "function") {
    await v.webkitRequestFullscreen();
  }
}

export const TrackedPlayback: Component<{ src: string; label: string }> = (
  props,
) => {
  const [open, setOpen] = createSignal(false);
  const [wantFs, setWantFs] = createSignal(false);
  let videoEl: HTMLVideoElement | undefined;

  const bindVideo = (el: HTMLVideoElement) => {
    videoEl = el;
    if (wantFs()) {
      void enterVideoFullscreen(el);
      setWantFs(false);
    }
  };

  const onFullscreen = () => {
    if (videoEl) {
      void enterVideoFullscreen(videoEl);
      return;
    }
    setWantFs(true);
    setOpen(true);
  };

  return (
    <div class="mt-1">
      <div class="flex flex-wrap gap-2">
        <Button
          aria-label={
            open() ? `Hide player for ${props.label}` : `Play ${props.label}`
          }
          onClick={() =>
            setOpen((v) => {
              if (v) videoEl = undefined;
              return !v;
            })
          }
        >
          {open() ? "Hide player" : "Play"}
        </Button>
        <Button
          aria-label={`Play ${props.label} fullscreen`}
          onClick={onFullscreen}
        >
          <span class="inline-flex items-center gap-1">
            <Maximize2 size={14} />
            Fullscreen
          </span>
        </Button>
      </div>
      <Show when={open()}>
        <div class="mt-2">
          <video
            ref={bindVideo}
            class="w-full rounded bg-surface-2"
            controls
            preload="none"
            src={props.src}
            aria-label={`Video player for ${props.label}`}
          />
        </div>
      </Show>
    </div>
  );
};
