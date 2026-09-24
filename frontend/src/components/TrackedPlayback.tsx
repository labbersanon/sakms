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
import { type Component, createSignal, onCleanup, Show } from "solid-js";
import Maximize2 from "lucide-solid/icons/maximize-2";
import { Button } from "./ui";

export type VideoProgress = {
  position: number;
  duration: number;
  ended: boolean;
};

// bindVideoProgress reports timeupdate (throttled 5s), pause, and ended.
// Claude 2026-09-24: Series play head only; Movies/Adult omit onProgress.
// Reason: PUT .../progress must not fire on every timeupdate tick.
// Troubleshooting: timeupdate fires many times per second.
export function bindVideoProgress(
  el: HTMLVideoElement,
  onProgress?: (info: VideoProgress) => void,
): () => void {
  if (!onProgress) return () => undefined;
  let last = 0;
  const emit = (ended: boolean) => {
    const duration = Number.isFinite(el.duration) ? el.duration : 0;
    onProgress({ position: el.currentTime || 0, duration, ended });
  };
  const onTime = () => {
    const now = Date.now();
    if (now - last < 5000) return;
    last = now;
    emit(false);
  };
  const onPause = () => emit(false);
  const onEnded = () => emit(true);
  el.addEventListener("timeupdate", onTime);
  el.addEventListener("pause", onPause);
  el.addEventListener("ended", onEnded);
  return () => {
    el.removeEventListener("timeupdate", onTime);
    el.removeEventListener("pause", onPause);
    el.removeEventListener("ended", onEnded);
  };
}

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

// PlayFullscreenLink is the Library header control under Watch Trailer.
// One click mounts the same inline <video> as TrackedPlayback, enters
// fullscreen, and starts playback. Discover catalog-only omits playSrc.
// Owned series passes the next episode URL (Play Show / Resume Show).
export const PlayFullscreenLink: Component<{
  src: string;
  noun: "Movie" | "Show" | "Scene";
  title: string;
  class?: string;
  resume?: boolean;
  onProgress?: (info: VideoProgress) => void;
}> = (props) => {
  const [open, setOpen] = createSignal(false);
  const [wantFs, setWantFs] = createSignal(false);
  let videoEl: HTMLVideoElement | undefined;
  let unbindProgress: (() => void) | undefined;
  onCleanup(() => unbindProgress?.());

  const start = (el: HTMLVideoElement) => {
    void (async () => {
      await enterVideoFullscreen(el);
      await el.play().catch(() => undefined);
    })();
  };

  const bindVideo = (el: HTMLVideoElement) => {
    videoEl = el;
    unbindProgress?.();
    unbindProgress = bindVideoProgress(el, props.onProgress);
    if (wantFs()) {
      start(el);
      setWantFs(false);
    }
  };

  const playLabel = () =>
    props.resume && props.noun === "Show"
      ? "Resume Show →"
      : `Play ${props.noun} →`;

  return (
    <>
      <button
        type="button"
        class={
          props.class ??
          "inline-flex w-full items-center justify-center rounded-md border border-border bg-surface-2 px-3 py-1.5 text-xs font-medium text-fg transition hover:opacity-90"
        }
        aria-label={
          props.resume && props.noun === "Show"
            ? "Resume Show fullscreen"
            : `Play ${props.noun} fullscreen`
        }
        onClick={() => {
          if (videoEl) {
            start(videoEl);
            return;
          }
          setWantFs(true);
          setOpen(true);
        }}
      >
        {playLabel()}
      </button>
      <Show when={open()}>
        {/* Claude 2026-08-14: bg-black so fullscreen pillarbox is not cream.
            Reason: bg-surface-2 painted the unused fullscreen area.
            Review if: a themed player chrome is added. */}
        <video
          ref={bindVideo}
          class="mt-2 w-full rounded bg-black"
          controls
          autoplay
          playsinline
          preload="metadata"
          src={props.src}
          aria-label={`Fullscreen player for ${props.title}`}
        />
      </Show>
    </>
  );
};

export const TrackedPlayback: Component<{
  src: string;
  label: string;
  onProgress?: (info: VideoProgress) => void;
}> = (props) => {
  const [open, setOpen] = createSignal(false);
  const [wantFs, setWantFs] = createSignal(false);
  let videoEl: HTMLVideoElement | undefined;
  let unbindProgress: (() => void) | undefined;
  onCleanup(() => unbindProgress?.());

  const bindVideo = (el: HTMLVideoElement) => {
    videoEl = el;
    unbindProgress?.();
    unbindProgress = bindVideoProgress(el, props.onProgress);
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
            class="w-full rounded bg-black"
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
