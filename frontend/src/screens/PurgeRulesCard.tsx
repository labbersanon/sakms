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
  PRUNING_TIER_FLOORS,
  PRUNING_TIER_FLOOR_LABELS,
  createPruningRule,
  deletePruningRule,
  fetchPruningRules,
  previewPruningRule,
  updatePruningRule,
  type PruningRule,
  type PruningRuleUpsertRequest,
  type PruningTierFloor,
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

// PREVIEW_DEBOUNCE_MS mirrors FolderPicker's as-you-type debounce
// (components/FolderPicker.tsx) — a keystroke in an age/size field, or a
// condition/tier/tag change, doesn't fire a preview request until it settles.
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
// generates at match time, but here it echoes the rule's THRESHOLDS, not an
// item's triggering values, since no item is in scope. Fragment order matches
// pruning.Match's own: age, size, tier, tags, rating.
export function summarizeConditions(rule: PruningRule): string {
  const parts: string[] = [];
  if (rule.ageDays) parts.push(`${rule.ageDays}+ days old`);
  if (rule.sizeBytes) parts.push(`${bytesToGB(rule.sizeBytes)}+ GB`);
  if (rule.qualityTierFloor) parts.push(`tier ≤ ${rule.qualityTierFloor}`);
  if (rule.tags?.length) parts.push(`tags: ${rule.tags.join(", ")}`);
  if (rule.minRating) parts.push(`rated below ${rule.minRating}★`);
  return parts.length ? parts.join(", ") : "no conditions";
}

// RuleForm creates a new rule (rule prop undefined) or edits an existing one
// (rule prop present) — the same form either way, following SliderForm's
// seed-from-props-at-mount pattern (SliderAdmin.tsx).
//
// There is deliberately NO Mode select: the card is mounted per-mode from the
// Clean-up screen's own mode tab, so the rule's mode comes from the prop. The
// old Settings tab was mode-agnostic and needed the dropdown; keeping it here
// would let an operator create a Series rule from the Movies tab and watch it
// vanish from the list it was created in.
const RuleForm: Component<{
  mode: Mode;
  rule?: PruningRule;
  onSaved: () => void;
  onCancel: () => void;
}> = (props) => {
  const [name, setName] = createSignal(props.rule?.name ?? "");
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
  const [tagsEnabled, setTagsEnabled] = createSignal(
    (props.rule?.tags ?? []).length > 0,
  );
  const [tags, setTags] = createSignal<string[]>(props.rule?.tags ?? []);
  const [newTag, setNewTag] = createSignal("");
  const [ratingEnabled, setRatingEnabled] = createSignal(
    (props.rule?.minRating ?? 0) > 0,
  );
  const [minRating, setMinRating] = createSignal(props.rule?.minRating ?? 3);
  const [enabled, setEnabled] = createSignal(props.rule?.enabled ?? true);
  const status = useSaveStatus();

  // Add/remove are CLIENT-SIDE ONLY — they mutate the local signal and fire no
  // request. This is a real behavior change from the retired allowlist, whose
  // Add POSTed immediately: a tag is now part of the rule and is persisted by
  // the form's own Save, so an abandoned edit changes nothing.
  const addTag = () => {
    const tag = newTag().trim();
    if (!tag) return;
    // Adding a tag already present is a no-op, not an error — preserving the
    // retired allowlist.Store.Add's documented semantic. De-duped
    // case-insensitively to match pruning.matchedTags' own comparison.
    if (!tags().some((t) => t.toLowerCase() === tag.toLowerCase())) {
      setTags([...tags(), tag]);
    }
    setNewTag("");
  };
  const removeTag = (tag: string) => setTags(tags().filter((t) => t !== tag));

  // hasCondition is AC1's "at least one condition configured" — mirrors
  // internal/pruning.ErrNoConditions client-side, which went four-way in the
  // same change (both the submit guard and the preview gate below share it,
  // since the preview endpoint 400s on a conditionless draft too).
  const hasCondition = () =>
    ageEnabled() ||
    sizeEnabled() ||
    tierEnabled() ||
    tags().length > 0 ||
    (ratingEnabled() && minRating() > 0);

  const buildBody = (): PruningRuleUpsertRequest => ({
    name: name().trim(),
    mode: props.mode,
    ageDays: ageEnabled() ? Math.max(0, Math.round(ageDays())) : 0,
    sizeBytes: sizeEnabled() ? Math.max(0, gbToBytes(sizeGB())) : 0,
    qualityTierFloor: tierEnabled() ? tier() : "",
    tags: tagsEnabled() ? tags() : [],
    minRating: ratingEnabled() ? Math.min(5, Math.max(1, Math.round(minRating()))) : 0,
    enabled: enabled(),
  });

  // --- soft preview banner ---------------------------------------------
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
      // tags/tagsEnabled are in the dependency list deliberately: without
      // them, adding a tag would leave a stale match count on screen, which is
      // worse than showing none.
      [
        name,
        ageEnabled,
        ageDays,
        sizeEnabled,
        sizeGB,
        tierEnabled,
        tier,
        tagsEnabled,
        tags,
        ratingEnabled,
        minRating,
      ],
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

        {/* Tags — the fourth condition. Rating is fifth, last, so form order
            matches pruning.Match's fragment order. Plain chip input, the
            retired Allowlist's own markup: one × per chip, one Add per input,
            no clear-all and no picker/autocomplete. */}
        <div class="mb-2 rounded border border-border p-2">
          <label class="flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable tags condition"
              checked={tagsEnabled()}
              onChange={(e) => setTagsEnabled(e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Tagged with any of</span>
          </label>
          <Show when={tagsEnabled()}>
            <div class="mt-2 flex flex-wrap items-center gap-2">
              <For each={tags()}>
                {(tag) => (
                  <span class="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-xs text-fg">
                    {tag}
                    <button
                      type="button"
                      class="text-muted hover:text-danger"
                      aria-label={`Remove ${tag}`}
                      onClick={() => removeTag(tag)}
                    >
                      ×
                    </button>
                  </span>
                )}
              </For>
              <div class="flex items-center gap-2">
                <input
                  class="w-40 rounded-md border border-border bg-bg px-3 py-1.5 text-sm text-fg outline-none focus:border-accent"
                  placeholder="tag name"
                  value={newTag()}
                  onInput={(e) => setNewTag(e.currentTarget.value)}
                  onKeyDown={(e) => {
                    // Enter adds the tag rather than submitting the whole
                    // form — the rule almost never wants saving mid-tag-entry.
                    if (e.key === "Enter") {
                      e.preventDefault();
                      addTag();
                    }
                  }}
                  aria-label="New tag"
                />
                <Button type="button" onClick={() => addTag()}>
                  Add
                </Button>
              </div>
            </div>
          </Show>
        </div>

        <div class="mb-2 rounded border border-border p-2">
          <label class="flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable minimum rating condition"
              checked={ratingEnabled()}
              onChange={(e) => setRatingEnabled(e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Rated below N stars</span>
          </label>
          <Show when={ratingEnabled()}>
            <select
              class={`${inputClass} mt-1`}
              aria-label="Minimum rating"
              value={String(minRating())}
              onChange={(e) => setMinRating(Number(e.currentTarget.value))}
            >
              <For each={[1, 2, 3, 4, 5]}>
                {(n) => (
                  <option value={n}>
                    {n} star{n === 1 ? "" : "s"}
                  </option>
                )}
              </For>
            </select>
            <p class="mt-1 text-xs text-muted">
              Propose deletion for titles rated below this. Unrated titles never
              match.
            </p>
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

        {/* Soft preview — a non-blocking count. Save/Update above stays
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

// PurgeRulesCard is the Clean-up screen's collapsible rules panel: the rules
// CRUD list/form, scoped to one mode.
export const PurgeRulesCard: Component<{ mode: Mode }> = (props) => {
  const [rules, { refetch }] = createResource(fetchPruningRules, {
    initialValue: [],
  });
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");

  // The endpoint is deliberately not mode-scoped (a rule carries its own
  // single mode in the BODY, and /api/pruning-rules has no mode segment), so
  // the card filters client-side rather than growing a query param.
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
  // deliberately whole-rule (mirrors updatePruningRuleHandler's own doc
  // comment: it overwrites every editable field), so every condition is
  // carried over from the current row and only `enabled` flips.
  const toggleEnabled = async (rule: PruningRule) => {
    setListError("");
    try {
      await updatePruningRule(rule.id, {
        name: rule.name,
        mode: rule.mode,
        // ageDays/sizeBytes/qualityTierFloor/tags carry `omitempty` on the
        // wire (@dto marks them optional), so a rule at its unset sentinel
        // comes back as undefined rather than 0/""/[] — default it back to the
        // sentinel the upsert body requires. `tags` is the dangerous one:
        // omitting it here would silently CLEAR a rule's tags on every
        // enable/disable toggle, since the body is whole-rule.
        ageDays: rule.ageDays ?? 0,
        sizeBytes: rule.sizeBytes ?? 0,
        qualityTierFloor: rule.qualityTierFloor ?? "",
        tags: rule.tags ?? [],
        minRating: rule.minRating ?? 0,
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
        Operator-authored rules that flag library items for Clean-up review by
        age, size, quality tier, tags and/or minimum rating — AND'd within one rule, OR'd
        across rules. A rule only ever PROPOSES: nothing is deleted until an
        operator explicitly Applies it from the review queue below.
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
