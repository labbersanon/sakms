// AddRssFeedModal tests — the "add feed" modal's three-state save behavior.
// A feed URL is now a masked secret (encrypted at rest, always returned as ""),
// so the three-state rule applies: preserve on an untouched update, but CREATE
// has no stored value to preserve, so the add modal must always send the real
// typed URL once. Conventions mirror RssFeedCard.test.tsx (stubFetch/Call).

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { AddRssFeedModal } from "./AddRssFeedModal";
import { jsonResponse } from "../../testing/http";


type Call = { url: string; method: string; body: unknown };

const stubFetch = () => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (url === "/api/discover/rss-feeds" && method === "POST") {
      return jsonResponse({
        id: 1,
        title: "NZBGeek Adult",
        feedUrl: "",
        target: "adult",
        protocol: "usenet",
        sortOrder: 0,
        enabled: true,
        createdAt: "2026-01-01T00:00:00Z",
        updatedAt: "2026-01-01T00:00:00Z",
      });
    }
    throw new Error("unexpected fetch: " + url);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

afterEach(() => vi.unstubAllGlobals());

describe("AddRssFeedModal — three-state create", () => {
  it("sends the real typed feedUrl on create (create never preserves — there is no stored value yet)", async () => {
    const calls = stubFetch();
    let saved = false;
    render(() => (
      <AddRssFeedModal
        allowedTargets={["adult"]}
        defaultTarget="adult"
        onClose={() => {}}
        onSaved={() => {
          saved = true;
        }}
      />
    ));

    fireEvent.input(screen.getByLabelText("Feed title"), {
      target: { value: "NZBGeek Adult" },
    });
    fireEvent.input(screen.getByLabelText("Feed URL"), {
      target: { value: "https://nzbgeek.info/rss?apikey=SECRET" },
    });
    fireEvent.click(screen.getByText("Add feed"));

    await vi.waitFor(() => expect(saved).toBe(true));

    const post = calls.find(
      (c) => c.url === "/api/discover/rss-feeds" && c.method === "POST",
    );
    expect(post?.body).toEqual({
      title: "NZBGeek Adult",
      // The full typed URL is sent verbatim on create — NOT null, NOT "" —
      // because there is no stored secret to preserve on a first-time save.
      feedUrl: "https://nzbgeek.info/rss?apikey=SECRET",
      target: "adult",
      protocol: "usenet",
      enabled: true,
    });
  });

  it("blocks the create and never fires a request when the feed URL is left blank", () => {
    const calls = stubFetch();
    render(() => (
      <AddRssFeedModal
        allowedTargets={["adult"]}
        defaultTarget="adult"
        onClose={() => {}}
        onSaved={() => {}}
      />
    ));

    fireEvent.input(screen.getByLabelText("Feed title"), {
      target: { value: "No URL Feed" },
    });
    fireEvent.click(screen.getByText("Add feed"));

    expect(screen.getByText("Enter a feed URL first.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
});
