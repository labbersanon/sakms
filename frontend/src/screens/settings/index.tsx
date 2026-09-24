// Settings — sidebar-driven screens via ?tab= (same pattern as Organize/Queue).
// Each child is its own screen. Per-mode screens (Roots / Metadata / Quality)
// share one Movies/Series/Adult pill bar. Cards save themselves; there is no
// tab-level batched Save.
//
// Claude 2026-09-24: Settings dropped the 9-tab in-page bar for sidebar children.
// Reason: Library/Discover/Organize/Queue already navigate that way; one long
//   Settings page was the outlier. Finer screens also split Library + Advanced.
// Troubleshooting: missing/invalid ?tab= → last-used or roots via replace navigate.
// Review if: nested paths replace query params.
//
// Workflow switching lives in the sidebar. This file reads `?tab=` and renders
// the matching cards. The active tab is mirrored to localStorage
// (`sakms.settings.tab`).
//
// The Movies/Series/Adult pills, Download's Usenet/Torrent switch, and Discover
// UI's Mainstream/Adult switch are plain ScreenTabBar (or ModeSelector). Left
// alone, a ScreenTabs registration would claim the shell's single tab slot.
// Children are wrapped in a shadowing ScreenTabsContext.Provider
// (value={undefined}) so those bars stay inline, matching Organize.tsx.
//
// One shared `mode` signal backs Roots / Metadata / Quality so switching
// screens keeps the selected mode.

import { type Component, createEffect, createSignal, Show } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import type { Mode } from "../../api/discover";
import {
  MODES,
  Muted,
  ScreenTabBar,
  ScreenTabsContext,
  useAdultEnabled,
} from "../../components/ui";
import { AISection } from "./AI";
import { APIAccessSection, AuthModeSection } from "./Auth";
import {
  KidsRootPathSection,
  LibraryConnectionsSection,
  LibraryRootFolderSection,
  NamingPresetSection,
  ProposeNestedMovesSection,
  QualityPrefsSection,
  SeriesNewSeasonDiscoverySection,
} from "./Library";
import { DownloadSection } from "./Download";
import { AdvancedSection } from "./Advanced";
import { ConnectionsSection, GlobalSection } from "./Global";
import { UISection } from "./UI";
import { WebhooksSection } from "./Webhooks";
import { NodesSection } from "./Nodes";
import { OrganizeScanScheduleSection } from "./OrganizeScanSchedule";
import { StashBoxDatabases } from "./StashBoxDatabases";
import {
  type SettingsTabId,
  isSettingsTabId,
  sanitizeSettingsTab,
  settingsTabLabel,
  writeStoredSettingsTab,
} from "../settingsTabs";

// ModeSelector is the inline Movies/Series/Adult tab bar shared by Roots,
// Metadata, and Quality. It is a plain ScreenTabBar (NOT registered with the
// shell) so it never claims the shell's tab slot. Omits "Adult" when the
// global adult_mode_enabled switch is off, and falls the shared `mode` signal
// back to Movies if it's currently pointed at Adult when that happens — same
// centralized pattern as ModeTabs (ui.tsx).
const ModeSelector: Component<{
  mode: () => Mode;
  onSelect: (m: Mode) => void;
}> = (props) => {
  const adultEnabled = useAdultEnabled();
  // Kept as a FUNCTION, called inline in the JSX below (never hoisted to a
  // plain variable) — see ModeTabs' matching doc comment in ui.tsx for why:
  // Solid compiles JSX prop expressions into getters, so this stays reactive
  // to adultEnabled() resolving after mount (e.g. still loading at first
  // paint) instead of freezing at whatever it read during this one synchronous
  // render pass.
  const tabs = () => (adultEnabled() ? MODES : MODES.filter((m) => m.id !== "adult"));

  createEffect(() => {
    if (!adultEnabled() && props.mode() === "adult") {
      props.onSelect("movies");
    }
  });

  return (
    <ScreenTabBar
      tabs={tabs()}
      current={props.mode}
      onSelect={(id) => props.onSelect(id as Mode)}
      class="mb-4 flex gap-1"
    />
  );
};

const PER_MODE_TABS: readonly SettingsTabId[] = ["roots", "metadata", "quality"];

export const Settings: Component<{ onReboot: () => void }> = (props) => {
  const [params, setParams] = useSearchParams();
  const [mode, setMode] = createSignal<Mode>("movies");

  const rawTab = () => {
    const t = params.tab;
    return typeof t === "string" ? t : Array.isArray(t) ? t[0] : undefined;
  };
  const tab = (): SettingsTabId => sanitizeSettingsTab(rawTab());

  createEffect(() => {
    const raw = rawTab();
    const next = isSettingsTabId(raw) ? raw : sanitizeSettingsTab(raw);
    if (raw !== next) {
      setParams({ tab: next }, { replace: true });
    }
    writeStoredSettingsTab(next);
  });

  return (
    <div>
      <h2 class="mb-4 text-lg font-semibold text-fg">{settingsTabLabel(tab())}</h2>

      <ScreenTabsContext.Provider value={undefined}>
        <Show when={PER_MODE_TABS.includes(tab())}>
          <ModeSelector mode={mode} onSelect={setMode} />
        </Show>

        <Show when={tab() === "roots"}>
          <LibraryRootFolderSection mode={mode} />
          <Show
            when={mode() !== "adult"}
            fallback={
              <Muted>
                Adult has no naming preferences (it uses a fixed naming scheme)
                and no kids classification. Adult's identify toggle lives on the
                Metadata screen.
              </Muted>
            }
          >
            <NamingPresetSection mode={mode} />
            <KidsRootPathSection mode={mode} />
          </Show>
        </Show>

        <Show when={tab() === "metadata"}>
          <LibraryConnectionsSection mode={mode} />
          {/* Claude 2026-08-04: mounted StashBoxDatabases here (Stage 5 Wave
              4.5, plan .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md
              §4.5). Reason: it stays next to Adult metadata connections.
              StashBoxDatabases never calls useSectionSaveItem — every one of
              its mutations persists immediately on its own, exactly like
              RssFeedAdmin.
              Review if: StashBoxDatabases starts calling useSectionSaveItem. */}
          <Show when={mode() === "adult"}>
            <StashBoxDatabases />
          </Show>
          <AdvancedSection mode={mode} />
        </Show>

        <Show when={tab() === "quality"}>
          <QualityPrefsSection mode={mode} />
          <Show when={mode() === "series"}>
            <SeriesNewSeasonDiscoverySection />
          </Show>
        </Show>

        <Show when={tab() === "scans"}>
          <OrganizeScanScheduleSection />
          <ProposeNestedMovesSection />
        </Show>

        <Show when={tab() === "download"}>
          <DownloadSection />
        </Show>

        <Show when={tab() === "discover"}>
          <UISection />
        </Show>

        <Show when={tab() === "ai"}>
          <AISection />
        </Show>

        <Show when={tab() === "auth"}>
          <AuthModeSection onReboot={props.onReboot} />
          <APIAccessSection />
        </Show>

        <Show when={tab() === "notifications"}>
          <WebhooksSection />
        </Show>

        <Show when={tab() === "nodes"}>
          <NodesSection />
        </Show>

        <Show when={tab() === "connections"}>
          <ConnectionsSection />
        </Show>

        <Show when={tab() === "global"}>
          <GlobalSection />
        </Show>
      </ScreenTabsContext.Provider>
    </div>
  );
};
