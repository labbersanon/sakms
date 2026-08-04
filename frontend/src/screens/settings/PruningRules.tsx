// Claude 2026-08-03: new file — the Settings "Pruning" tab (F1, plan
// .omc/plans/autopilot-impl-pruning-rules.md §6.2 + §6.3 + §13.1).
// Reason: a new top-level SECTION_TABS entry per §6.1 (rejected placements:
// Library's SectionSave batch-save context doesn't fit a per-row CRUD list;
// the UI tab is for Discover presentation, not a Purge safety control).
// Follows SliderAdmin's inline-form create/edit pattern (one form, seeded
// from an optional `rule` prop) rather than RowEditor — rules have no
// meaningful display order, so drag-and-drop reorder does not apply here.
// Troubleshooting: none yet.

import {
  type Component,
  For,
  Show,
  createEffect,
  createResource,
  createSignal,
  on,
  onCleanup,
} from "solid-js";
import Pencil from "lucide-solid/icons/pencil";
import type { Mode } from "../../api/discover";
import {
  GB_BYTES,
  PRUNING_TIER_FLOORS,
  PRUNING_TIER_FLOOR_LABELS,
  createPruningRule,
  deletePruningRule,
  fetchPruningRules,
  fetchPurgeScanInterval,
  previewPruningRule,
  putPurgeScanInterval,
  updatePruningRule,
  type PruningRule,
  type PruningRuleUpsertRequest,
  type PruningTierFloor,
} from "../../api/pruningRules";
import {
  Button,
  Card,
  ErrorText,
  Muted,
  MODES,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../../components/ui";
import { MODE_LABELS } from "./shared";
import { DurationSetting } from "./Advanced";

// PREVIEW_DEBOUNCE_MS mirrors FolderPicker's as-you-type debounce
// (components/FolderPicker.tsx) — a keystroke in an age/size field, or a
// condition/tier toggle, doesn't fire a preview request until it settles.
const PREVIEW_DEBOUNCE_MS = 300;

// gbToBytes/bytesToGB are the size condition's GB<->bytes conversion, 1024
// -based to match internal/pruning's humanBytes convention (the DTO's
// sizeBytes is always whole bytes; the form only ever shows GB).
export function gbToBytes(gb: number): number {
  return Math.round(gb * GB_BYTES);
}
export function bytesToGB(bytes: number): number {
  return Math.round((bytes / GB_BYTES) * 100) / 100;
}

// summarizeConditions renders a rule's configured conditions for the list
// row — the client-side mirror of the (actual-value) Reason text the backend
// generates at match time (plan §5), but here it echoes the rule's
// THRESHOLDS, not an item's triggering values, since no item is in scope.
export function summarizeConditions(rule: PruningRule): string {
  const parts: string[] = [];
  if (rule.ageDays) parts.push(`${rule.ageDays}+ days old`);
  if (rule.sizeBytes) parts.push(`${bytesToGB(rule.sizeBytes)}+ GB`);
  if (rule.qualityTierFloor) parts.push(`tier \u2264 ${rule.qualityTierFloor}`);
  return parts.length ? parts.join(", ") : "no conditions";
}

// RuleForm creates a new rule (rule prop undefined) or edits an existing one
// (rule prop present) — the same form either way, following SliderForm's
// seed-from-props-at-mount pattern (SliderAdmin.tsx).
const RuleForm: Component<{
  rule?: PruningRule;
  onSaved: () => void;
  onCancel: () => void;
}> = (props) => {
  const [name, setName] = createSignal(props.rule?.name ?? "");
  const [mode, setMode] = createSignal<Mode>(
    (props.rule?.mode as Mode) ?? "movies",
  );
  const [ageEnabled, setAgeEnabled] = createSignal(
    (props.rule?.ageDays ?? 0) > 0,
  );
  const [ageDays, setAgeDays] = createSignal(props.rule?.ageDays ?? 0);
  const [sizeEnabled, setSizeEnabled] = createSignal(
    (props.rule?.sizeBytes ?? 0) > 0,
  );
  const [sizeGB, setSizeGB] = createSignal(
    props.rule?.sizeBytes ? bytesToGB(props.rule.sizeBytes) : 0,
  );
  const [tierEnabled, setTierEnabled] = createSignal(
    !!props.rule?.qualityTierFloor,
  );
  const [tier, setTier] = createSignal<PruningTierFloor>(
    (props.rule?.qualityTierFloor as PruningTierFloor) || "low",
  );
  const [enabled, setEnabled] = createSignal(props.rule?.enabled ?? true);
  const status = useSaveStatus();

  // hasCondition is AC1's "at least one condition configured" — mirrors
  // internal/pruning.ErrNoConditions client-side (both the submit guard and
  // the preview gate below share it, since the preview endpoint 400s on a
  // conditionless draft too).
  const hasCondition = () => ageEnabled() || sizeEnabled() || tierEnabled();

  const buildBody = (): PruningRuleUpsertRequest => ({
    name: name().trim(),
    mode: mode(),
    ageDays: ageEnabled() ? Math.max(0, Math.round(ageDays())) : 0,
    sizeBytes: sizeEnabled() ? Math.max(0, gbToBytes(sizeGB())) : 0,
    qualityTierFloor: tierEnabled() ? tier() : "",
    enabled: enabled(),
  });

  // --- §13.1 soft preview banner --------------------------------------
  // Debounced re-fetch whenever a condition (or its value) changes. Skipped
  // entirely while no condition is enabled — a blank name is fine (the
  // backend previews unnamed drafts), but a conditionless draft is not, so
  // firing it anyway would just paint an error where the form should stay
  // quiet. Save/Update is NEVER gated on this — a large or errored count
  // changes nothing about whether the submit button is enabled.
  const [previewCount, setPreviewCount] = createSignal<number | null>(null);
  const [previewError, setPreviewError] = createSignal("");
  let previewTimer: ReturnType<typeof setTimeout> | undefined;

  const runPreview = async () => {
    try {
      const count = await previewPruningRule(buildBody());
      setPreviewCount(count);
      setPreviewError("");
    } catch (e) {
      setPreviewError((e as Error).message);
      setPreviewCount(null);
    }
  };

  createEffect(
    on(
      [name, mode, ageEnabled, ageDays, sizeEnabled, sizeGB, tierEnabled, tier],
      () => {
        if (previewTimer !== undefined) clearTimeout(previewTimer);
        if (!hasCondition()) {
          setPreviewCount(null);
          setPreviewError("");
          return;
        }
        previewTimer = setTimeout(() => void runPreview(), PREVIEW_DEBOUNCE_MS);
      },
    ),
  );
  onCleanup(() => {
    if (previewTimer !== undefined) clearTimeout(previewTimer);
  });

  const save = async () => {
    if (!name().trim()) {
      status.failed(new Error("name is required"));
      return;
    }
    if (!hasCondition()) {
      status.failed(new Error("select at least one condition"));
      return;
    }
    try {
      if (props.rule) {
        await updatePruningRule(props.rule.id, buildBody());
      } else {
        await createPruningRule(buildBody());
      }
      props.onSaved();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded-md border border-dashed border-border p-3">
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="mb-2 block">
          <span class={labelClass}>Name</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Rule name"
            value={name()}
            onInput={(e) => setName(e.currentTarget.value)}
          />
        </label>
        <label class="mb-2 block">
          <span class={labelClass}>Mode</span>
          <select
            class={`${inputClass} mt-1`}
            aria-label="Rule mode"
            value={mode()}
            onChange={(e) => setMode(e.currentTarget.value as Mode)}
          >
            <For each={MODES}>{(m) => <option value={m.id}>{m.label}</option>}</For>
          </select>
        </label>

        <div class="mb-2 rounded border border-border p-2">
          <label class="flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable age condition"
              checked={ageEnabled()}
              onChange={(e) => setAgeEnabled(e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Older than N days</span>
          </label>
          <Show when={ageEnabled()}>
            <input
              type="number"
              min="0"
              class={`${inputClass} mt-1`}
              aria-label="Age in days"
              value={ageDays()}
              onInput={(e) => setAgeDays(Number(e.currentTarget.value))}
            />
          </Show>
        </div>

        <div class="mb-2 rounded border border-border p-2">
          <label class="flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable size condition"
              checked={sizeEnabled()}
              onChange={(e) => setSizeEnabled(e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Larger than N GB</span>
          </label>
          <Show when={sizeEnabled()}>
            <input
              type="number"
              min="0"
              step="0.1"
              class={`${inputClass} mt-1`}
              aria-label="Size in GB"
              value={sizeGB()}
              onInput={(e) => setSizeGB(Number(e.currentTarget.value))}
            />
          </Show>
        </div>

        <div class="mb-2 rounded border border-border p-2">
          <label class="flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable quality tier condition"
              checked={tierEnabled()}
              onChange={(e) => setTierEnabled(e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Quality tier at or below</span>
          </label>
          <Show when={tierEnabled()}>
            <select
              class={`${inputClass} mt-1`}
              aria-label="Quality tier floor"
              value={tier()}
              onChange={(e) => setTier(e.currentTarget.value as PruningTierFloor)}
            >
              <For each={PRUNING_TIER_FLOORS}>
                {(t) => <option value={t}>{PRUNING_TIER_FLOOR_LABELS[t]}</option>}
              </For>
            </select>
          </Show>
        </div>

        <label class="mb-2 flex items-center gap-2">
          <input
            type="checkbox"
            aria-label="Rule enabled"
            checked={enabled()}
            onChange={(e) => setEnabled(e.currentTarget.checked)}
          />
          <span class="text-sm text-fg">Enabled</span>
        </label>

        {/* §13.1 soft preview — a non-blocking count. Save/Update above stays
            enabled no matter what this shows, including a large N. */}
        <Show when={hasCondition()}>
          <div class="mb-2 text-sm text-muted" data-testid="pruning-preview">
            <Show when={previewError()}>
              <ErrorText>{previewError()}</ErrorText>
            </Show>
            <Show when={!previewError() && previewCount() !== null}>
              This rule would currently match {previewCount()} item
              {previewCount() === 1 ? "" : "s"}.
            </Show>
          </div>
        </Show>

        <div class="mt-2 flex items-center gap-2">
          <Button variant="primary" type="submit">
            {props.rule ? "Save changes" : "Create rule"}
          </Button>
          <Button onClick={props.onCancel}>Cancel</Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </form>
    </div>
  );
};

// PruningRulesSection is the Settings "Pruning" tab's whole panel: the rules
// CRUD list/form (§6.2) and the purge scan-interval control (§6.3's AC3 gap).
export const PruningRulesSection: Component = () => {
  const [rules, { refetch }] = createResource(fetchPruningRules, {
    initialValue: [],
  });
  const [scanInterval] = createResource(fetchPurgeScanInterval);
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");

  const closeForm = () => setEditing(null);
  const afterSave = () => {
    closeForm();
    void refetch();
  };

  const editingRule = (): PruningRule | undefined => {
    const e = editing();
    if (e === null || e === "new") return undefined;
    return (rules() ?? []).find((r) => r.id === e);
  };

  // toggleEnabled is an immediate full update — the upsert body is
  // deliberately whole-rule (mirrors updatePruningRuleHandler's own doc
  // comment: it overwrites every editable field), so every condition is
  // carried over from the current row and only `enabled` flips.
  const toggleEnabled = async (rule: PruningRule) => {
    setListError("");
    try {
      await updatePruningRule(rule.id, {
        name: rule.name,
        mode: rule.mode,
        // ageDays/sizeBytes/qualityTierFloor carry `omitempty` on the wire
        // (@dto marks them optional), so a rule at its unset sentinel comes
        // back as undefined rather than 0/"" — default it back to the
        // sentinel the upsert body requires.
        ageDays: rule.ageDays ?? 0,
        sizeBytes: rule.sizeBytes ?? 0,
        qualityTierFloor: rule.qualityTierFloor ?? "",
        enabled: !rule.enabled,
      });
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const remove = async (rule: PruningRule) => {
    if (!confirm(`Delete the "${rule.name}" rule?`)) return;
    setListError("");
    try {
      await deletePruningRule(rule.id);
      if (editing() === rule.id) closeForm();
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  return (
    <>
      <Card title="Pruning rules">
        <Muted class="mb-3">
          Operator-authored rules that flag library items for Purge review by
          age, size, and/or quality tier — AND'd within one rule, OR'd across
          rules. A rule only ever PROPOSES: nothing is deleted until an
          operator explicitly Applies it from the Purge review queue.
        </Muted>
        <Show when={rules.error}>
          <ErrorText>{(rules.error as Error)?.message}</ErrorText>
        </Show>
        <Show when={listError()}>
          <ErrorText>{listError()}</ErrorText>
        </Show>

        <Show
          when={(rules() ?? []).length > 0}
          fallback={<Muted>No pruning rules yet.</Muted>}
        >
          <ul>
            <For each={rules()}>
              {(rule) => (
                <li class="flex items-center justify-between gap-2 border-b border-border py-2 last:border-b-0">
                  <div class="min-w-0 flex-1">
                    <div class="truncate text-sm font-medium text-fg">
                      {rule.name}{" "}
                      <span class="text-xs text-muted">
                        ({MODE_LABELS[rule.mode as Mode] ?? rule.mode})
                      </span>
                    </div>
                    <div class="text-xs text-muted">
                      {summarizeConditions(rule)}
                    </div>
                  </div>
                  <label class="flex shrink-0 items-center gap-1 text-xs text-muted">
                    <input
                      type="checkbox"
                      aria-label={`${rule.name} enabled`}
                      checked={rule.enabled}
                      onChange={() => void toggleEnabled(rule)}
                    />
                    enabled
                  </label>
                  <Button
                    aria-label={`Edit ${rule.name}`}
                    onClick={() => setEditing(rule.id)}
                  >
                    <Pencil size={14} />
                  </Button>
                  <Button onClick={() => void remove(rule)}>Delete</Button>
                </li>
              )}
            </For>
          </ul>
        </Show>

        <Show
          when={editing() !== null}
          fallback={
            <div class="mt-3">
              <Button variant="primary" onClick={() => setEditing("new")}>
                + New rule
              </Button>
            </div>
          }
        >
          <RuleForm rule={editingRule()} onSaved={afterSave} onCancel={closeForm} />
        </Show>
      </Card>

      <Card title="Purge scan interval">
        <Muted class="mb-3">
          How often Purge automatically re-scans for tag- and rule-matched
          items to propose (0 = off — manual Scan only, the default). A
          scheduled scan only ever PROPOSES, exactly like a manual one —
          nothing is deleted without an explicit Apply in the Purge review
          queue. Turning this ON from 0 takes effect on the app's next
          restart; turning it OFF stops a running schedule on its next tick.
        </Muted>
        <DurationSetting
          id="purge-scan-interval"
          label="Purge scan interval"
          help="How often a scheduled Purge Scan runs in the background."
          value={() => scanInterval()}
          onSave={(v) => putPurgeScanInterval(v)}
        />
      </Card>
    </>
  );
};
