// BrowserNotifications test — the shell-mounted, no-UI component opens an
// EventSource to /api/notifications/stream only when the shared preference is
// enabled AND Notification permission is granted, fires a native Notification
// (tagged by event type) for each received event, reacts to the shared signal
// flipping without a remount, and closes the stream on unmount. EventSource and
// Notification are mocked globally with controllable stand-ins.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@solidjs/testing-library";
import { BrowserNotifications } from "./BrowserNotifications";
import {
  setBrowserNotificationsEnabled,
} from "../api/webhooks";

// MockEventSource mirrors Dashboard.test.tsx's harness: the most recently
// constructed instance is captured so a test can fire events at it.
class MockEventSource {
  static last: MockEventSource | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  url: string;
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockEventSource.last = this;
  }

  close() {
    this.closed = true;
  }

  // emit fires a data message the way the real SSE onmessage path does — the
  // server sends `data: {"event":...,"data":{...}}`, so the frame's `data`
  // string is the JSON-encoded BroadcastEvent.
  emit(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent);
  }
}

// MockNotification records every constructed notification and exposes a
// settable static `permission` the effect reads.
class MockNotification {
  static permission = "granted";
  static instances: { title: string; options?: NotificationOptions }[] = [];
  title: string;
  options?: NotificationOptions;

  constructor(title: string, options?: NotificationOptions) {
    this.title = title;
    this.options = options;
    MockNotification.instances.push({ title, options });
  }
}

beforeEach(() => {
  MockEventSource.last = null;
  MockNotification.permission = "granted";
  MockNotification.instances = [];
  // Reset the shared module-level signal between tests (it persists across the
  // file, being module state).
  setBrowserNotificationsEnabled(false);
  vi.stubGlobal("EventSource", MockEventSource);
  vi.stubGlobal("Notification", MockNotification);
  // The component seeds the shared signal from the server on mount; a
  // never-resolving fetch keeps that init from clobbering the values a test
  // drives directly.
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise<Response>(() => {})),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("BrowserNotifications", () => {
  it("does not open an EventSource when the preference is off", () => {
    // permission granted, but preference off (default)
    render(() => <BrowserNotifications />);
    expect(MockEventSource.last).toBeNull();
  });

  it("does not open an EventSource when enabled but permission is not granted", () => {
    MockNotification.permission = "default";
    setBrowserNotificationsEnabled(true);
    render(() => <BrowserNotifications />);
    expect(MockEventSource.last).toBeNull();
  });

  it("opens the EventSource when enabled and permission is granted", () => {
    setBrowserNotificationsEnabled(true);
    render(() => <BrowserNotifications />);
    expect(MockEventSource.last).not.toBeNull();
    expect(MockEventSource.last!.url).toBe("/api/notifications/stream");
  });

  it("reacts to the shared signal flipping on/off without a remount", () => {
    // starts off → no stream
    render(() => <BrowserNotifications />);
    expect(MockEventSource.last).toBeNull();

    // flip on → stream opens
    setBrowserNotificationsEnabled(true);
    const es = MockEventSource.last;
    expect(es).not.toBeNull();
    expect(es!.closed).toBe(false);

    // flip off → same stream closes, none reopened
    setBrowserNotificationsEnabled(false);
    expect(es!.closed).toBe(true);
  });

  it("fires a Notification with the event-keyed tag for each event type", () => {
    setBrowserNotificationsEnabled(true);
    render(() => <BrowserNotifications />);
    const es = MockEventSource.last!;

    es.emit({
      event: "rename.applied",
      data: { mode: "movies", workflow: "rename", title: "The Matrix" },
    });
    es.emit({
      event: "purge.applied",
      data: { mode: "series", workflow: "purge", title: "The Wire" },
    });
    es.emit({
      event: "dedup.applied",
      data: { mode: "movies", workflow: "dedup", title: "Inception" },
    });
    // grab.completed carries NO workflow field — copy must not read it.
    es.emit({
      event: "grab.completed",
      data: { mode: "adult", title: "Scene X" },
    });

    const byTag = (tag: string) =>
      MockNotification.instances.find((n) => n.options?.tag === tag);

    const rename = byTag("rename.applied");
    expect(rename?.title).toBe("Rename applied");
    expect(rename?.options?.body).toContain("The Matrix");

    expect(byTag("purge.applied")?.title).toBe("Clean-up applied");
    expect(byTag("dedup.applied")?.title).toBe("Dedup applied");

    const grab = byTag("grab.completed");
    expect(grab?.title).toBe("Grab completed");
    expect(grab?.options?.body).toBe("adult · Scene X");
    // The grab.completed payload has no workflow field — ensure the missing
    // field never leaks into the copy as the string "undefined".
    expect(grab?.options?.body).not.toContain("undefined");

    expect(MockNotification.instances).toHaveLength(4);
  });

  it("ignores an unknown event without firing a Notification", () => {
    setBrowserNotificationsEnabled(true);
    render(() => <BrowserNotifications />);
    MockEventSource.last!.emit({
      event: "something.else",
      data: { title: "nope" },
    });
    expect(MockNotification.instances).toHaveLength(0);
  });

  it("closes the EventSource on unmount", () => {
    setBrowserNotificationsEnabled(true);
    const { unmount } = render(() => <BrowserNotifications />);
    const es = MockEventSource.last!;
    expect(es.closed).toBe(false);
    unmount();
    expect(es.closed).toBe(true);
  });
});
