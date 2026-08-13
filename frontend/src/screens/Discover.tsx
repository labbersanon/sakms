// Discover moved into ./discover/ (split by tab: Mainstream / Adult, plus the
// shared grab pipeline and pagination engine). This thin re-export keeps the
// public import path stable — `import { Discover } from "./Discover"` (and
// "./screens/Discover" from outside) still resolves — so nothing else in the
// codebase or the test suite has to change its imports.
export { Discover, DiscoverAdult, DiscoverMainstream } from "./discover";
