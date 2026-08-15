// TrackedPlayback tests — in-app Library movie player.
//
// Load-bearing: Play and the <video> use different aria-labels so
// getByLabelText cannot silently match the still-present button after Hide.
// Fullscreen is an explicit button (native controls also include it) that
// calls requestFullscreen on the video element. The player is inline — never
// a dialog — because Library detail is already a Modal.

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { TrackedPlayback, PlayFullscreenLink, enterVideoFullscreen } from "./TrackedPlayback";

describe("TrackedPlayback", () => {
  it("renders Play and Fullscreen with no video until Play is clicked", () => {
    render(() => (
      <TrackedPlayback
        src="/api/modes/movies/tracked/10/video"
        label="Inception"
      />
    ));
    expect(screen.getByRole("button", { name: "Play Inception" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Play Inception fullscreen" }),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Video player for Inception")).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Play mounts an unmuted, no-autoplay video with native controls", () => {
    render(() => (
      <TrackedPlayback
        src="/api/modes/movies/tracked/10/video?fileId=3"
        label="Inception"
      />
    ));
    fireEvent.click(screen.getByRole("button", { name: "Play Inception" }));
    const video = screen.getByLabelText(
      "Video player for Inception",
    ) as HTMLVideoElement;
    expect(video.tagName).toBe("VIDEO");
    expect(video.getAttribute("preload")).toBe("none");
    expect(video.hasAttribute("controls")).toBe(true);
    expect(video.hasAttribute("muted")).toBe(false);
    expect(video.muted).toBe(false);
    expect(video.hasAttribute("autoplay")).toBe(false);
    expect(video.getAttribute("src")).toBe(
      "/api/modes/movies/tracked/10/video?fileId=3",
    );
    expect(screen.getByRole("button", { name: "Hide player for Inception" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Hide player unmounts the video", () => {
    render(() => (
      <TrackedPlayback
        src="/api/modes/movies/tracked/10/video"
        label="Inception"
      />
    ));
    fireEvent.click(screen.getByRole("button", { name: "Play Inception" }));
    expect(screen.getByLabelText("Video player for Inception")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Hide player for Inception" }));
    expect(screen.queryByLabelText("Video player for Inception")).toBeNull();
    expect(screen.getByRole("button", { name: "Play Inception" })).toBeInTheDocument();
  });

  it("Fullscreen calls requestFullscreen on the mounted video", async () => {
    const requestFullscreen = vi.fn().mockResolvedValue(undefined);
    render(() => (
      <TrackedPlayback
        src="/api/modes/movies/tracked/10/video"
        label="Inception"
      />
    ));
    fireEvent.click(screen.getByRole("button", { name: "Play Inception" }));
    const video = screen.getByLabelText(
      "Video player for Inception",
    ) as HTMLVideoElement;
    video.requestFullscreen = requestFullscreen;
    fireEvent.click(
      screen.getByRole("button", { name: "Play Inception fullscreen" }),
    );
    expect(requestFullscreen).toHaveBeenCalledTimes(1);
  });
});

describe("enterVideoFullscreen", () => {
  it("prefers requestFullscreen, then webkitEnterFullscreen", async () => {
    const requestFullscreen = vi.fn().mockResolvedValue(undefined);
    const webkitEnterFullscreen = vi.fn();
    const el = {
      requestFullscreen,
      webkitEnterFullscreen,
    } as unknown as HTMLVideoElement;
    await enterVideoFullscreen(el);
    expect(requestFullscreen).toHaveBeenCalledTimes(1);
    expect(webkitEnterFullscreen).not.toHaveBeenCalled();

    const ios = {
      requestFullscreen: vi.fn().mockRejectedValue(new Error("ios")),
      webkitEnterFullscreen,
    } as unknown as HTMLVideoElement;
    await enterVideoFullscreen(ios);
    expect(webkitEnterFullscreen).toHaveBeenCalledTimes(1);
  });
});

describe("PlayFullscreenLink", () => {
  it("renders Play Movie with no video until clicked", () => {
    render(() => (
      <PlayFullscreenLink
        src="/api/modes/movies/tracked/10/video?fileId=3"
        noun="Movie"
        title="Inception"
      />
    ));
    expect(
      screen.getByRole("button", { name: "Play Movie fullscreen" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Play Movie →")).toBeInTheDocument();
    expect(screen.queryByLabelText("Fullscreen player for Inception")).toBeNull();
  });

  it("click mounts the video, plays, and requests fullscreen", async () => {
    const requestFullscreen = vi.fn().mockResolvedValue(undefined);
    const play = vi.fn().mockResolvedValue(undefined);
    const proto = HTMLVideoElement.prototype as HTMLVideoElement & {
      play: () => Promise<void>;
    };
    const origFs = proto.requestFullscreen;
    const origPlay = proto.play;
    proto.requestFullscreen = requestFullscreen;
    proto.play = play;
    try {
      render(() => (
        <PlayFullscreenLink
          src="/api/modes/movies/tracked/10/video?fileId=3"
          noun="Movie"
          title="Inception"
        />
      ));
      fireEvent.click(
        screen.getByRole("button", { name: "Play Movie fullscreen" }),
      );
      const video = screen.getByLabelText(
        "Fullscreen player for Inception",
      ) as HTMLVideoElement;
      expect(video.getAttribute("src")).toBe(
        "/api/modes/movies/tracked/10/video?fileId=3",
      );
      expect(video.hasAttribute("autoplay")).toBe(true);
      expect(screen.queryByRole("dialog")).toBeNull();
      await vi.waitFor(() => {
        expect(requestFullscreen).toHaveBeenCalledTimes(1);
        expect(play).toHaveBeenCalled();
      });
    } finally {
      proto.requestFullscreen = origFs;
      proto.play = origPlay;
    }
  });
});
