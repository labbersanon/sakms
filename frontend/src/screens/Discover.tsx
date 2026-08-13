// Discover moved into ./discover/ (split by tab: Mainstream / Adult, plus the
// shared grab pipeline and pagination engine). This thin re-export keeps the
// public import path stable for DiscoverAdult / DiscoverMainstream.
//
// Claude 2026-08-13: Discover (combined Mainstream/Adult ModeTabs shell) is no
// longer exported. Reason: Slice 1.5 — unrouted; tests mount the routed shells.
// Review if: a combined deep-link shell is restored.
export { DiscoverAdult, DiscoverMainstream } from "./discover";
