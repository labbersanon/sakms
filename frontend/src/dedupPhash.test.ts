import { describe, expect, it } from "vitest";
import { phashMatches } from "./dedupPhash";

const matching = "pdq256/5f:" + "0".repeat(320);
const far = "pdq256/5f:" + "f".repeat(320);

describe("phashMatches", () => {
  it("matches identical hashes", () => {
    expect(phashMatches(matching, matching, "movies")).toBe(true);
    expect(phashMatches(matching, matching, "series")).toBe(true);
  });

  it("rejects far hashes (Last Crusade crop vs open-matte)", () => {
    expect(phashMatches(matching, far, "movies")).toBe(false);
  });

  it("rejects missing hashes", () => {
    expect(phashMatches(undefined, matching, "movies")).toBe(false);
    expect(phashMatches(matching, "", "movies")).toBe(false);
  });
});
