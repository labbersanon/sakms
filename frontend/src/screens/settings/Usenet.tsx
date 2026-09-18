// Usenet — a sub-tab of the Settings > Download tab (see Download.tsx), not a
// row in a connections table.
//
// It has its own page precisely because a Usenet subscription's field set is
// richer than the URL + API-key shape ConnectionRow renders: label, host, port,
// TLS, username, password, max-connections. Deliberately does NOT reuse
// ConnectionRow.
//
// Two things this page must never grow:
//   - Priority / fallback ordering between subscriptions. When a download needs
//     Usenet, every enabled subscription is tried, with no priority ordering at
//     all — an operator runs several providers for differing retention and
//     takedown coverage, not as a ranked chain. There is no priority field and
//     no reorder UI beyond cosmetic sorting.
//   - A generic "add any connection type" framework. This page adds NNTP
//     subscriptions and nothing else.
//   - A retry-interval input. The cadence is coupled to the auto-grab toggle
//     server-side (on → 86400s, off → 0) precisely so the two can't disagree;
//     there is deliberately no user-facing control for it.

import {
  type Component,
  For,
  Show,
  createResource,
  createSignal,
  onMount,
} from "solid-js";
import {
  DEFAULT_MAX_CONNS,
  NNTP_DEFAULT_PORT,
  buildServiceConnectionBody,
  createServiceConnection,
  deleteServiceConnection,
  fetchServiceConnections,
  testServiceConnection,
  testStoredServiceConnection,
  updateServiceConnection,
  type ServiceConnectionSummary,
} from "../../api/serviceConnections";
import {
  fetchUsenetAutoGrabEnabled,
  fetchUsenetMaxConcurrentDownloads,
  fetchUsenetNNTPGroups,
  fetchUsenetNNTPNative,
  fetchUsenetSegmentResume,
  putUsenetAutoGrabEnabled,
  putUsenetMaxConcurrentDownloads,
  putUsenetNNTPNative,
  putUsenetSegmentResume,
  type UsenetNNTPNativeSettings,
} from "../../api/usenet";
import { fetchAutoGrabSlots, putAutoGrabSlots } from "../../api/autograbSlots";
import {
  Button,
  ErrorText,
  Muted,
  inputClass,
  labelClass,
} from "../../components/ui";
import {
  AutoGrabSlotFields,
  Card,
  SaveStatus,
  SectionSave,
  autoGrabSlotsValid,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

// One SectionSave wraps BOTH cards, so the page has a single Save button that
// commits every dirty subscription row and the auto-grab toggle together (each
// child still fires its OWN request — SectionSave batches the trigger, never
// the payload). Add and Delete stay immediate, per-row actions.
export const UsenetSection: Component = () => (
  <div>
    <SectionSave>
      <SubscriptionsCard />
      <DownloadsCard />
      <ResumeCard />
      <AutoGrabCard />
      <NativeSearchCard />
    </SectionSave>
  </div>
);

// SubscriptionDraft is one subscription's editable field set — the shape both
// the per-row editor and the Add form bind to. No priority/sortOrder field:
// see the ordering note at the top of this file.
interface SubscriptionDraft {
  label: string;
  host: string;
  port: number;
  tls: boolean;
  username: string;
  secret: string;
  maxConns: number;
  enabled: boolean;
}

// SubscriptionFields renders the field set itself, shared by the edit row and
// the Add form so the two can't drift. ariaPrefix distinguishes the Add form's
// inputs from the rows' in the accessibility tree (and in tests), since several
// rows render the same labels at once.
const SubscriptionFields: Component<{
  draft: () => SubscriptionDraft;
  onChange: (patch: Partial<SubscriptionDraft>) => void;
  secretPlaceholder: string;
  ariaPrefix: string;
}> = (props) => (
  <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
    <label class="block">
      <span class={labelClass}>Label</span>
      <input
        type="text"
        class={inputClass}
        placeholder="e.g. Eweka"
        aria-label={`${props.ariaPrefix} label`}
        value={props.draft().label}
        onInput={(e) => props.onChange({ label: e.currentTarget.value })}
      />
    </label>
    <label class="block">
      <span class={labelClass}>Host</span>
      <input
        type="text"
        class={inputClass}
        placeholder="news.example.com"
        aria-label={`${props.ariaPrefix} host`}
        value={props.draft().host}
        onInput={(e) => props.onChange({ host: e.currentTarget.value })}
      />
    </label>
    <label class="block">
      <span class={labelClass}>Port</span>
      <input
        type="number"
        class={inputClass}
        aria-label={`${props.ariaPrefix} port`}
        value={props.draft().port}
        onInput={(e) =>
          props.onChange({ port: Number(e.currentTarget.value) || 0 })
        }
      />
    </label>
    <label class="block">
      <span class={labelClass}>Username</span>
      <input
        type="text"
        class={inputClass}
        aria-label={`${props.ariaPrefix} username`}
        value={props.draft().username}
        onInput={(e) => props.onChange({ username: e.currentTarget.value })}
      />
    </label>
    <label class="block">
      <span class={labelClass}>Password</span>
      <input
        type="password"
        class={inputClass}
        placeholder={props.secretPlaceholder}
        aria-label={`${props.ariaPrefix} password`}
        value={props.draft().secret}
        onInput={(e) => props.onChange({ secret: e.currentTarget.value })}
      />
    </label>
    <label class="block">
      <span class={labelClass}>Max connections</span>
      <input
        type="number"
        class={inputClass}
        aria-label={`${props.ariaPrefix} max connections`}
        value={props.draft().maxConns}
        onInput={(e) =>
          props.onChange({ maxConns: Number(e.currentTarget.value) || 0 })
        }
      />
      <span class="mt-1 block text-xs text-muted">
        0 = use the default {DEFAULT_MAX_CONNS}
      </span>
    </label>
    <label class="flex items-center gap-2">
      <input
        type="checkbox"
        aria-label={`${props.ariaPrefix} TLS`}
        checked={props.draft().tls}
        onChange={(e) => props.onChange({ tls: e.currentTarget.checked })}
      />
      <span class="text-sm text-fg">TLS</span>
    </label>
    <label class="flex items-center gap-2">
      <input
        type="checkbox"
        aria-label={`${props.ariaPrefix} enabled`}
        checked={props.draft().enabled}
        onChange={(e) => props.onChange({ enabled: e.currentTarget.checked })}
      />
      <span class="text-sm text-fg">Enabled</span>
    </label>
  </div>
);

// SubscriptionRow is one saved subscription's always-editable controls.
// secretTouched tracks whether the operator actually typed a password — the
// input is blank for a configured subscription (the stored secret is never sent
// back), so an untouched blank MUST be omitted from the body rather than
// persisted as "", which the backend reads as "clear it".
const SubscriptionRow: Component<{
  conn: ServiceConnectionSummary;
  onChanged: () => void;
}> = (props) => {
  const [draft, setDraft] = createSignal<SubscriptionDraft>({
    label: props.conn.label ?? "",
    host: props.conn.host ?? "",
    port: props.conn.port ?? NNTP_DEFAULT_PORT,
    tls: props.conn.tls ?? false,
    username: props.conn.username ?? "",
    secret: "",
    maxConns: props.conn.maxConns ?? 0,
    enabled: props.conn.enabled,
  });
  const [secretTouched, setSecretTouched] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const status = useSaveStatus();

  const change = (patch: Partial<SubscriptionDraft>) => {
    if (patch.secret !== undefined) setSecretTouched(true);
    setDraft((d) => ({ ...d, ...patch }));
    setDirty(true);
  };

  const body = () =>
    buildServiceConnectionBody({
      base: {
        kind: "usenet",
        provider: "nntp",
        label: draft().label,
        enabled: draft().enabled,
        host: draft().host,
        port: draft().port,
        tls: draft().tls,
        maxConns: draft().maxConns,
        username: draft().username,
      },
      secretTouched: secretTouched(),
      secretValue: draft().secret,
      hasExistingSecret: props.conn.hasSecret,
    });

  // save sets its OWN inline status and rethrows so the section batcher can
  // report which rows failed. On success it clears the touched/secret state, so
  // a subsequent untouched save omits `secret` again.
  const save = async () => {
    try {
      await updateServiceConnection(props.conn.id, body());
      status.set("✓ saved");
      setDraft((d) => ({ ...d, secret: "" }));
      setSecretTouched(false);
      setDirty(false);
      props.onChanged();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: `usenet-subscription:${props.conn.id}`,
    label: props.conn.label || props.conn.host || `subscription ${props.conn.id}`,
    dirty,
    save,
  });

  // An untouched secret isn't in the client's hands at all, so a saved row is
  // tested through its own id route (which uses the stored secret); a retyped
  // one is validated before saving through the stateless route.
  const test = async () => {
    status.set("testing…");
    try {
      const r = secretTouched()
        ? await testServiceConnection({
            provider: "nntp",
            host: draft().host,
            port: draft().port,
            tls: draft().tls,
            username: draft().username,
            secret: draft().secret,
          })
        : await testStoredServiceConnection(props.conn.id);
      if (r.ok) status.set("✓ ok");
      else status.failed(new Error(r.error || "connection failed"));
    } catch (e) {
      status.failed(e);
    }
  };

  const name = () => props.conn.label || props.conn.host || "this";
  const remove = async () => {
    if (!confirm(`Remove the ${name()} subscription?`)) return;
    try {
      await deleteServiceConnection(props.conn.id);
      props.onChanged();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="rounded border border-border p-3">
      <SubscriptionFields
        draft={draft}
        onChange={change}
        secretPlaceholder={
          props.conn.hasSecret
            ? `unchanged (••••${props.conn.secretSuffix ?? ""})`
            : "password (if needed)"
        }
        ariaPrefix={`Subscription ${props.conn.id}`}
      />
      <div class="mt-3 flex items-center gap-2">
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
          Test
        </Button>
        {/* Own Save button only when standalone; inside a SectionSave the
            page's one button drives this row. Test/Delete stay per-row. */}
        <Show when={!batched()}>
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
        </Show>
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void remove()}>
          Delete
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </div>
  );
};

// AddSubscriptionForm is the explicit Add flow. Create takes a PLAIN secret (a
// brand-new row has no stored one to preserve), so it does not go through
// buildServiceConnectionBody's three-state gate.
const AddSubscriptionForm: Component<{
  onAdded: () => void;
  onCancel: () => void;
}> = (props) => {
  const [draft, setDraft] = createSignal<SubscriptionDraft>({
    label: "",
    host: "",
    port: NNTP_DEFAULT_PORT,
    tls: true,
    username: "",
    secret: "",
    maxConns: 0,
    enabled: true,
  });
  const status = useSaveStatus();
  const change = (patch: Partial<SubscriptionDraft>) =>
    setDraft((d) => ({ ...d, ...patch }));

  const test = async () => {
    status.set("testing…");
    try {
      const r = await testServiceConnection({
        provider: "nntp",
        host: draft().host,
        port: draft().port,
        tls: draft().tls,
        username: draft().username,
        secret: draft().secret,
      });
      if (r.ok) status.set("✓ ok");
      else status.failed(new Error(r.error || "connection failed"));
    } catch (e) {
      status.failed(e);
    }
  };

  const add = async () => {
    if (!draft().host.trim()) {
      status.failed(new Error("host is required"));
      return;
    }
    try {
      await createServiceConnection({
        kind: "usenet",
        provider: "nntp",
        label: draft().label,
        enabled: draft().enabled,
        host: draft().host,
        port: draft().port,
        tls: draft().tls,
        maxConns: draft().maxConns,
        username: draft().username,
        secret: draft().secret,
      });
      props.onAdded();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded border border-dashed border-border p-3">
      <SubscriptionFields
        draft={draft}
        onChange={change}
        secretPlaceholder="password (if needed)"
        ariaPrefix="New subscription"
      />
      <div class="mt-3 flex items-center gap-2">
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
          Test
        </Button>
        <Button
          variant="primary"
          class="!px-2 !py-1 !text-xs"
          onClick={() => void add()}
        >
          Add subscription
        </Button>
        <Button class="!px-2 !py-1 !text-xs" onClick={() => props.onCancel()}>
          Cancel
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </div>
  );
};

const SubscriptionsCard: Component = () => {
  const [conns, { refetch }] = createResource(fetchServiceConnections);
  const [adding, setAdding] = createSignal(false);
  // The registry holds players too; this page owns only the usenet rows.
  const subscriptions = () =>
    (conns() ?? []).filter((c) => c.kind === "usenet");

  return (
    <Card title="Subscriptions">
      <Muted>
        Add one entry per Usenet provider. Every enabled subscription is tried
        for each download — there is no priority order, because providers
        differ in retention and takedown coverage rather than in rank.
      </Muted>
      <Show when={conns.error}>
        <ErrorText>{(conns.error as Error)?.message}</ErrorText>
      </Show>
      {/* Rows mount only AFTER the list resolves: each row seeds hasSecret from
          props.conn at mount, so mounting early would seed false and an
          untouched save would send secret:"" and WIPE the stored password —
          the same hazard ConnectionServiceTable documents. */}
      <Show when={!conns.error && conns() !== undefined}>
        <div class="mt-3 space-y-3">
          <For each={subscriptions()}>
            {(c) => (
              <SubscriptionRow conn={c} onChanged={() => void refetch()} />
            )}
          </For>
          <Show when={subscriptions().length === 0}>
            <Muted>No subscriptions configured yet.</Muted>
          </Show>
        </div>
      </Show>
      <Show
        when={adding()}
        fallback={
          <Button class="mt-3" onClick={() => setAdding(true)}>
            Add subscription
          </Button>
        }
      >
        <AddSubscriptionForm
          onAdded={() => {
            setAdding(false);
            void refetch();
          }}
          onCancel={() => setAdding(false)}
        />
      </Show>
    </Card>
  );
};

// AutoGrabCard is the UI surface of a deliberate staged-for-approval exception,
// so its copy says plainly that SAK will download without review.
//
// The single PUT this fires also sets the retry cadence server-side (on →
// 86400s, off → 0). Do not add a second request to the interval route: one
// DownloadsCard is the global NZB job concurrency cap. Deliberately separate
// from each subscription's Max connections: that knob is NNTP sockets per
// server; this one is how many NZBs may fetch segments at once. PAR2/repair
// does not count against this cap (server-enforced).
const DownloadsCard: Component = () => {
  const [maxConcurrentDownloads, setMaxConcurrentDownloads] = createSignal(1);
  const [dirty, setDirty] = createSignal(false);
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void fetchUsenetMaxConcurrentDownloads()
      .then((n) => setMaxConcurrentDownloads(n))
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const save = async () => {
    try {
      await putUsenetMaxConcurrentDownloads(maxConcurrentDownloads());
      setDirty(false);
      status.set("✓ saved");
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: "usenet-downloads",
    label: "downloads",
    dirty,
    valid: () => maxConcurrentDownloads() >= 1,
    save,
  });

  return (
    <Card title="Downloads">
      <label class="mb-3 block">
        <span class={labelClass}>Max concurrent downloads</span>
        <input
          type="number"
          min={1}
          class={`${inputClass} mt-1 !w-40`}
          aria-label="Max concurrent downloads"
          value={maxConcurrentDownloads()}
          disabled={loadError() !== null}
          onInput={(e) => {
            const n = Number(e.currentTarget.value);
            if (Number.isNaN(n)) return;
            setMaxConcurrentDownloads(n);
            setDirty(true);
          }}
        />
        <Muted class="mt-1">
          How many NZBs may download segments at once. At least 1. Does not
          count PAR2 repair or import — a download finishes its segment fetch,
          frees this slot, then repairs in the background. Separate from each
          subscription's Max connections (NNTP sockets). Applies immediately.
        </Muted>
      </label>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load downloads settings: {loadError()?.message}
        </ErrorText>
      </Show>
      <Show when={!batched()}>
        <div class="mt-3 flex items-center gap-2">
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            disabled={!dirty() || maxConcurrentDownloads() < 1}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
    </Card>
  );
};

const ResumeCard: Component = () => {
  const [enabled, setEnabled] = createSignal(true);
  const [forceFull, setForceFull] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void fetchUsenetSegmentResume()
      .then((r) => {
        setEnabled(r.enabled);
        setForceFull(r.forceFull);
      })
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const save = async () => {
    try {
      const r = await putUsenetSegmentResume({
        enabled: enabled(),
        forceFull: forceFull(),
      });
      setEnabled(r.enabled);
      setForceFull(r.forceFull);
      setDirty(false);
      status.set("✓ saved");
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: "usenet-segment-resume",
    label: "segment resume",
    dirty,
    save,
  });

  return (
    <Card title="Segment resume">
      <label class="mb-3 flex items-start gap-2">
        <input
          type="checkbox"
          class="mt-1"
          aria-label="Enable segment resume"
          checked={enabled()}
          disabled={loadError() !== null}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span>
          <span class={labelClass}>Resume completed segments after restart</span>
          <Muted class="mt-1">
            When on (default), sakms skips NNTP segments already written under
            each NZB staging dir (.sakms-resume.json). When off, every relaunch
            re-fetches the full article set.
          </Muted>
        </span>
      </label>
      <label class="mb-3 flex items-start gap-2">
        <input
          type="checkbox"
          class="mt-1"
          aria-label="Force full re-download"
          checked={forceFull()}
          disabled={loadError() !== null}
          onChange={(e) => {
            setForceFull(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span>
          <span class={labelClass}>Force full re-download</span>
          <Muted class="mt-1">
            One-shot: clears resume sidecars, cancels live usenet jobs, and
            wipes owned staging payloads, then turns itself off so later
            downloads can resume normally.
          </Muted>
        </span>
      </label>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load segment resume settings: {loadError()?.message}
        </ErrorText>
      </Show>
      <Show when={!batched()}>
        <div class="mt-3 flex items-center gap-2">
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            disabled={!dirty()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
    </Card>
  );
};

// source of truth is the whole point of coupling them there.
const AutoGrabCard: Component = () => {
  const [enabled, setEnabled] = createSignal(false);
  const [perCycle, setPerCycle] = createSignal(20);
  const [perSeries, setPerSeries] = createSignal(5);
  const [dirty, setDirty] = createSignal(false);
  // loadError is set when the initial GET fails. The route is registered
  // unconditionally (internal/api/handler.go), so this is never "the feature
  // isn't built yet" — it's a genuine fetch/network/server error, surfaced the
  // same way the sibling SubscriptionsCard shows conns.error. Scoped to THIS
  // card so a failure here never takes down the subscriptions list beside it,
  // and it leaves the toggle showing its honest default: off.
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void Promise.all([fetchUsenetAutoGrabEnabled(), fetchAutoGrabSlots("usenet")])
      .then(([on, slots]) => {
        setEnabled(on);
        setPerCycle(slots.perCycle);
        setPerSeries(slots.perSeries);
      })
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const save = async () => {
    try {
      await putUsenetAutoGrabEnabled(enabled());
      await putAutoGrabSlots("usenet", {
        perCycle: perCycle(),
        perSeries: perSeries(),
      });
      setDirty(false);
      status.set("✓ saved");
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: "usenet-autograb",
    label: "auto-grab",
    dirty,
    valid: () => autoGrabSlotsValid(perCycle(), perSeries(), 1),
    save,
  });

  return (
    <Card title="Auto-grab">
      <label class="mb-3 flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Enable auto-grab"
          checked={enabled()}
          disabled={loadError() !== null}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            setDirty(true);
          }}
        />
        <span class="text-sm text-fg">Enable auto-grab</span>
      </label>
      <Muted>
        When enabled, a Usenet request that produces a qualifying scored match is
        downloaded immediately, with no review step — SAK grabs it without asking
        you first. Leave this off to keep every grab operator-approved. A request
        with no qualifying match is never grabbed either way; it stays pending and
        re-searches every 24 hours once the retry loop is running. Toggling this on
        takes effect for new searches immediately, but the 24-hour retry loop itself
        only starts after the next restart.
      </Muted>
      <AutoGrabSlotFields
        protocol="Usenet"
        minPerCycle={1}
        disabled={loadError() !== null}
        perCycle={perCycle()}
        perSeries={perSeries()}
        onChange={(patch) => {
          if (patch.perCycle !== undefined) setPerCycle(patch.perCycle);
          if (patch.perSeries !== undefined) setPerSeries(patch.perSeries);
          setDirty(true);
        }}
      />
      <Muted class="mt-2">
        The nightly auto-grab pass searches oldest-air-date first. Cycle slots
        are the total searches; the per-series cap stops one classic backlog
        from taking every slot. Monitor-on still kicks an immediate search for
        that show.
      </Muted>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load the auto-grab setting: {loadError()?.message}. Auto-grab
          is off.
        </ErrorText>
      </Show>
      <Show when={!batched()}>
        <div class="mt-3 flex items-center gap-2">
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            disabled={!dirty()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
      <Show when={batched()}>
        <div class="mt-3">
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
    </Card>
  );
};

// Claude 2026-09-18: dual-list toolbox (unmonitored | arrows | monitored).
// Reason: operator moves groups between LIST ACTIVE leftovers and crawl set;
//   inputClass truncate must not wrap these lists (overflow:hidden).
// Troubleshooting: no scrollbar → check for truncate on the <select>.
// Review if: drag-drop transfer is added (arrows + dblclick stay).
const listBoxClass =
  "h-48 w-full overflow-y-auto rounded-md border border-border bg-bg px-2 py-1 font-mono text-xs text-fg outline-none focus:border-accent";

const GroupTransferToolbox: Component<{
  title: string;
  available: string[];
  monitored: string[];
  onChange: (monitored: string[]) => void;
}> = (props) => {
  const [leftSel, setLeftSel] = createSignal<string[]>([]);
  const [rightSel, setRightSel] = createSignal<string[]>([]);

  const monitoredSet = () => new Set(props.monitored);
  const unmonitored = () =>
    props.available
      .filter((g) => !monitoredSet().has(g))
      .slice()
      .sort((a, b) => a.localeCompare(b));
  const monitored = () =>
    props.monitored.slice().sort((a, b) => a.localeCompare(b));

  const readSel = (e: Event & { currentTarget: HTMLSelectElement }) =>
    Array.from(e.currentTarget.selectedOptions).map((o) => o.value);

  const addToMonitored = (names: string[]) => {
    if (names.length === 0) return;
    const next = new Set(props.monitored);
    for (const g of names) next.add(g);
    props.onChange([...next].sort((a, b) => a.localeCompare(b)));
    setLeftSel([]);
  };

  const removeFromMonitored = (names: string[]) => {
    if (names.length === 0) return;
    const drop = new Set(names);
    props.onChange(props.monitored.filter((g) => !drop.has(g)));
    setRightSel([]);
  };

  return (
    <div class="mb-4">
      <div class={labelClass + " mb-2"}>{props.title}</div>
      <div class="grid grid-cols-1 items-stretch gap-2 sm:grid-cols-[1fr_auto_1fr] sm:gap-3">
        <label class="block min-w-0">
          <span class={labelClass}>
            Unmonitored ({unmonitored().length})
          </span>
          <select
            multiple
            size={10}
            class={listBoxClass}
            aria-label={`${props.title} unmonitored newsgroups`}
            onChange={(e) => setLeftSel(readSel(e))}
            onDblClick={(e) => {
              const t = e.target as HTMLOptionElement;
              if (t?.tagName === "OPTION" && t.value) addToMonitored([t.value]);
            }}
          >
            <For each={unmonitored()}>
              {(g) => (
                <option value={g} selected={leftSel().includes(g)}>
                  {g}
                </option>
              )}
            </For>
          </select>
        </label>
        <div class="flex flex-row justify-center gap-1 sm:flex-col sm:justify-center">
          <Button
            class="!px-2 !py-1 !text-xs"
            disabled={leftSel().length === 0}
            title="Monitor selected"
            aria-label={`${props.title} monitor selected`}
            onClick={() => addToMonitored(leftSel())}
          >
            &gt;
          </Button>
          <Button
            class="!px-2 !py-1 !text-xs"
            disabled={unmonitored().length === 0}
            title="Monitor all"
            aria-label={`${props.title} monitor all`}
            onClick={() => addToMonitored(unmonitored())}
          >
            &gt;&gt;
          </Button>
          <Button
            class="!px-2 !py-1 !text-xs"
            disabled={rightSel().length === 0}
            title="Unmonitor selected"
            aria-label={`${props.title} unmonitor selected`}
            onClick={() => removeFromMonitored(rightSel())}
          >
            &lt;
          </Button>
          <Button
            class="!px-2 !py-1 !text-xs"
            disabled={monitored().length === 0}
            title="Unmonitor all"
            aria-label={`${props.title} unmonitor all`}
            onClick={() => removeFromMonitored(monitored())}
          >
            &lt;&lt;
          </Button>
        </div>
        <label class="block min-w-0">
          <span class={labelClass}>Monitored ({monitored().length})</span>
          <select
            multiple
            size={10}
            class={listBoxClass}
            aria-label={`${props.title} monitored newsgroups`}
            onChange={(e) => setRightSel(readSel(e))}
            onDblClick={(e) => {
              const t = e.target as HTMLOptionElement;
              if (t?.tagName === "OPTION" && t.value)
                removeFromMonitored([t.value]);
            }}
          >
            <For each={monitored()}>
              {(g) => (
                <option value={g} selected={rightSel().includes(g)}>
                  {g}
                </option>
              )}
            </For>
          </select>
        </label>
      </div>
    </div>
  );
};

const NativeSearchCard: Component = () => {
  const [enabled, setEnabled] = createSignal(false);
  const [movies, setMovies] = createSignal(false);
  const [series, setSeries] = createSignal(false);
  const [adult, setAdult] = createSignal(false);
  const [moviesGroups, setMoviesGroups] = createSignal<string[]>([]);
  const [seriesGroups, setSeriesGroups] = createSignal<string[]>([]);
  const [adultGroups, setAdultGroups] = createSignal<string[]>([]);
  const [availMovies, setAvailMovies] = createSignal<string[]>([]);
  const [availSeries, setAvailSeries] = createSignal<string[]>([]);
  const [availAdult, setAvailAdult] = createSignal<string[]>([]);
  const [groupsError, setGroupsError] = createSignal("");
  const [indexDir, setIndexDir] = createSignal("");
  const [indexMaxGb, setIndexMaxGb] = createSignal(20);
  const [windowDays, setWindowDays] = createSignal(14);
  const [crawlInterval, setCrawlInterval] = createSignal(0);
  const [probeState, setProbeState] = createSignal("unknown");
  const [probeDetail, setProbeDetail] = createSignal("");
  const [dirty, setDirty] = createSignal(false);
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  const status = useSaveStatus();

  const parseStored = (raw: string | undefined) =>
    (raw ?? "")
      .split(/[\n,]+/)
      .map((s) => s.trim())
      .filter(Boolean);

  const apply = (r: UsenetNNTPNativeSettings) => {
    setEnabled(r.enabled);
    setMovies(r.movies);
    setSeries(r.series);
    setAdult(r.adult);
    setMoviesGroups(parseStored(r.moviesGroups));
    setSeriesGroups(parseStored(r.seriesGroups));
    setAdultGroups(parseStored(r.adultGroups));
    setIndexDir(r.indexDir ?? "");
    setIndexMaxGb(r.indexMaxGb || 20);
    setWindowDays(r.windowDays || 14);
    setCrawlInterval(r.crawlIntervalSeconds || 0);
    setProbeState(r.probeState || "unknown");
    setProbeDetail(r.probeDetail || "");
  };

  const loadAvailable = async () => {
    setGroupsError("");
    try {
      const [m, s, a] = await Promise.all([
        fetchUsenetNNTPGroups("movies"),
        fetchUsenetNNTPGroups("series"),
        fetchUsenetNNTPGroups("adult"),
      ]);
      setAvailMovies(m.groups ?? []);
      setAvailSeries(s.groups ?? []);
      setAvailAdult(a.groups ?? []);
    } catch (e) {
      setGroupsError(e instanceof Error ? e.message : String(e));
    }
  };

  onMount(() => {
    void fetchUsenetNNTPNative()
      .then(async (r) => {
        apply(r);
        await loadAvailable();
      })
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const valid = () => {
    if (!enabled()) return true;
    if (movies() && moviesGroups().length === 0) return false;
    if (series() && seriesGroups().length === 0) return false;
    if (adult() && adultGroups().length === 0) return false;
    return indexMaxGb() > 0 && windowDays() > 0 && crawlInterval() >= 0;
  };

  const save = async () => {
    try {
      const r = await putUsenetNNTPNative({
        enabled: enabled(),
        movies: movies(),
        series: series(),
        adult: adult(),
        moviesGroups: moviesGroups().join("\n"),
        seriesGroups: seriesGroups().join("\n"),
        adultGroups: adultGroups().join("\n"),
        indexDir: indexDir().trim(),
        indexMaxGb: indexMaxGb(),
        windowDays: windowDays(),
        crawlIntervalSeconds: crawlInterval(),
      });
      apply(r);
      setDirty(false);
      status.set("✓ saved");
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: "usenet-nntp-native",
    label: "native search",
    dirty,
    valid,
    save,
  });

  const mark = () => {
    setDirty(true);
    status.set("");
  };

  const setGroups = (
    setter: (v: string[]) => void,
    groups: string[],
  ) => {
    setter(groups);
    mark();
  };

  return (
    <Card title="Native NNTP search">
      <Muted class="mb-3">
        Optional built-in Usenet discovery: crawl only the newsgroups you select
        per mode, store headers in the SAK Postgres DB, and search that index
        before Prowlarr. Off by default. Does not replace NZB/Prowlarr — they
        remain the fallback. Obfuscated posts are not discoverable from headers
        alone.
      </Muted>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load native search settings: {loadError()?.message}
        </ErrorText>
      </Show>
      <label class="mb-3 flex items-center gap-2">
        <input
          type="checkbox"
          checked={enabled()}
          onChange={(e) => {
            setEnabled(e.currentTarget.checked);
            mark();
            if (e.currentTarget.checked) void loadAvailable();
          }}
        />
        <span>Enable native NNTP search</span>
      </label>
      <div class="mb-3 flex flex-wrap gap-4">
        <label class="flex items-center gap-2">
          <input
            type="checkbox"
            checked={movies()}
            onChange={(e) => {
              setMovies(e.currentTarget.checked);
              mark();
            }}
          />
          Movies
        </label>
        <label class="flex items-center gap-2">
          <input
            type="checkbox"
            checked={series()}
            onChange={(e) => {
              setSeries(e.currentTarget.checked);
              mark();
            }}
          />
          Series
        </label>
        <label class="flex items-center gap-2">
          <input
            type="checkbox"
            checked={adult()}
            onChange={(e) => {
              setAdult(e.currentTarget.checked);
              mark();
            }}
          />
          Adult
        </label>
      </div>
      <Show when={groupsError()}>
        <ErrorText>
          Couldn't load available newsgroups: {groupsError()}
        </ErrorText>
      </Show>
      <GroupTransferToolbox
        title="Movies"
        available={availMovies()}
        monitored={moviesGroups()}
        onChange={(g) => setGroups(setMoviesGroups, g)}
      />
      <GroupTransferToolbox
        title="Series"
        available={availSeries()}
        monitored={seriesGroups()}
        onChange={(g) => setGroups(setSeriesGroups, g)}
      />
      <GroupTransferToolbox
        title="Adult"
        available={availAdult()}
        monitored={adultGroups()}
        onChange={(g) => setGroups(setAdultGroups, g)}
      />
      <Muted class="mb-3">
        Unmonitored = available from the provider (LIST ACTIVE). Monitored =
        crawled and searched for that mode. Use the arrows (or double-click a
        group) to move. Crawl indexes the union of all monitored groups; search
        uses only the active mode's monitored list.
      </Muted>
      <label class="mb-3 block">
        <span class={labelClass}>Index directory (unused — index is in the SAK Postgres DB)</span>
        <input
          type="text"
          class={inputClass + " font-mono text-sm"}
          aria-label="native search index directory"
          value={indexDir()}
          disabled
          title="Header index uses dedicated Postgres tables after the SQLite cutover"
        />
      </label>
      <div class="mb-3 grid gap-3 sm:grid-cols-3">
        <label class="block">
          <span class={labelClass}>Max index size (GiB)</span>
          <input
            type="number"
            class={inputClass}
            value={indexMaxGb()}
            onInput={(e) => {
              setIndexMaxGb(Number(e.currentTarget.value) || 0);
              mark();
            }}
          />
        </label>
        <label class="block">
          <span class={labelClass}>Retention window (days)</span>
          <input
            type="number"
            class={inputClass}
            value={windowDays()}
            onInput={(e) => {
              setWindowDays(Number(e.currentTarget.value) || 0);
              mark();
            }}
          />
        </label>
        <label class="block">
          <span class={labelClass}>Crawl interval (seconds, 0=off)</span>
          <input
            type="number"
            class={inputClass}
            value={crawlInterval()}
            onInput={(e) => {
              setCrawlInterval(Number(e.currentTarget.value) || 0);
              mark();
            }}
          />
        </label>
      </div>
      <Muted>
        Probe: {probeState()}
        {probeDetail() ? ` — ${probeDetail()}` : ""}
      </Muted>
      <Show when={!batched()}>
        <div class="mt-3 flex items-center gap-2">
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            disabled={!dirty() || !valid()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
      <Show when={batched()}>
        <div class="mt-3">
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </Show>
    </Card>
  );
};
