// RssFeedCard — one item from an admin-added raw RSS feed row (NZBGeek
// saved-search style). Text-forward by default (title, pubDate, size, feed
// name) with a single one-click Grab button — mirrors AdultCard's existing
// quick-grab button's request/response/"Grabbed" state handling (shared.tsx's
// GrabDialog.pickManual), but with NO DetailPopup integration: an RSS item is
// already a fully-resolved release (a real downloadUrl+protocol in hand), so
// there's no candidate-selection step to gate behind a click-through popup
// the way TMDB/TPDB catalog items need. Calls manualGrab directly, reusing
// the existing /search/grab endpoint unchanged (see internal/grabs +
// internal/api/search.go's dispatchToDownloadClient — protocol-agnostic, no
// new grab machinery needed for this feature).
//
// An Adult feed item can now additionally carry the identify pipeline's resolved
// poster/title/studio (resolvedImage/resolvedTitle/resolvedStudio, joined onto
// the live feed item server-side — see rss_feeds.go's resolve handler): when
// present the card renders an AdultCard-style poster (proxied image, resolved
// title + studio) instead of the plain text tile. It falls back to the plain
// text-forward form for every Movies/Series item (which never carry resolved
// fields) and for any Adult item that arrives without resolved fields. Note the
// resolve handler now filters unmatched Adult items out server-side, so on the
// normal path an Adult item always arrives resolved — the fallback stays load-
// bearing for Movies/Series always, and for Adult only in the best-effort case
// where a pool-lookup error made the server return raw items un-enriched. The
// Grab always uses the RAW feed title + downloadUrl the release actually needs —
// the resolved fields are display-only.

import { type Component, Show, createSignal } from "solid-js";
import type { RssFeedItem } from "@dto";
import { libraryRootFolder, manualGrab } from "../../api/grab";
import { proxyImage } from "../../api/discover";
import { Button, ErrorText } from "../../components/ui";
import { TextPoster } from "./shared";

// formatSize renders a byte count as a short human-readable size — "" when
// absent (SizeBytes is 0/omitted for a malformed/no-enclosure item).
function formatSize(bytes: number | undefined): string {
  if (!bytes) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export const RssFeedCard: Component<{
  item: RssFeedItem;
  mode: "movies" | "series" | "adult";
}> = (props) => {
  const [grabbing, setGrabbing] = createSignal(false);
  const [grabbed, setGrabbed] = createSignal(false);
  const [error, setError] = createSignal("");

  const meta = () =>
    [props.item.indexer, formatSize(props.item.sizeBytes), props.item.pubDate]
      .filter(Boolean)
      .join(" · ");

  // resolved is true once the server joined this (Adult) feed item onto the
  // identify pipeline's pool — it then carries a poster and/or a cleaned title.
  const resolved = () =>
    Boolean(props.item.resolvedImage || props.item.resolvedTitle);
  const posterSrc = () => proxyImage(props.item.resolvedImage ?? "");
  const displayTitle = () => props.item.resolvedTitle || props.item.title;
  const resolvedMeta = () =>
    [props.item.resolvedStudio, formatSize(props.item.sizeBytes), props.item.pubDate]
      .filter(Boolean)
      .join(" · ");

  const grab = async () => {
    setError("");
    setGrabbing(true);
    try {
      const root = await libraryRootFolder(props.mode);
      if (!root) {
        throw new Error(
          "no root folder configured for this mode — set one in Settings first",
        );
      }
      await manualGrab(props.mode, {
        title: props.item.title,
        indexer: props.item.indexer,
        protocol: props.item.protocol,
        downloadUrl: props.item.downloadUrl,
        rootFolderPath: root,
      });
      setGrabbed(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setGrabbing(false);
    }
  };

  return (
    <div class="w-[220px] shrink-0">
      <Show
        when={resolved()}
        fallback={
          <div class="flex h-24 flex-col justify-between rounded-lg border border-border bg-surface p-2">
            <div class="line-clamp-3 text-sm text-fg" title={props.item.title}>
              {props.item.title}
            </div>
            <div class="truncate text-xs text-muted" title={meta()}>
              {meta() || "—"}
            </div>
          </div>
        }
      >
        <div class="aspect-video overflow-hidden rounded-lg border border-border bg-surface">
          <Show
            when={posterSrc()}
            fallback={<TextPoster label={displayTitle()} />}
          >
            <img
              src={posterSrc()}
              alt={displayTitle()}
              loading="lazy"
              class="h-full w-full object-cover"
            />
          </Show>
        </div>
        <div class="mt-1.5 truncate text-sm text-fg" title={displayTitle()}>
          {displayTitle()}
        </div>
        <div class="truncate text-xs text-muted" title={resolvedMeta()}>
          {resolvedMeta() || "—"}
        </div>
      </Show>
      <div class="mt-1.5">
        <Show
          when={!grabbed()}
          fallback={
            <div class="py-1 text-center text-xs text-ok">Grabbed</div>
          }
        >
          <Button
            class="w-full !py-1 text-xs"
            onClick={() => void grab()}
            disabled={grabbing()}
          >
            {grabbing() ? "Grabbing…" : "Grab"}
          </Button>
        </Show>
      </div>
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
    </div>
  );
};
