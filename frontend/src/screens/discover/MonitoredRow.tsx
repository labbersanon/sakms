// MonitoredRow — the "Monitored" strip on Adult Discover's browse view.
// Self-hides when the resolved page comes back empty, following the
// TraktWatchlistRow / RssFeedRow self-guarding pattern: the component owns its
// own empty-state decision so the parent only needs to mount it, not
// conditionally render it. Unlike RssFeedRow it IS paginated, because the
// monitored-entities pool can be arbitrarily large.
//
// Placed ABOVE all other newest rows on browse so monitored content is always
// the first thing an operator sees, regardless of row-order config.

import { type Component, Show, createResource } from "solid-js";
import { fetchMonitoredRow } from "../../api/adultMonitor";
import { PaginatedStrip } from "./shared";
import { AdultCard, toAdultDiscoverItem } from "./Adult";
import type { AdultNewestReleaseItem } from "../../api/adultNewestRows";
import type { DetailTarget } from "./DetailPopup";

export const MonitoredRow: Component<{
  reloadToken: () => number;
  onDetail: (t: DetailTarget) => void;
  onError: (err: unknown) => void;
}> = (props) => {
  // Probe page 1 purely to decide whether to render at all — the displayed data
  // is owned by PaginatedStrip's own state via its `load` prop, so this result
  // is only ever read by the Show guard below. Keyed on reloadToken, the same
  // keying newestRowsData uses in Adult.tsx.
  //
  // Claude 2026-08-24: errors are swallowed to [] rather than thrown.
  // Reason: this page has no ErrorBoundary, so an unguarded throw mid-render
  //   takes down the whole SPA. Every other resource here does the same.
  // Review if: Discover gains an ErrorBoundary around its row list.
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
