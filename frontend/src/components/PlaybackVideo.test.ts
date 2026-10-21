import { afterEach, describe, expect, it, vi } from "vitest";
import { bindMediaSrc } from "./PlaybackVideo";

const loadSource = vi.fn();
const attachMedia = vi.fn();
const destroy = vi.fn();

vi.mock("hls.js", () => ({
  default: class MockHls {
    static isSupported() {
      return true;
    }
    loadSource = loadSource;
    attachMedia = attachMedia;
    destroy = destroy;
  },
}));

afterEach(() => {
  loadSource.mockClear();
  attachMedia.mockClear();
  destroy.mockClear();
});

function video(canPlay: CanPlayTypeResult = ""): HTMLVideoElement {
  const el = document.createElement("video");
  el.canPlayType = () => canPlay;
  return el;
}

describe("bindMediaSrc", () => {
  it("sets native src for Direct Play", () => {
    const el = video();
    const unbind = bindMediaSrc(el, "/api/x/video?fileId=1");
    expect(el.getAttribute("src") || el.src).toContain("/api/x/video?fileId=1");
    expect(loadSource).not.toHaveBeenCalled();
    unbind();
  });

  it("uses native HLS when the element can play it", () => {
    const el = video("probably");
    const unbind = bindMediaSrc(el, "/api/x/video/hls/index.m3u8?fileId=1");
    expect(el.getAttribute("src") || el.src).toContain("hls/index.m3u8");
    expect(loadSource).not.toHaveBeenCalled();
    unbind();
  });

  it("loads hls.js only when native HLS is missing", async () => {
    const el = video("");
    const unbind = bindMediaSrc(el, "/api/x/video/hls/index.m3u8?fileId=1");
    await vi.waitFor(() => {
      expect(loadSource).toHaveBeenCalledWith("/api/x/video/hls/index.m3u8?fileId=1");
    });
    expect(attachMedia).toHaveBeenCalledWith(el);
    unbind();
    expect(destroy).toHaveBeenCalled();
  });
});
