// LibraryRedirect — /library* is retired; send the operator to Discover
// with the In library filter on.
//
// Claude 2026-09-24: Library sidebar page removed in the Discover merge.
// Reason: owned catalog is Discover ?view=library, not a second nav root.
// Troubleshooting: a bookmark to /library/mainstream?tab=series&tier=low
//   must keep tab/tier. Adult path must land on /discover/adult.
// Review if: Discover owned view uses path segments instead of query params.

import { type Component } from "solid-js";
import { Navigate, useLocation } from "@solidjs/router";
import { libraryPathToDiscover } from "./discoverHref";

export const LibraryRedirect: Component = () => {
  const location = useLocation();
  return (
    <Navigate href={libraryPathToDiscover(location.pathname, location.search)} />
  );
};

// DiscoverLibraryRowRedirect — leftover View All route
// /discover/row/library/:mode becomes Discover owned view.
export const DiscoverLibraryRowRedirect: Component = () => {
  const location = useLocation();
  const parts = location.pathname.split("/").filter(Boolean);
  const mode = parts[parts.length - 1];
  const tab = mode === "series" || mode === "movies" ? mode : "";
  return (
    <Navigate
      href={libraryPathToDiscover("/library/mainstream", tab ? `tab=${tab}` : "")}
    />
  );
};
