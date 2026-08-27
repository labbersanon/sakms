import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Browse } from "./Browse";
import { jsonResponse } from "../testing/http";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const roots = [
  { name: "/media", path: "/media", isDir: true, tracked: true },
  { name: "/downloads", path: "/downloads", isDir: true, tracked: false },
];
const mediaKids = [
  {
    name: "a.mkv",
    path: "/media/a.mkv",
    isDir: false,
    size: 2048,
    tracked: true,
    playable: false,
  },
  {
    name: "clip.mp4",
    path: "/media/clip.mp4",
    isDir: false,
    size: 4096,
    tracked: false,
    playable: true,
  },
  { name: "Movies", path: "/media/Movies", isDir: true, tracked: true, playable: false },
];

function stubBrowse() {
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method || "GET").toUpperCase();
    if (method === "GET" && url.includes("/api/organize/browse/stat")) {
      const path = new URL(url, "http://local").searchParams.get("path") || "";
      const kid = mediaKids.find((e) => e.path === path);
      return jsonResponse({
        name: kid?.name || path.split("/").pop(),
        path,
        isDir: kid?.isDir ?? false,
        size: kid?.size ?? 0,
        tracked: kid?.tracked ?? false,
        playable: kid?.playable ?? false,
        videoUrl: kid?.playable
          ? `/api/organize/browse/video?path=${encodeURIComponent(path)}`
          : "",
        itemCount: kid?.isDir ? 3 : 0,
        totalSize: kid?.isDir ? 999 : 0,
        library: kid?.tracked
          ? [{ kind: "movie", mode: "movies", id: 1, title: "Tracked Title", year: 2020 }]
          : [],
        libraryTotal: kid?.tracked ? 1 : 0,
        probe:
          path.endsWith(".mkv") || path.endsWith(".mp4")
            ? { codec: "h264", width: 1920, height: 1080, duration: 90 }
            : undefined,
      });
    }
    // Must match before /api/organize/browse, whose prefix would
    // otherwise swallow this URL.
    if (method === "GET" && url.includes("/api/organize/browse/tracked")) {
      const path = new URL(url, "http://local").searchParams.get("path") || "";
      const entries = path === "/media" || path.startsWith("/media/") ? mediaKids : roots;
      return jsonResponse({
        path,
        names: entries.filter((e) => e.tracked).map((e) => e.name),
      });
    }
    if (method === "GET" && url.includes("/api/organize/browse")) {
      const path = new URL(url, "http://local").searchParams.get("path") || "";
      const entries = path === "/media" || path.startsWith("/media/") ? mediaKids : roots;
      return jsonResponse({
        path,
        parent: path === "/media" ? "" : path ? "/media" : "",
        entries,
      });
    }
    if (url.includes("/api/organize/events")) return jsonResponse([]);
    if (method === "POST" && url.includes("/rename")) {
      return jsonResponse({
        results: [{ path: "/media/a.mkv", dest: "/media/b.mkv", ok: true }],
      });
    }
    if (method === "POST" && url.includes("/delete")) {
      return jsonResponse({
        results: [{ path: "/media/a.mkv", ok: true }],
      });
    }
    if (method === "POST" && url.includes("/move")) {
      return jsonResponse({
        results: [{ path: "/media/a.mkv", dest: "/media/Movies/a.mkv", ok: true }],
      });
    }
    return jsonResponse([]);
  });
  vi.stubGlobal("fetch", fn);
  return fn;
}

async function openMedia() {
  render(() => <Browse />);
  fireEvent.click(await screen.findByText("/media"));
  await screen.findByText("a.mkv");
}

describe("Browse", () => {
  it("lists Files then entries after opening a folder", async () => {
    stubBrowse();
    render(() => <Browse />);
    expect(await screen.findByRole("button", { name: "Files" })).toBeInTheDocument();
    fireEvent.click(await screen.findByText("/media"));
    await screen.findByText("a.mkv");
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getAllByText("Tracked").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Path")).toHaveClass("bg-bg");
  });

  it("enables rename for one selection and posts after confirm", async () => {
    const fetchFn = stubBrowse();
    await openMedia();

    expect(screen.getByRole("button", { name: "Rename" })).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Select a.mkv"));
    expect(screen.getByRole("button", { name: "Rename" })).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    const input = await screen.findByLabelText("New name");
    fireEvent.input(input, { target: { value: "b.mkv" } });
    const renameButtons = screen.getAllByRole("button", { name: "Rename" });
    fireEvent.click(renameButtons.at(-1)!);

    await waitFor(() => {
      expect(
        fetchFn.mock.calls.some(
          (c) =>
            String(c[0]).includes("/api/organize/browse/rename") &&
            String((c[1] as RequestInit)?.body).includes("b.mkv"),
        ),
      ).toBe(true);
    });
  });

  it("delete confirm lists tracked paths then posts", async () => {
    const fetchFn = stubBrowse();
    await openMedia();
    fireEvent.click(screen.getByLabelText("Select a.mkv"));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(
      await screen.findByText(/Tracked library titles will be updated/),
    ).toBeInTheDocument();
    const deleteButtons = screen.getAllByRole("button", { name: "Delete" });
    fireEvent.click(deleteButtons.at(-1)!);
    await waitFor(() => {
      expect(
        fetchFn.mock.calls.some((c) =>
          String(c[0]).includes("/api/organize/browse/delete"),
        ),
      ).toBe(true);
    });
  });

  it("right-click shows Copy path and Play/preview only for playable files", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(window.navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    stubBrowse();
    await openMedia();

    fireEvent.contextMenu(screen.getByText("a.mkv"));
    expect(await screen.findByRole("menuitem", { name: "Copy path" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Rename" })).toBeEnabled();
    expect(screen.queryByRole("menuitem", { name: "Play/preview" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "Copy path" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("/media/a.mkv"));
    expect(await screen.findByText("Path copied")).toBeInTheDocument();

    fireEvent.contextMenu(screen.getByText("clip.mp4"));
    expect(await screen.findByRole("menuitem", { name: "Play/preview" })).toBeEnabled();
  });

  it("properties pane updates on row click and is gated on a single selection", async () => {
    stubBrowse();
    await openMedia();

    fireEvent.click(screen.getByText("a.mkv"));
    expect(await screen.findByText("/media/a.mkv")).toBeInTheDocument();
    expect(screen.getByText(/Tracked Title/)).toBeInTheDocument();
    expect(screen.getByText(/1920×1080/)).toBeInTheDocument();

    fireEvent.click(screen.getByText("clip.mp4"));
    await waitFor(() => {
      expect(screen.getByText("/media/clip.mp4")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByLabelText("Select a.mkv"));
    fireEvent.contextMenu(screen.getByText("a.mkv"));
    expect(await screen.findByRole("menuitem", { name: "Properties" })).toBeDisabled();
    expect(screen.getByRole("menuitem", { name: "Move" })).toBeEnabled();
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeEnabled();
  });
});
