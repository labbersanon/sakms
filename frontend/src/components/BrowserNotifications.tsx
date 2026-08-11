// BrowserNotifications is a no-visible-UI component mounted once in AppShell's
// persistent ShellRoot (never inside a swapped <Route>), so it stays active
// regardless of which screen is displayed. When the shared preference is on AND
// the browser has already granted Notification permission, it opens an
// EventSource to the notifications stream and pops a native desktop Notification
// for each event. Flipping the preference off (or a revoked/never-granted
// permission) closes the stream — the effect reacts to the shared signal, so no
// remount is needed.

import { createEffect, onCleanup, onMount, type Component } from "solid-js";
import {
  browserNotificationsEnabled,
  loadBrowserNotificationsEnabled,
} from "../api/webhooks";

// NotifyPayload is the `data` object carried by each broadcast event. mode and
// title are present on all four events; workflow is present ONLY on the applied
// events (rename/purge/dedup), NOT on grab.completed — reading it there would be
// undefined, so the copy mapping below never touches workflow.
type NotifyPayload = {
  mode?: string;
  workflow?: string;
  title?: string;
};

const EVENT_TITLES: Record<string, string> = {
  "rename.applied": "Rename applied",
  "purge.applied": "Clean-up applied",
  "dedup.applied": "Dedup applied",
  "grab.completed": "Grab completed",
};

// buildNotification maps an event name + payload to a Notification title/body,
// or null for an unknown event. The body is built from mode + title, both of
// which exist on every event; it deliberately does not read `workflow` (absent
// on grab.completed, and redundant with the heading for the applied events).
// No event carries a count field, so batch copy is the last item's title only.
function buildNotification(
  event: string,
  data: NotifyPayload,
): { title: string; body: string } | null {
  const heading = EVENT_TITLES[event];
  if (!heading) return null;
  const body = [data.mode, data.title].filter(Boolean).join(" · ");
  return { title: heading, body };
}

export const BrowserNotifications: Component = () => {
  onMount(() => {
    void loadBrowserNotificationsEnabled();
  });

  let es: EventSource | undefined;

  const closeStream = () => {
    es?.close();
    es = undefined;
  };

  const handleMessage = (ev: MessageEvent) => {
    let frame: { event?: string; data?: NotifyPayload };
    try {
      frame = JSON.parse(ev.data) as { event?: string; data?: NotifyPayload };
    } catch {
      return; // ignore a malformed frame — the next one should be fine
    }
    if (!frame.event) return;
    const n = buildNotification(frame.event, frame.data ?? {});
    if (!n) return;
    // tag keyed on event type collapses same-type bursts (e.g. a bulk Apply)
    // and cross-tab duplicates into a single visible notification.
    new Notification(n.title, { body: n.body, tag: frame.event });
  };

  createEffect(() => {
    const enabled = browserNotificationsEnabled();
    const granted =
      typeof Notification !== "undefined" &&
      Notification.permission === "granted";
    if (enabled && granted) {
      if (!es) {
        es = new EventSource("/api/notifications/stream");
        es.onmessage = handleMessage;
        // onerror: the browser auto-reconnects; events during the gap are lost
        // by design (no replay buffer), so there is nothing to surface here.
        es.onerror = () => {};
      }
    } else {
      closeStream();
    }
  });

  onCleanup(closeStream);

  return null;
};
