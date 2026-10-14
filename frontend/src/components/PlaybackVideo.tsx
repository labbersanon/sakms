// PlaybackVideo attaches native src for Direct Play URLs and hls.js for HLS.
import { type Component, onCleanup } from "solid-js";
import Hls from "hls.js";
import { isHlsSrc } from "../playback";

export function bindMediaSrc(el: HTMLVideoElement, src: string): () => void {
  if (!isHlsSrc(src)) {
    el.src = src;
    return () => {
      el.removeAttribute("src");
    };
  }
  if (Hls.isSupported()) {
    const hls = new Hls({ maxBufferLength: 30 });
    hls.loadSource(src);
    hls.attachMedia(el);
    return () => {
      hls.destroy();
    };
  }
  el.src = src;
  return () => {
    el.removeAttribute("src");
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
