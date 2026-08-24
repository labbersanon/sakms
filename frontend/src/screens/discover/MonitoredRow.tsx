// MonitoredRow — the "Monitored" strip on Adult Discover's browse view.
// Self-hides when the resolved page comes back empty (page 1 returns zero
// items), following the TraktWatchlistRow / RssFeedRow self-guarding pattern:
// the component owns its own empty-state decision so the parent only needs to
// mount it, not conditionally render it.
//
// Unlike RssFeedRow (single fetch, no pagination) this row IS paginated via
// PaginatedStrip — the monitored-entities pool can be arbitrarily large, so
// "Show more" works the same way every other newest-row strip does.
//
// Placed ABOVE all other newest rows on browse so monitored content is always
// the first thing an operator sees, regardless of row-order config.

import { type Component, Show, createResource } from "solid-js";
import { fetchMonitoredRow } from "../../api/adultMonitor";
import { PaginatedStrip } from "./shared";
import { toAdultDiscoverItem } from "./Adult";
import { AdultCard } from "./Adult";
import type { AdultNewestReleaseItem } from "../../api/adultNewestRows";
import type { DetailTarget } from "./DetailPopup";

export const MonitoredRow: Component<{
  reloadToken: () => number;
  onDetail: (t: DetailTarget) => void;
  onError: (err: unknown) => void;
}> = (props) => {
  // Probe page 1 to know whether to render at all. We pass this through to
  // PaginatedStrip as its first-page load; the strip then handles pagination
  // itself. Using createResource here only for the Show guard — the actual
  // data is owned by PaginatedStrip's internal state (via the `load` prop).
  //
  // Claude 2026-08-24: self-hide is a two-gate approach:
  //   1. hasAny tracks whether the initial probe returned >0 items.
  //   2. The Show below suppresses the strip entirely until known non-empty.
  // The probe runs once per reloadToken change (same keying as newestRowsData
  // in Adult.tsx). Swallowing errors here (-> []) matches the pattern every
  // other resource on this page uses — no ErrorBoundary means an unguarded
  // error throws mid-render and crashes the SPA.
  const [probe] = createResource(props.reloadToken, async () => {
    try {
      return await fetchMonitoredRow(1);
    } catch {
      return [] as AdultNewestReleaseItem[];
    }
  });

  const hasAny = () => !probe.loading && (probe() ?? []).length > 0;

  return (
    <Show when={hasAny()}>
      <PaginatedStrip<AdultNewestReleaseItem>
        title="Monitored"
        reloadToken={props.reloadToken}
        load={(page) => fetchMonitoredRow(page)}
        onError={props.onError}
      >
        {(item) => (
          <AdultCard
            item={toAdultDiscoverItem(item)}
            onDetail={props.onDetail}
          />
        )}
      </PaginatedStrip>
    </Show>
  );
};
