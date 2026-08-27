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
  },
  { name: "Movies", path: "/media/Movies", isDir: true, tracked: true },
];

function stubBrowse() {
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method || "GET").toUpperCase();
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
  it("lists roots then files after opening a folder", async () => {
    stubBrowse();
    await openMedia();
    expect(screen.getByText("Movies")).toBeInTheDocument();
    expect(screen.getAllByText("Tracked").length).toBeGreaterThan(0);
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
});
