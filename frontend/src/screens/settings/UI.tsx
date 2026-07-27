// UI — the Settings "UI" tab. Groups controls that shape how the app's own
// screens present themselves. Its one subsection today, "Discover", splits into
// Mainstream and Adult sub-tabs: Mainstream hosts the custom Discover slider
// editor (SliderAdminSection, TMDB-backed), Adult the admin-defined Adult
// "newest" row editor (AdultRowAdminSection, Prowlarr-backed). Both panels are
// relocated here unchanged — this tab only reparents them under one nav home.
//
// The inner Mainstream/Adult switch is a PLAIN ScreenTabBar, NOT ScreenTabs/
// useScreenTabs. The app shell has a single global tab-bar slot, already held by
// Settings' own SECTION_TABS. ScreenTabs registers a tab set with that slot, so
// using it here would overwrite Settings' section tabs the moment this tab
// mounts — a real, visible navigation bug, not a style choice. ScreenTabBar
// renders inline and never touches the shell registration, so it stays scoped
// to this subsection — the same solution ModeSelector uses for the Library/
// Advanced tabs' inner Movies/Series/Adult switch.

import { type Component, createSignal, Match, Show, Switch } from "solid-js";
import { ScreenTabBar, useAdultEnabled, type TabDef } from "../../components/ui";
import { SliderAdminSection } from "../SliderAdmin";
import { AdultRowAdminSection } from "../AdultRowAdmin";
import { RssFeedAdminSection } from "./RssFeedAdmin";

const DISCOVER_TABS: TabDef[] = [
  { id: "mainstream", label: "Mainstream" },
  { id: "adult", label: "Adult" },
];

export const UISection: Component = () => {
  const adultEnabled = useAdultEnabled();
  const [tab, setTab] = createSignal("mainstream");

  return (
    <div>
      <h3 class="mb-3 text-base font-semibold text-fg">Discover</h3>
      {/* Spec-mandated exact behavior (Critic finding, see
          ralplan-adult-disable-switch.md step 8): when Adult mode is
          disabled, do not render ScreenTabBar at all — a filtered-to-one-entry
          bar would be a dangling lone-tab UI. Render SliderAdminSection (the
          Mainstream content) directly instead. */}
      <Show
        when={adultEnabled()}
        fallback={<SliderAdminSection />}
      >
        <ScreenTabBar
          tabs={DISCOVER_TABS}
          current={tab}
          onSelect={setTab}
          class="mb-4 flex gap-1"
        />
        <Switch>
          <Match when={tab() === "mainstream"}>
            <SliderAdminSection />
          </Match>
          <Match when={tab() === "adult"}>
            <AdultRowAdminSection />
            <RssFeedAdminSection />
          </Match>
        </Switch>
      </Show>
    </div>
  );
};
