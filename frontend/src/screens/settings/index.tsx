// Settings — ported from the vanilla-JS frontend's renderSettings plus the
// Advanced Settings section. SECTION TABS (registered with the app shell via
// ScreenTabs, so the shell draws the bar in its one consistent location; inline
// fallback when rendered standalone in a unit test): Connections (own inline
// Connections/AI sub-tab split, see ConnectionsTab.tsx — AI configuration is
// conceptually a kind of connection setup, folded in here rather than living as
// its own top-level tab; each sub-tab keeps its own independent Save); Library
// (per-mode root folder + quality prefs for all three modes; naming preset and
// kids path for Movies/Series only — Adult has a fixed naming scheme and no
// kids classification); UI (screen-presentation admin controls — today a
// Discover subsection with Mainstream/Adult sub-tabs hosting the custom slider
// and Adult-newest-row editors, see UI.tsx); Auth (Authentication mode + API
// Access break-glass key together); Advanced (per-mode phash-threshold; match-
// confidence-threshold for Movies/Series; identify-enabled for Adult only;
// recheck-interval is global).
//
// There are TWO INDEPENDENT selectors here and they must not be conflated: the
// section-tab selector (SECTION_TABS below), and a Movies/Series/Adult MODE
// selector (ModeSelector) rendered INLINE inside the Library and Advanced tabs
// (the only tabs with per-mode content). The mode selector is a plain
// ScreenTabBar — it is NOT registered with the shell, since the shell's single
// tab slot already holds the section tabs. One shared `mode` signal backs both
// per-mode tabs, so switching between Library and Advanced preserves the
// selected mode.
//
// This screen is split across settings/: shared primitives (Card, SaveStatus,
// useSaveStatus, MODE_LABELS) in shared.tsx; one file per section (Connections/
// Auth/AI/Library/Advanced); ConnectionsTab.tsx and UI.tsx each add an inline
// sub-tab split combining two of those section files under one top-level tab;
// this file is the thin tab shell.

import { type Component, createSignal, Show } from "solid-js";
import type { Mode } from "../../api/discover";
import {
  MODES,
  Muted,
  ScreenTabBar,
  ScreenTabs,
  type TabDef,
} from "../../components/ui";
import { ConnectionsTabSection } from "./ConnectionsTab";
import { APIAccessSection, AuthModeSection } from "./Auth";
import {
  KidsRootPathSection,
  LibraryRootFolderSection,
  NamingPresetSection,
  QualityPrefsSection,
} from "./Library";
import { AdvancedSection } from "./Advanced";
import { SectionSave } from "./shared";
import { UISection } from "./UI";
import { WebhooksSection } from "./Webhooks";
import { NodesSection } from "./Nodes";

// SECTION_TABS is the section-level tab set (distinct from the Movies/Series/
// Adult mode selector). Connections is first so it is the default tab — that
// keeps the safety-critical Connections table (and its three-state secret gate)
// on screen at mount with zero navigation.
const SECTION_TABS: TabDef[] = [
  { id: "connections", label: "Connections" },
  { id: "library", label: "Library" },
  { id: "ui", label: "UI" },
  { id: "auth", label: "Auth" },
  { id: "webhooks", label: "Notifications" },
  { id: "nodes", label: "Nodes" },
  { id: "advanced", label: "Advanced" },
];

// ModeSelector is the inline Movies/Series/Adult tab bar shared by the Library
// and Advanced sections. It is a plain ScreenTabBar (NOT registered with the
// shell) so it never competes with the section tabs for the shell's tab slot.
const ModeSelector: Component<{
  mode: () => Mode;
  onSelect: (m: Mode) => void;
}> = (props) => (
  <ScreenTabBar
    tabs={MODES}
    current={props.mode}
    onSelect={(id) => props.onSelect(id as Mode)}
    class="mb-4 flex gap-1"
  />
);

export const Settings: Component<{ onReboot: () => void }> = (props) => {
  const [section, setSection] = createSignal<string>("connections");
  const [mode, setMode] = createSignal<Mode>("movies");

  return (
    <div>
      <h2 class="mb-4 text-lg font-semibold text-fg">Settings</h2>

      <ScreenTabs tabs={SECTION_TABS} current={section} onSelect={setSection} />

      <Show when={section() === "connections"}>
        <ConnectionsTabSection />
      </Show>

      <Show when={section() === "library"}>
        <ModeSelector mode={mode} onSelect={setMode} />
        {/* One Save button for the active mode's Library panels (root folder +
            quality prefs + naming preset + kids root). Switching mode reseeds
            each panel and clears its dirty flag, so the button reflects only the
            currently-shown mode. */}
        <SectionSave>
          <LibraryRootFolderSection mode={mode} />
          <QualityPrefsSection mode={mode} />
          <Show
            when={mode() !== "adult"}
            fallback={
              <Muted>
                Adult has no naming preferences (it uses a fixed naming scheme)
                and no kids classification. Adult's identify toggle lives in the
                Advanced tab.
              </Muted>
            }
          >
            <NamingPresetSection mode={mode} />
            <KidsRootPathSection mode={mode} />
          </Show>
        </SectionSave>
      </Show>

      <Show when={section() === "ui"}>
        <UISection />
      </Show>

      <Show when={section() === "auth"}>
        <AuthModeSection onReboot={props.onReboot} />
        <APIAccessSection />
      </Show>

      <Show when={section() === "webhooks"}>
        <WebhooksSection />
      </Show>

      <Show when={section() === "nodes"}>
        <NodesSection />
      </Show>

      <Show when={section() === "advanced"}>
        <ModeSelector mode={mode} onSelect={setMode} />
        <AdvancedSection mode={mode} />
      </Show>
    </div>
  );
};
