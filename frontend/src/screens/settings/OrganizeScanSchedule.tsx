// Claude 2026-08-10: new file — the Settings "Organize" tab: one panel each for
// Rename, Dedup and Purge scheduled scanning (plan
// .omc/plans/autopilot-impl-scanschedule-settings-ui.md §5).
// Reason: a new top-level SECTION_TABS entry consolidating all three
// scan-schedule controls in one place, echoing the app's existing Organize
// sidebar grouping (screens/organizeTabs.ts's ORGANIZE_WORKFLOWS). Purge's
// scan-interval Card moved here out of PruningRules.tsx; Rename and Dedup had
// no frontend control at all before this.
// Troubleshooting: none yet.
// Related files: src/api/scanSchedule.ts (all three enabled endpoints + the
// Rename/Dedup interval endpoints), src/api/pruningRules.ts (Purge's interval
// endpoints, deliberately left where they are), src/screens/settings/
// PruningRules.tsx (the Card this tab took over).

import {
  type Component,
  Show,
  createResource,
  createSignal,
} from "solid-js";
import {
  fetchPurgeScanInterval,
  putPurgeScanInterval,
} from "../../api/pruningRules";
import {
  fetchDedupScanEnabled,
  fetchDedupScanInterval,
  fetchPurgeScanEnabled,
  fetchRenameScanEnabled,
  fetchRenameScanInterval,
  putDedupScanEnabled,
  putDedupScanInterval,
  putPurgeScanEnabled,
  putRenameScanEnabled,
  putRenameScanInterval,
} from "../../api/scanSchedule";
import { Card, ErrorText, Muted, Switch } from "../../components/ui";
import { DurationSetting } from "./Advanced";

// ONE toggle + ONE interval per WORKFLOW, covering Movies+Series+Adult
// together — never per mode. That mirrors the backend exactly:
// internal/scanschedule's allModes scans all three modes as a single cycle per
// workflow, and its settings keys (rename_scan_enabled,
// rename_scan_interval_seconds, …) are workflow-scoped, not mode-scoped.
// Building nine controls here would have nothing to bind six of them to.

// RESTART_CAVEAT is stated identically on all three panels because it is
// confirmed scheduler behavior, not a hedge: internal/scanschedule's runLoop
// starts no ticker at all for a workflow that is off at boot (enabled false or
// interval <= 0) and there is no live path that starts a stopped loop, so
// turning a workflow ON always needs a restart. Turning a running one OFF
// (either control) takes effect on its next tick. Do not soften this to "may
// need a restart."
const RESTART_CAVEAT =
  "Turning this ON takes effect on the app's next restart; turning it OFF stops a running schedule on its next tick.";

// ScanSchedulePanel is the shared shape of all three panels — a file-local
// component, not an exported abstraction: the three differ only in their copy
// and their four endpoint functions, and there is no second consumer outside
// this tab. Same register as PruningRules.tsx's own RuleForm.
const ScanSchedulePanel: Component<{
  // title names the WORKFLOW ("Rename"), while intervalLabel names the field
  // ("Rename scan interval") — kept separate so the interval input's
  // accessible name stays exactly what it was in the Pruning tab, and the
  // toggle gets a name of its own rather than "Rename scan interval enabled".
  title: string;
  intervalLabel: string;
  // id is DurationSetting's stable registration key (see its doc comment: two
  // fields sharing a label would otherwise share one registration).
  id: string;
  help: string;
  intervalHelp: string;
  fetchEnabled: () => Promise<boolean>;
  putEnabled: (enabled: boolean) => Promise<void>;
  fetchInterval: () => Promise<number>;
  putInterval: (intervalSeconds: number) => Promise<void>;
}> = (props) => {
  // Enabled and interval are two independent resources against two independent
  // endpoints, deliberately: saving one never writes the other, which is what
  // makes "toggling a workflow off preserves its configured interval" true.
  const [enabled, { mutate: setEnabled }] = createResource(props.fetchEnabled);
  const [interval] = createResource(props.fetchInterval);
  const [toggleError, setToggleError] = createSignal("");

  // The Switch is immediate-apply (its own contract, and the convention for
  // every row-level toggle in this app) — no Save button, unlike the
  // DurationSetting below it. On failure the optimistic flip is rolled back so
  // the control never claims a state the server didn't accept.
  const toggle = async (next: boolean) => {
    const previous = enabled();
    setEnabled(next);
    setToggleError("");
    try {
      await props.putEnabled(next);
    } catch (e) {
      setEnabled(previous);
      setToggleError((e as Error).message);
    }
  };

  return (
    <Card title={props.title}>
      <div class="mb-3 flex items-center gap-2">
        <Switch
          // Fallback TRUE, not false. Usenet's AutoGrabCard falls back to off
          // and calls that "its honest default" — correct there, because
          // usenet_autograb_enabled genuinely defaults off. These three
          // genuinely default ON, so falling back to off would be the
          // dishonest choice here: it would show a stopped schedule while the
          // backend, reading the same unset key, is running one.
          checked={enabled() ?? true}
          disabled={enabled.loading || enabled.error !== undefined}
          onChange={(next) => void toggle(next)}
          ariaLabel={`${props.title} scheduled scanning enabled`}
        />
        <span class="text-sm text-fg">Enabled</span>
      </div>
      <Muted class="mb-3">
        {props.help} A scheduled scan only ever PROPOSES, exactly like a manual
        one — nothing is changed or deleted without an explicit Apply in the
        review queue. On by default at a 24-hour interval. {RESTART_CAVEAT}
      </Muted>
      <Show when={enabled.error}>
        <ErrorText>
          Couldn't load this setting: {(enabled.error as Error)?.message}
        </ErrorText>
      </Show>
      <Show when={toggleError()}>
        <ErrorText>{toggleError()}</ErrorText>
      </Show>
      <DurationSetting
        id={props.id}
        label={props.intervalLabel}
        help={props.intervalHelp}
        value={() => interval()}
        onSave={(v) => props.putInterval(v)}
      />
    </Card>
  );
};

export const OrganizeScanScheduleSection: Component = () => (
  <>
    <ScanSchedulePanel
      title="Rename"
      intervalLabel="Rename scan interval"
      id="rename-scan-interval"
      help="How often Rename automatically re-scans the library for files whose on-disk name doesn't match the active naming preset."
      intervalHelp="How often a scheduled Rename Scan runs in the background."
      fetchEnabled={fetchRenameScanEnabled}
      putEnabled={putRenameScanEnabled}
      fetchInterval={fetchRenameScanInterval}
      putInterval={putRenameScanInterval}
    />
    <ScanSchedulePanel
      title="Dedup"
      intervalLabel="Dedup scan interval"
      id="dedup-scan-interval"
      help="How often Dedup automatically re-scans the library for duplicate copies of the same title, season, episode, or scene."
      intervalHelp="How often a scheduled Dedup Scan runs in the background."
      fetchEnabled={fetchDedupScanEnabled}
      putEnabled={putDedupScanEnabled}
      fetchInterval={fetchDedupScanInterval}
      putInterval={putDedupScanInterval}
    />
    <ScanSchedulePanel
      title="Purge"
      intervalLabel="Purge scan interval"
      id="purge-scan-interval"
      help="How often Purge automatically re-scans for tag- and rule-matched items to propose."
      intervalHelp="How often a scheduled Purge Scan runs in the background."
      fetchEnabled={fetchPurgeScanEnabled}
      putEnabled={putPurgeScanEnabled}
      fetchInterval={fetchPurgeScanInterval}
      putInterval={putPurgeScanInterval}
    />
  </>
);
