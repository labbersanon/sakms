import { afterEach, describe, expect, it } from "vitest";
import {
  SETTINGS_TAB_KEY,
  isSettingsTabId,
  readStoredSettingsTab,
  sanitizeSettingsTab,
  settingsHref,
  settingsTabLabel,
} from "./settingsTabs";

afterEach(() => localStorage.clear());

describe("settingsTabs", () => {
  it("accepts the finer screen ids and rejects the old in-page ones", () => {
    expect(isSettingsTabId("roots")).toBe(true);
    expect(isSettingsTabId("library")).toBe(false);
    expect(isSettingsTabId("advanced")).toBe(false);
  });

  it("maps leftover ?tab= values onto the finer screens", () => {
    expect(sanitizeSettingsTab("library")).toBe("roots");
    expect(sanitizeSettingsTab("organize")).toBe("scans");
    expect(sanitizeSettingsTab("ui")).toBe("discover");
    expect(sanitizeSettingsTab("webhooks")).toBe("notifications");
    expect(sanitizeSettingsTab("advanced")).toBe("global");
    expect(sanitizeSettingsTab("nope")).toBe("roots");
  });

  it("reads a stored legacy id as the mapped screen", () => {
    localStorage.setItem(SETTINGS_TAB_KEY, "advanced");
    expect(readStoredSettingsTab()).toBe("global");
  });

  it("builds Organize-style hrefs and labels", () => {
    expect(settingsHref("metadata")).toBe("/settings?tab=metadata");
    expect(settingsTabLabel("scans")).toBe("Organize scans");
  });
});
