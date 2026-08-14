// Shared Adult poster-aspect query helper.
// Lives outside organize.ts so Library (tag.ts) can append ?aspect= without
// importing Organize workflow helpers.

export type AdultOrganizeAspect = "" | "vertical" | "horizontal";

export function adultAspectQuery(aspect?: string): string {
  if (aspect === "vertical" || aspect === "horizontal") {
    return `?aspect=${aspect}`;
  }
  return "";
}
