import { describe, expect, it } from "vitest";
import { inAppVideoSrc, isDirectPlayContainer, isHlsSrc, toHlsPlaylist } from "./playback";

describe("playback urls", () => {
  it("treats mp4/m4v/webm/mov as Direct Play", () => {
    expect(isDirectPlayContainer("/m/a.mp4")).toBe(true);
    expect(isDirectPlayContainer("/m/a.mkv")).toBe(false);
    expect(isDirectPlayContainer("/m/a.avi")).toBe(false);
  });

  it("inserts /hls/index.m3u8 before the query", () => {
    expect(toHlsPlaylist("/api/modes/movies/tracked/10/video?fileId=3")).toBe(
      "/api/modes/movies/tracked/10/video/hls/index.m3u8?fileId=3",
    );
    expect(
      toHlsPlaylist("/api/organize/browse/video?path=%2Fmedia%2Fa.mkv"),
    ).toBe("/api/organize/browse/video/hls/index.m3u8?path=%2Fmedia%2Fa.mkv");
  });

  it("keeps Direct Play src and HLS-wraps mkv", () => {
    const base = "/api/modes/movies/tracked/10/video";
    expect(inAppVideoSrc(base, "/m/a.mp4")).toBe(base);
    expect(inAppVideoSrc(base, "/m/a.mkv")).toBe(
      "/api/modes/movies/tracked/10/video/hls/index.m3u8",
    );
  });

  it("detects HLS playlist srcs", () => {
    expect(isHlsSrc("/api/x/video/hls/index.m3u8?fileId=1")).toBe(true);
    expect(isHlsSrc("/api/x/video?fileId=1")).toBe(false);
  });
});
