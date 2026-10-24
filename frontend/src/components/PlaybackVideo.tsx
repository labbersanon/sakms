// PlaybackVideo attaches native src for Direct Play URLs and hls.js for HLS.
// hls.js is imported only when native HLS is missing so the app shell stays
// under Guardrail #9 (200 KB gzipped JS). Safari never downloads it.
import { type Component, onCleanup } from "solid-js";
import { isHlsSrc } from "../playback";

function playIfAutoplay(el: HTMLVideoElement) {
  if (el.autoplay) {
    void el.play().catch(() => undefined);
  }
}

function attachNative(el: HTMLVideoElement, src: string): () => void {
  el.src = src;
  playIfAutoplay(el);
  return () => {
    el.removeAttribute("src");
  };
}

function canPlayNativeHls(el: HTMLVideoElement): boolean {
  return el.canPlayType("application/vnd.apple.mpegurl") !== "";
}

export function bindMediaSrc(el: HTMLVideoElement, src: string): () => void {
  if (!isHlsSrc(src) || canPlayNativeHls(el)) {
    return attachNative(el, src);
  }

  let cancelled = false;
  let unbind: (() => void) | undefined;
  void import("hls.js").then(({ default: Hls }) => {
    if (cancelled) return;
    if (Hls.isSupported()) {
      const hls = new Hls({ maxBufferLength: 30 });
      hls.loadSource(src);
      hls.attachMedia(el);
      playIfAutoplay(el);
      unbind = () => {
        hls.destroy();
      };
      return;
    }
    unbind = attachNative(el, src);
  });
  return () => {
    cancelled = true;
    unbind?.();
  };
}

export const PlaybackVideo: Component<{
  src: string;
  class?: string;
  preload?: "none" | "metadata" | "auto";
  muted?: boolean;
  autoplay?: boolean;
  controls?: boolean;
  playsinline?: boolean;
  ariaLabel: string;
  ref?: (el: HTMLVideoElement) => void;
}> = (props) => {
  let unbind: (() => void) | undefined;
  onCleanup(() => unbind?.());
  return (
    <video
      // Claude 2026-10-21: data-src mirrors the intended media URL
      // Reason: HLS URLs bind via hls.js / native HLS and often leave the native src attribute unset in jsdom
      // Troubleshooting: Rename.preview tests read video.src and got null vs /video
      // Review if: PlaybackVideo always sets the native src attribute, or tests query via a different hook
      data-src={props.src}
      ref={(el) => {
        unbind?.();
        unbind = bindMediaSrc(el, props.src);
        props.ref?.(el);
      }}
      class={props.class}
      controls={props.controls !== false}
      preload={props.preload ?? "none"}
      muted={props.muted}
      autoplay={props.autoplay}
      playsinline={props.playsinline}
      aria-label={props.ariaLabel}
    />
  );
};
