// Download — the Settings "Download" tab. Pure re-parenting: it holds the two
// pre-existing native download panels, Usenet (UsenetSection, NNTP
// multi-subscription CRUD + the auto-grab toggle) and Torrent (TorrentSection,
// the torrent engine's engine/performance/seeding/stale-handling settings), as
// two sub-tabs instead of two separate top-level Settings tabs. Usenet is first
// and default, matching the old top-level order. Neither panel's own behavior,
// fields, validation, or save mechanics change — this tab only gives them one
// nav home.
//
// The inner Usenet/Torrent switch is a PLAIN ScreenTabBar, NOT ScreenTabs/
// useScreenTabs — the same rule, for the same reason, as UI.tsx's Mainstream/
// Adult switch. The app shell has a single global tab-bar slot, already held by
// Settings' own SECTION_TABS; ScreenTabs registers a tab set with that slot, so
// using it here would overwrite Settings' section tabs the moment this tab
// mounts. ScreenTabBar renders inline and never touches the shell registration.
// Settings.test.tsx's shell-slot suite has a dedicated regression case for this.
//
// Two things this file must NOT grow:
//   - A SectionSave wrapper. UsenetSection and TorrentSection each provide
//     their OWN. Wrapping them here would nest a second SectionSaveContext, and
//     useSectionSaveItem resolves to the nearest provider — silently changing
//     which button drives which row.
//   - A keep-both-mounted variant (a CSS-hidden <Show> instead of <Switch>).
//     SectionSave renders its "Save" button unconditionally, so mounting both
//     panels at once would put TWO buttons named "Save" on this tab, which
//     Settings.test.tsx's unscoped clickSectionSave helper throws on.

import { type Component, createSignal, Match, Switch } from "solid-js";
import { ScreenTabBar, type TabDef } from "../../components/ui";
import { UsenetSection } from "./Usenet";
import { TorrentSection } from "./Torrent";

const DOWNLOAD_TABS: TabDef[] = [
  { id: "usenet", label: "Usenet" },
  { id: "torrent", label: "Torrent" },
];

export const DownloadSection: Component = () => {
  const [tab, setTab] = createSignal("usenet");

  return (
    <div>
      <ScreenTabBar
        tabs={DOWNLOAD_TABS}
        current={tab}
        onSelect={setTab}
        class="mb-4 flex gap-1"
      />
      <Switch>
        <Match when={tab() === "usenet"}>
          <UsenetSection />
        </Match>
        <Match when={tab() === "torrent"}>
          <TorrentSection />
        </Match>
      </Switch>
    </div>
  );
};
