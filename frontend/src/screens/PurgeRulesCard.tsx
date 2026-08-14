// Claude 2026-08-11: new file — the Clean-up screen's collapsible "Rules"
// card, moved here from the deleted Settings "Pruning" tab
// (settings/PruningRules.tsx), plan
// .omc/plans/autopilot-impl-purge-rules-consolidation-cleanup-rename.md §8.
// Reason: rules are now Clean-up's ONLY matching mechanism — the global
// per-mode tag allowlist that used to sit in this slot on the Purge screen is
// retired, its tags folded in as a fourth AND'd per-rule condition. Keeping
// the builder on a separate Settings tab meant an operator configured matching
// in one place and reviewed its results in another.
// Troubleshooting: this is a native <details>/<summary>, class-for-class from
// OrganizeChrome.tsx's ActivityLogPanel — NOT a <Card> and NOT a new
// collapsible component. Note createResource fetches on mount even while the
// <details> is collapsed, so any test rendering the Clean-up screen must stub
// GET /api/pruning-rules.
// Review if: the rules builder ever moves back into Settings.
//
// Still follows SliderAdmin's inline-form create/edit pattern (one form,
// seeded from an optional `rule` prop) rather than RowEditor — rules have no
// meaningful display order, so drag-and-drop reorder does not apply here.
//
// Claude 2026-08-14: RuleForm is a dynamic AND-criteria row builder
// (field / operator / value / unit) instead of five checkboxes. One empty
// trailing row at a time; completing it appends another. Multiple complete
// rows are AND'd within the rule; rules stay OR'd across the list.
// Reason: two ages, contains + does-not-contain tags, etc. cannot fit in
// five scalars. The form always POSTs `criteria` and zeros the old fields.
// Troubleshooting: do not put aria-pressed on these controls — Library
// sort tests select button[aria-pressed] as card shells. Seed from
// rule.criteria, falling back to the five scalars for pre-0014 payloads.
//
// Claude 2026-08-14: tag rows are chips + contains/notContains + Any/All
// (matchMode). Default contains+Any; at least one chip to complete the row.
// Reason: 0014 one-contains-per-tag was AND; Any restores OR on one row,
//   All is AND on the same chips. notContains uses the same mode.
// Troubleshooting: chipInput is local UI state, not POSTed. Empty chips
//   are incomplete (same as an empty value). No aria-pressed.
// Review if: drag-and-drop criterion ordering ships.

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
import type { Mode } from "../api/discover";
import {
  GB_BYTES,
  createPruningRule,
  deletePruningRule,
  fetchPruningRules,
  previewPruningRule,
  updatePruningRule,
  type PruningCriterion,
  type PruningRule,
  type PruningRuleUpsertRequest,
} from "../api/pruningRules";
import {
  Button,
  ErrorText,
  Muted,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../components/ui";

const PREVIEW_DEBOUNCE_MS = 300;

const CRITERION_FIELDS = ["age", "size", "quality", "tag", "rating"] as const;
type CriterionField = (typeof CRITERION_FIELDS)[number];

const COMPARE_OPS = [
  { value: "gt", label: "greater than" },
  { value: "lt", label: "less than" },
  { value: "eq", label: "equal to" },
] as const;
const TAG_OPS = [
  { value: "contains", label: "contains" },
  { value: "notContains", label: "does not contain" },
] as const;
const TAG_MATCH_MODES = [
  { value: "any", label: "any" },
  { value: "all", label: "all" },
] as const;

type CriterionDraft = {
  field: CriterionField | "";
  op: string;
  value: string;
  unit: string;
  values: string[];
  matchMode: string;
  chipInput: string;
};

export function gbToBytes(gb: number): number {
  return Math.round(gb * GB_BYTES);
}
export function bytesToGB(bytes: number): number {
  return Math.round((bytes / GB_BYTES) * 100) / 100;
}

const OP_LABEL: Record<string, string> = {
  gt: ">",
  lt: "<",
  eq: "=",
  contains: "contains",
  notContains: "does not contain",
};

function summarizeCriterion(c: PruningCriterion): string {
  if (c.field === "tag") {
    const op = c.op === "notContains" ? "does not contain" : "contains";
    const mode = c.matchMode === "all" ? "all of" : "any of";
    const tags = (c.values?.length ? c.values : c.value ? [c.value] : []).join(
      ", ",
    );
    return `tag ${op} ${mode} ${tags}`;
  }
  const op = OP_LABEL[c.op] ?? c.op;
  const unit = c.unit ? ` ${c.unit}` : "";
  return `${c.field} ${op} ${c.value}${unit}`;
}

// summarizeConditions renders a rule's configured conditions for the list
// row. Prefers criteria (AND, row order). Falls back to the five scalars for
// any leftover pre-0014 payload.
export function summarizeConditions(rule: PruningRule): string {
  if (rule.criteria?.length) {
    return rule.criteria.map(summarizeCriterion).join(" AND ");
  }
  const parts: string[] = [];
  if (rule.ageDays) parts.push(`${rule.ageDays}+ days old`);
  if (rule.sizeBytes) parts.push(`${bytesToGB(rule.sizeBytes)}+ GB`);
  if (rule.qualityTierFloor) parts.push(`tier ≤ ${rule.qualityTierFloor}`);
  if (rule.tags?.length) parts.push(`tags: ${rule.tags.join(", ")}`);
  if (rule.minRating) parts.push(`rated below ${rule.minRating}★`);
  return parts.length ? parts.join(", ") : "no conditions";
}

function emptyCriterion(): CriterionDraft {
  return {
    field: "",
    op: "",
    value: "",
    unit: "",
    values: [],
    matchMode: "",
    chipInput: "",
  };
}

function isBlankCriterion(row: CriterionDraft): boolean {
  // A tag field with no chips yet is in-progress, not a second blank slot.
  if (row.field) return false;
  return (
    !row.op &&
    !row.value.trim() &&
    !row.unit &&
    row.values.length === 0 &&
    !row.matchMode &&
    !row.chipInput.trim()
  );
}

function isCompleteCriterion(row: CriterionDraft): boolean {
  if (!row.field || !row.op) return false;
  if (row.field === "tag") {
    return (
      (row.matchMode === "any" || row.matchMode === "all") &&
      row.values.length >= 1
    );
  }
  if (!row.value.trim()) return false;
  if (row.field === "age" || row.field === "size" || row.field === "rating") {
    return !!row.unit;
  }
  return true;
}

// Keep every non-blank row, then exactly one trailing empty slot when the
// last kept row is complete (or the list is empty). An in-progress row
// (field chosen, value empty) is the working row — do not also show a blank.
function normalizeCriterionRows(rows: CriterionDraft[]): CriterionDraft[] {
  const kept = rows.filter((r) => !isBlankCriterion(r));
  const last = kept[kept.length - 1];
  if (!last || isCompleteCriterion(last)) {
    kept.push(emptyCriterion());
  }
  return kept;
}

function defaultOp(field: CriterionField): string {
  return field === "tag" ? "contains" : "gt";
}

function defaultUnit(field: CriterionField): string {
  if (field === "age") return "days";
  if (field === "size") return "gb";
  if (field === "rating") return "stars";
  return "";
}

function opsFor(field: CriterionField | ""): readonly { value: string; label: string }[] {
  if (field === "tag") return TAG_OPS;
  return COMPARE_OPS;
}

function unitsFor(field: CriterionField | ""): { value: string; label: string }[] {
  if (field === "age") return [{ value: "days", label: "days" }];
  if (field === "size") {
    return [
      { value: "kb", label: "KB" },
      { value: "mb", label: "MB" },
      { value: "gb", label: "GB" },
      { value: "tb", label: "TB" },
    ];
  }
  if (field === "rating") return [{ value: "stars", label: "stars" }];
  return [];
}

function legacyTierCriterion(floor: string): CriterionDraft | null {
  switch (floor) {
    case "low":
      return { ...emptyCriterion(), field: "quality", op: "eq", value: "low" };
    case "medium":
      return { ...emptyCriterion(), field: "quality", op: "lt", value: "high" };
    case "high":
      return {
        ...emptyCriterion(),
        field: "quality",
        op: "lt",
        value: "lossless",
      };
    default:
      return null;
  }
}

function rowsFromRule(rule?: PruningRule): CriterionDraft[] {
  const rows: CriterionDraft[] = [];
  if (rule?.criteria?.length) {
    for (const c of rule.criteria) {
      const field = (CRITERION_FIELDS as readonly string[]).includes(c.field)
        ? (c.field as CriterionField)
        : "";
      const values =
        c.values?.length ? c.values : c.value?.trim() ? [c.value] : [];
      rows.push({
        field,
        op: c.op,
        value: field === "tag" ? "" : c.value,
        unit: c.unit ?? "",
        values: field === "tag" ? values : [],
        matchMode:
          field === "tag" ? (c.matchMode === "all" ? "all" : "any") : "",
        chipInput: "",
      });
    }
    return normalizeCriterionRows(rows);
  }
  if (rule?.ageDays) {
    rows.push({
      ...emptyCriterion(),
      field: "age",
      op: "gt",
      value: String(rule.ageDays),
      unit: "days",
    });
  }
  if (rule?.sizeBytes) {
    rows.push({
      ...emptyCriterion(),
      field: "size",
      op: "gt",
      value: String(bytesToGB(rule.sizeBytes)),
      unit: "gb",
    });
  }
  if (rule?.qualityTierFloor) {
    const mapped = legacyTierCriterion(rule.qualityTierFloor);
    if (mapped) rows.push(mapped);
  }
  if (rule?.tags?.length) {
    rows.push({
      ...emptyCriterion(),
      field: "tag",
      op: "contains",
      values: [...rule.tags],
      matchMode: "any",
    });
  }
  if (rule?.minRating) {
    rows.push({
      ...emptyCriterion(),
      field: "rating",
      op: "lt",
      value: String(rule.minRating),
      unit: "stars",
    });
  }
  return normalizeCriterionRows(rows);
}

function draftToCriterion(row: CriterionDraft): PruningCriterion {
  if (row.field === "tag") {
    const out: PruningCriterion = {
      field: "tag",
      op: row.op,
      value: "",
      values: row.values,
      matchMode: row.matchMode || "any",
    };
    return out;
  }
  const out: PruningCriterion = {
    field: row.field,
    op: row.op,
    value: row.value.trim(),
  };
  if (row.unit) out.unit = row.unit;
  return out;
}

const RuleForm: Component<{
  mode: Mode;
  rule?: PruningRule;
  onSaved: () => void;
  onCancel: () => void;
}> = (props) => {
  const [name, setName] = createSignal(props.rule?.name ?? "");
  const [rows, setRows] = createSignal<CriterionDraft[]>(rowsFromRule(props.rule));
  const [enabled, setEnabled] = createSignal(props.rule?.enabled ?? true);
  const status = useSaveStatus();

  const completeRows = () => rows().filter(isCompleteCriterion);
  const hasCondition = () => completeRows().length > 0;

  const patchRow = (index: number, patch: Partial<CriterionDraft>) => {
    setRows((prev) =>
      normalizeCriterionRows(
        prev.map((r, i) => (i === index ? { ...r, ...patch } : r)),
      ),
    );
  };

  const onFieldChange = (index: number, field: CriterionField | "") => {
    setRows((prev) =>
      normalizeCriterionRows(
        prev.map((r, i) => {
          if (i !== index) return r;
          if (!field) return emptyCriterion();
          if (field === "tag") {
            return {
              ...emptyCriterion(),
              field: "tag",
              op: defaultOp("tag"),
              matchMode: r.field === "tag" && r.matchMode ? r.matchMode : "any",
              values: r.field === "tag" ? r.values : [],
              chipInput: r.field === "tag" ? r.chipInput : "",
            };
          }
          return {
            ...emptyCriterion(),
            field,
            op: defaultOp(field),
            value: r.field === field ? r.value : "",
            unit: defaultUnit(field),
          };
        }),
      ),
    );
  };

  const addChip = (index: number) => {
    setRows((prev) =>
      normalizeCriterionRows(
        prev.map((r, i) => {
          if (i !== index) return r;
          const next = r.chipInput.trim();
          if (!next) return r;
          const exists = r.values.some(
            (v) => v.toLowerCase() === next.toLowerCase(),
          );
          return {
            ...r,
            values: exists ? r.values : [...r.values, next],
            chipInput: "",
          };
        }),
      ),
    );
  };

  const removeChip = (index: number, tag: string) => {
    setRows((prev) =>
      normalizeCriterionRows(
        prev.map((r, i) =>
          i === index ? { ...r, values: r.values.filter((v) => v !== tag) } : r,
        ),
      ),
    );
  };

  const removeRow = (index: number) => {
    setRows((prev) => normalizeCriterionRows(prev.filter((_, i) => i !== index)));
  };

  const buildBody = (): PruningRuleUpsertRequest => ({
    name: name().trim(),
    mode: props.mode,
    ageDays: 0,
    sizeBytes: 0,
    qualityTierFloor: "",
    tags: [],
    minRating: 0,
    criteria: completeRows().map(draftToCriterion),
    enabled: enabled(),
  });

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
    on([name, rows], () => {
      if (previewTimer !== undefined) clearTimeout(previewTimer);
      if (!hasCondition()) {
        setPreviewCount(null);
        setPreviewError("");
        return;
      }
      previewTimer = setTimeout(() => void runPreview(), PREVIEW_DEBOUNCE_MS);
    }),
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

        <p class="mb-2 text-xs text-muted">
          Each row is AND'd with the others. Completing a row adds another.
        </p>
        <div class="mb-2 space-y-2">
          <For each={rows()}>
            {(row, i) => {
              const n = () => i() + 1;
              const fieldSet = () => !!row.field;
              const unitOptions = () => unitsFor(row.field);
              const canRemove = () => !isBlankCriterion(row);
              return (
                <div class="flex flex-wrap items-center gap-2">
                  <select
                    class={`${inputClass} min-w-[7rem] !w-auto`}
                    aria-label={`Criterion ${n()} field`}
                    value={row.field}
                    onChange={(e) =>
                      onFieldChange(i(), e.currentTarget.value as CriterionField | "")
                    }
                  >
                    <option value="">field</option>
                    <For each={CRITERION_FIELDS}>
                      {(f) => <option value={f}>{f}</option>}
                    </For>
                  </select>
                  <select
                    class={`${inputClass} min-w-[9rem] !w-auto`}
                    aria-label={`Criterion ${n()} operator`}
                    value={row.op}
                    disabled={!fieldSet()}
                    onChange={(e) => patchRow(i(), { op: e.currentTarget.value })}
                  >
                    <Show when={!row.op}>
                      <option value="">operator</option>
                    </Show>
                    <For each={opsFor(row.field)}>
                      {(op) => <option value={op.value}>{op.label}</option>}
                    </For>
                  </select>
                  <Show when={row.field === "tag"}>
                    <select
                      class={`${inputClass} min-w-[5rem] !w-auto`}
                      aria-label={`Criterion ${n()} match mode`}
                      value={row.matchMode}
                      onChange={(e) =>
                        patchRow(i(), { matchMode: e.currentTarget.value })
                      }
                    >
                      <For each={TAG_MATCH_MODES}>
                        {(m) => <option value={m.value}>{m.label}</option>}
                      </For>
                    </select>
                    <div class="flex min-w-[12rem] flex-1 flex-wrap items-center gap-1">
                      <For each={row.values}>
                        {(tag) => (
                          <span class="inline-flex items-center gap-1 rounded border border-border px-2 py-0.5 text-sm text-fg">
                            {tag}
                            <Button
                              type="button"
                              aria-label={`Remove ${tag}`}
                              onClick={() => removeChip(i(), tag)}
                            >
                              ×
                            </Button>
                          </span>
                        )}
                      </For>
                      <input
                        type="text"
                        class={`${inputClass} min-w-[7rem] flex-1`}
                        aria-label={`Criterion ${n()} new tag`}
                        placeholder="tag"
                        value={row.chipInput}
                        onInput={(e) =>
                          patchRow(i(), { chipInput: e.currentTarget.value })
                        }
                        onKeyDown={(e) => {
                          if (e.key !== "Enter") return;
                          e.preventDefault();
                          addChip(i());
                        }}
                      />
                      <Button
                        type="button"
                        aria-label={`Add tag to criterion ${n()}`}
                        onClick={() => addChip(i())}
                      >
                        Add
                      </Button>
                    </div>
                  </Show>
                  <Show when={row.field !== "tag"}>
                    <input
                      type={
                        row.field === "quality" || !row.field ? "text" : "number"
                      }
                      min={row.field === "rating" ? 1 : 0}
                      max={row.field === "rating" ? 5 : undefined}
                      step={row.field === "size" ? "0.1" : "1"}
                      class={`${inputClass} min-w-[7rem] flex-1`}
                      aria-label={`Criterion ${n()} value`}
                      placeholder="value"
                      value={row.value}
                      disabled={!fieldSet()}
                      onInput={(e) =>
                        patchRow(i(), { value: e.currentTarget.value })
                      }
                    />
                  </Show>
                  <select
                    class={`${inputClass} min-w-[5rem] !w-auto`}
                    aria-label={`Criterion ${n()} unit`}
                    value={row.unit}
                    disabled={!fieldSet() || unitOptions().length === 0}
                    onChange={(e) => patchRow(i(), { unit: e.currentTarget.value })}
                  >
                    <Show when={unitOptions().length === 0}>
                      <option value=""></option>
                    </Show>
                    <For each={unitOptions()}>
                      {(u) => <option value={u.value}>{u.label}</option>}
                    </For>
                  </select>
                  <Show when={canRemove()}>
                    <Button
                      type="button"
                      aria-label={`Remove criterion ${n()}`}
                      onClick={() => removeRow(i())}
                    >
                      ×
                    </Button>
                  </Show>
                </div>
              );
            }}
          </For>
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

export const PurgeRulesCard: Component<{ mode: Mode }> = (props) => {
  const [rules, { refetch }] = createResource(fetchPruningRules, {
    initialValue: [],
  });
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");

  const modeRules = () => (rules() ?? []).filter((r) => r.mode === props.mode);

  const closeForm = () => setEditing(null);
  const afterSave = () => {
    closeForm();
    void refetch();
  };

  const editingRule = (): PruningRule | undefined => {
    const e = editing();
    if (e === null || e === "new") return undefined;
    return modeRules().find((r) => r.id === e);
  };

  // toggleEnabled is an immediate full update — the upsert body is
  // deliberately whole-rule. criteria MUST be carried: omitting it would
  // send [] and wipe every AND row on a mere enable flip.
  const toggleEnabled = async (rule: PruningRule) => {
    setListError("");
    try {
      await updatePruningRule(rule.id, {
        name: rule.name,
        mode: rule.mode,
        ageDays: rule.ageDays ?? 0,
        sizeBytes: rule.sizeBytes ?? 0,
        qualityTierFloor: rule.qualityTierFloor ?? "",
        tags: rule.tags ?? [],
        minRating: rule.minRating ?? 0,
        criteria: rule.criteria ?? [],
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
    <details class="mt-6 rounded border border-border p-3">
      <summary class="cursor-pointer text-sm font-medium">Rules</summary>
      <Muted class="mb-3 mt-3 block">
        Operator-authored rules that flag library items for Clean-up review.
        Criteria are AND'd within one rule, OR'd across rules. A rule only ever
        PROPOSES: nothing is deleted until an operator explicitly Applies it
        from the review queue below.
      </Muted>
      <Show when={rules.error}>
        <ErrorText>{(rules.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={listError()}>
        <ErrorText>{listError()}</ErrorText>
      </Show>

      <Show
        when={modeRules().length > 0}
        fallback={<Muted>No rules for this mode yet.</Muted>}
      >
        <ul>
          <For each={modeRules()}>
            {(rule) => (
              <li class="flex items-center justify-between gap-2 border-b border-border py-2 last:border-b-0">
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium text-fg">
                    {rule.name}
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
        <RuleForm
          mode={props.mode}
          rule={editingRule()}
          onSaved={afterSave}
          onCancel={closeForm}
        />
      </Show>
    </details>
  );
};
