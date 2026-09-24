// UI — the Settings "UI" tab. Groups controls that shape how the app's own
// screens present themselves. Its one subsection today, "Discover", splits into
// Mainstream and Adult sub-tabs: Mainstream hosts the custom Discover slider
// editor (SliderAdminSection, TMDB-backed), Adult the admin-defined Adult
// "newest" row editor (AdultRowAdminSection, Prowlarr-backed). Both panels are
// relocated here unchanged — this tab only reparents them under one nav home.
//
// The inner Mainstream/Adult switch is a PLAIN ScreenTabBar, NOT ScreenTabs/
// useScreenTabs. Settings no longer registers a shell tab set (sidebar
// children own navigation), but ScreenTabs would still claim the shell slot.
// ScreenTabBar stays inline.

import { type Component, createSignal, Match, Show, Switch } from "solid-js";
import { ScreenTabBar, useAdultEnabled, type TabDef } from "../../components/ui";
import { SliderAdminSection } from "../SliderAdmin";
import { AdultRowAdminSection } from "../AdultRowAdmin";
import { RssFeedAdminSection } from "./RssFeedAdmin";
import { TraktConnectionSection } from "./Trakt";

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

      {/* Trakt drives a Discover row, so it lives with the other Discover
          controls rather than in a connections list. It sits OUTSIDE the
          Mainstream/Adult sub-tab switch because the Watchlist row is
          Mainstream-and-Adult-independent. Unbatched: Trakt renders its own
          Save credentials button. */}
      <TraktConnectionSection />
    </div>
  );
};
