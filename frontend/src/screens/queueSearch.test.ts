import { describe, expect, it } from "vitest";
import { matchesQueueSearch } from "./queueSearch";

describe("matchesQueueSearch", () => {
  it("matches everything when the query is empty or whitespace", () => {
    expect(matchesQueueSearch("", "Anything")).toBe(true);
    expect(matchesQueueSearch("   ", "Anything")).toBe(true);
  });

  it("matches case-insensitively across any part", () => {
    expect(matchesQueueSearch("MOV", "My Movie", "Pending")).toBe(true);
    expect(matchesQueueSearch("pend", "My Movie", "Pending")).toBe(true);
    expect(matchesQueueSearch("xyz", "My Movie", "Pending")).toBe(false);
  });

  it("treats nullish parts as empty strings", () => {
    expect(matchesQueueSearch("hi", null, undefined, "hi there")).toBe(true);
    expect(matchesQueueSearch("hi", null, undefined)).toBe(false);
  });
});
