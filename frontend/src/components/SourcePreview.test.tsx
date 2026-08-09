// SourcePreview tests — component-level coverage per
// .omc/plans/autopilot-impl-rename-preview.md §8.6.
//
// #14: SourcePreviewVideo's attribute conventions, asserted directly here
// rather than through a screen (preload="none", controls, NOT muted, no
// autoplay, "Preview of <label>" aria-label).
// #15: SourcePreviewPopout and SourcePreviewDisclosure each label their
// trigger distinctly from the video they mount, and mounting/unmounting the
// video is exercised end to end (open reveals it, close/collapse removes
// it) — this is what keeps a shared-label mistake from silently passing.

import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import {
  SourcePreviewDisclosure,
  SourcePreviewPopout,
  SourcePreviewVideo,
} from "./SourcePreview";

describe("SourcePreviewVideo", () => {
  it("renders the video conventions: preload=none, controls, NOT muted, no autoplay", () => {
    render(() => (
      <SourcePreviewVideo src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    const video = screen.getByLabelText(
      "Preview of movie.mkv",
    ) as HTMLVideoElement;
    expect(video).toBeInTheDocument();
    expect(video.tagName).toBe("VIDEO");
    expect(video.getAttribute("preload")).toBe("none");
    expect(video.hasAttribute("controls")).toBe(true);
    // The deliberate divergence from Dedup's tile: unmuted by default.
    expect(video.hasAttribute("muted")).toBe(false);
    expect(video.muted).toBe(false);
    expect(video.hasAttribute("autoplay")).toBe(false);
    expect(video.getAttribute("src")).toBe(
      "/api/modes/movies/proposals/1/video",
    );
  });
});

describe("SourcePreviewPopout", () => {
  it("renders a trigger control and no video until opened", () => {
    render(() => (
      <SourcePreviewPopout src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    expect(
      screen.getByLabelText("Open preview for movie.mkv"),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Preview of movie.mkv")).toBeNull();
  });

  it("clicking the trigger opens the modal with the video, distinctly labeled", () => {
    render(() => (
      <SourcePreviewPopout src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    fireEvent.click(screen.getByLabelText("Open preview for movie.mkv"));
    const video = screen.getByLabelText(
      "Preview of movie.mkv",
    ) as HTMLVideoElement;
    expect(video).toBeInTheDocument();
    expect(video.getAttribute("preload")).toBe("none");
    expect(video.hasAttribute("muted")).toBe(false);
    // Both labels coexist without a multiple-match error — the whole point
    // of the split-label design.
    expect(
      screen.getByLabelText("Open preview for movie.mkv"),
    ).toBeInTheDocument();
  });

  it("closing the modal unmounts the video (preload=none stops playback)", () => {
    render(() => (
      <SourcePreviewPopout src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    fireEvent.click(screen.getByLabelText("Open preview for movie.mkv"));
    expect(screen.getByLabelText("Preview of movie.mkv")).toBeInTheDocument();
    // Modal's Close button; the trigger button remains, distinctly labeled.
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByLabelText("Preview of movie.mkv")).toBeNull();
    expect(
      screen.getByLabelText("Open preview for movie.mkv"),
    ).toBeInTheDocument();
  });
});

describe("SourcePreviewDisclosure", () => {
  it("renders collapsed by default: a toggle, and no video", () => {
    render(() => (
      <SourcePreviewDisclosure src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    expect(
      screen.getByRole("button", { name: "Preview source file" }),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Preview of movie.mkv")).toBeNull();
  });

  it("clicking the toggle expands the video inline, distinctly labeled", () => {
    render(() => (
      <SourcePreviewDisclosure src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    fireEvent.click(
      screen.getByRole("button", { name: "Preview source file" }),
    );
    const video = screen.getByLabelText(
      "Preview of movie.mkv",
    ) as HTMLVideoElement;
    expect(video).toBeInTheDocument();
    expect(video.hasAttribute("muted")).toBe(false);
    expect(
      screen.getByRole("button", { name: "Hide preview" }),
    ).toBeInTheDocument();
  });

  it("clicking again collapses the video (Hide preview -> Preview source file)", () => {
    render(() => (
      <SourcePreviewDisclosure src="/api/modes/movies/proposals/1/video" label="movie.mkv" />
    ));
    fireEvent.click(
      screen.getByRole("button", { name: "Preview source file" }),
    );
    expect(screen.getByLabelText("Preview of movie.mkv")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Hide preview" }));
    expect(screen.queryByLabelText("Preview of movie.mkv")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Preview source file" }),
    ).toBeInTheDocument();
  });
});
