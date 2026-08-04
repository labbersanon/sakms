// Settings — ported from the vanilla-JS frontend's renderSettings plus the
// Advanced Settings section. SECTION TABS (registered with the app shell via
// ScreenTabs, so the shell draws the bar in its one consistent location; inline
// fallback when rendered standalone in a unit test): Library (per-mode metadata
// source connections + root folder + quality prefs for all three modes; naming
// preset and kids path for Movies/Series only — Adult has a fixed naming scheme
// and no kids classification; new-season discovery for Series only); Pruning
// (Claude 2026-08-03: operator-authored propose-only Purge rules — CRUD list +
// per-rule age/size/quality-tier conditions with a soft match-count preview,
// plus the Purge background-scan interval control, see PruningRules.tsx); Usenet (the multi-subscription page + the
// auto-grab toggle, its own tab because a subscription's field set is richer
// than a connection row, see Usenet.tsx); Torrent (the torrent engine's
// counterpart to Usenet — engine/performance/seeding/stale-handling settings,
// its own tab for the same reason, see Torrent.tsx); UI (screen-presentation admin
// controls — a Discover subsection with Mainstream/Adult sub-tabs hosting the
// custom slider and Adult-newest-row editors, plus the Trakt watchlist
// connection, see UI.tsx); AI (provider/model config + the AI-provider/Brave
// connection rows); Auth (Authentication mode + API Access
// break-glass key together); Advanced (leads with the mode-INDEPENDENT Global
// settings — API Connections, monitored-title refresh interval + manual
// trigger, Entity Database, Watch Folders, Adult Mode master switch, see
// Global.tsx — rendered ABOVE the mode selector so they read as global, then
// the per-mode fields: phash-threshold; match-confidence-threshold for
// Movies/Series; identify-enabled for Adult only).
//
// There is no Connections tab. It was dismantled: each connection now lives in
// the section it actually belongs to, and the two service classes an operator
// can have more than one of — Usenet subscriptions and media players — moved out
// of the singleton `connections` table into the `service_connections` registry
// (migration 0053) entirely.
//
// There are TWO INDEPENDENT selectors here and they must not be conflated: the
// section-tab selector (SECTION_TABS below), and a Movies/Series/Adult MODE
// selector (ModeSelector) rendered INLINE inside the Library and Advanced tabs
// (the only tabs with per-mode content). The mode selector is a plain
// ScreenTabBar — it is NOT registered with the shell, since the shell's single
// tab slot already holds the section tabs. One shared `mode` signal backs both
// per-mode tabs, so switching between Library and Advanced preserves the
// selected mode. GlobalSection sits ABOVE the Advanced tab's mode selector
// precisely because none of its settings vary by mode — the selector below it
// governs only the AdvancedSection fields, not the global cards above.
//
// This screen is split across settings/: shared primitives (Card, SaveStatus,
// useSaveStatus, MODE_LABELS) in shared.tsx; one file per section (Library/
// Usenet/UI/AI/Auth/Webhooks/Nodes/Advanced — Global is composed into the
// Advanced tab here, it has no tab of its own); UI.tsx additionally composes
// several standalone subsection files (SliderAdmin, AdultRowAdmin,
// RssFeedAdmin, Trakt) under its one top-level tab; this file is the thin tab
// shell.

import { type Component, createEffect, createSignal, Show } from "solid-js";
import type { Mode } from "../../api/discover";
import {
  MODES,
  Muted,
  ScreenTabBar,
  ScreenTabs,
  useAdultEnabled,
  type TabDef,
} from "../../components/ui";
import { AISection } from "./AI";
import { APIAccessSection, AuthModeSection } from "./Auth";
import {
  KidsRootPathSection,
  LibraryConnectionsSection,
  LibraryRootFolderSection,
  NamingPresetSection,
  QualityPrefsSection,
  SeriesNewSeasonDiscoverySection,
} from "./Library";
import { UsenetSection } from "./Usenet";
import { TorrentSection } from "./Torrent";
import { AdvancedSection } from "./Advanced";
import { GlobalSection } from "./Global";
import { SectionSave } from "./shared";
import { UISection } from "./UI";
import { WebhooksSection } from "./Webhooks";
import { NodesSection } from "./Nodes";
import { PruningRulesSection } from "./PruningRules";

// SECTION_TABS is the section-level tab set (distinct from the Movies/Series/
// Adult mode selector). There is no Connections tab: its rows were redistributed
// to the section each one actually belongs to (metadata sources to Library under
// their own mode, Prowlarr/Stash/media players to Advanced -> API Connections,
// Trakt to UI -> Discover), and AI was promoted from a sub-tab to a top-level
// tab of its own. Library leads, so it is the default.
const SECTION_TABS: TabDef[] = [
  { id: "library", label: "Library" },
  { id: "pruning", label: "Pruning" },
  { id: "usenet", label: "Usenet" },
  { id: "torrent", label: "Torrent" },
  { id: "ui", label: "UI" },
  { id: "ai", label: "AI" },
  { id: "auth", label: "Auth" },
  { id: "webhooks", label: "Notifications" },
  { id: "nodes", label: "Nodes" },
  { id: "advanced", label: "Advanced" },
];

// ModeSelector is the inline Movies/Series/Adult tab bar shared by the Library
// and Advanced sections. It is a plain ScreenTabBar (NOT registered with the
// shell) so it never competes with the section tabs for the shell's tab slot.
// Omits "Adult" when the global adult_mode_enabled switch is off, and falls
// back the shared `mode` signal to Movies if it's currently pointed at Adult
// when that happens — same centralized pattern as ModeTabs (ui.tsx), see
// ralplan-adult-disable-switch.md step 5 / Open Question 3.
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

export const Settings: Component<{ onReboot: () => void }> = (props) => {
  const [section, setSection] = createSignal<string>("library");
  const [mode, setMode] = createSignal<Mode>("movies");

  return (
    <div>
      <h2 class="mb-4 text-lg font-semibold text-fg">Settings</h2>

      <ScreenTabs tabs={SECTION_TABS} current={section} onSelect={setSection} />

      <Show when={section() === "library"}>
        <ModeSelector mode={mode} onSelect={setMode} />
        {/* One Save button for the active mode's Library panels (metadata source
            connections + root folder + quality prefs + naming preset + kids
            root). Switching mode reseeds each panel and clears its dirty flag,
            so the button reflects only the currently-shown mode. */}
        <SectionSave>
          <LibraryConnectionsSection mode={mode} />
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
          {/* Series-only: new-season discovery. Not under Usenet — it is a
              Series-library behavior, even though the dispatch it eventually
              feeds is gated by the usenet auto-grab toggle. */}
          <Show when={mode() === "series"}>
            <SeriesNewSeasonDiscoverySection />
          </Show>
        </SectionSave>
      </Show>

      <Show when={section() === "pruning"}>
        <PruningRulesSection />
      </Show>

      <Show when={section() === "usenet"}>
        <UsenetSection />
      </Show>

      <Show when={section() === "torrent"}>
        <TorrentSection />
      </Show>

      <Show when={section() === "ui"}>
        <UISection />
      </Show>

      {/* AI is its own top-level tab now, not a sub-tab of Connections.
          AISection takes no props and provides its own SectionSave, so it is
          already standalone-renderable — promoting it needed no change to it. */}
      <Show when={section() === "ai"}>
        <AISection />
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
        {/* Global (mode-INDEPENDENT) settings render FIRST, above the mode
            selector, so they're visually and structurally separate from — and
            clearly not governed by — the per-mode selector that follows. Their
            standalone Save buttons stay outside AdvancedSection's own
            SectionSave batch since GlobalSection is a sibling, not a child, of
            it. */}
        <GlobalSection />
        <ModeSelector mode={mode} onSelect={setMode} />
        <AdvancedSection mode={mode} />
      </Show>
    </div>
  );
};
