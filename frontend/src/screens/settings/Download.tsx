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
// useScreenTabs. Settings no longer registers a shell tab set (sidebar
// children own navigation), but ScreenTabs would still claim the shell slot
// if this switch used it. ScreenTabBar stays inline.
//
// Two things this file must NOT grow:
//   - A SectionSave wrapper. Each Usenet/Torrent card saves itself.
//   - A keep-both-mounted variant (a CSS-hidden <Show> instead of <Switch>).

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
