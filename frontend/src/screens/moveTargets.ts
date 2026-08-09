import type { Mode } from "../api/discover";
const MOVE_TARGETS: Mode[] = ["movies", "series", "adult"];
export const otherModes = (current: Mode): Mode[] =>
  MOVE_TARGETS.filter((m) => m !== current);
