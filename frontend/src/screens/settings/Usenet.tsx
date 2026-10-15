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
  createMemo,
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
  type UsenetNNTPAvailableGroups,
  type UsenetNNTPGroupItem,
  type UsenetNNTPNativeSettings,
} from "../../api/usenet";
import { fetchAutoGrabSlots, putAutoGrabSlots } from "../../api/autograbSlots";
import { calibrateHardware, fetchEtaPriors, type EtaPriors } from "../../api/downloads";
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
  autoGrabSlotsValid,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

// Claude 2026-09-24: each Usenet card saves itself; no page-level SectionSave.
// Reason: Settings screens are immediate-apply / per-card Save.
// Troubleshooting: one Save for the whole Download page → leftover wrapper.
// Review if: subscriptions + auto-grab need a single commit again.
export const UsenetSection: Component = () => (
  <div>
    <SubscriptionsCard />
    <DownloadsCard />
    <ResumeCard />
    <AutoGrabCard />
    <NativeSearchCard />
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
  const [etaPriors, setEtaPriors] = createSignal<EtaPriors | null>(null);
  const [calibrating, setCalibrating] = createSignal(false);
  const [calibrateError, setCalibrateError] = createSignal<string | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void fetchUsenetMaxConcurrentDownloads()
      .then((n) => setMaxConcurrentDownloads(n))
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
    void fetchEtaPriors()
      .then((p) => setEtaPriors(p))
      .catch(() => {
        /* card still usable without live priors */
      });
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
      {/* Claude 2026-09-21: Recalibrate is an immediate action, not SectionSave.
          Reason: same POST as the Downloads banner; must not wait on dirty
          concurrency fields. Live bps may include in-process EMA until restart.
          Troubleshooting: Recalibrate disabled because Max concurrent is dirty.
          Review if: hardware priors become a typed settings struct or the engine
          emits its own ETA. */}
      <div class="mt-4 border-t border-border pt-3">
        <Muted>
          {etaPriors()?.calibrated
            ? `Calibrated ${formatCalibratedAt(etaPriors()!.calibratedAt)}. Live repair ${formatMibps(etaPriors()!.repairBps)}, unpack ${formatMibps(etaPriors()!.unpackBps)}. Session samples may refine until restart.`
            : `Hardware not measured yet. ETAs use compiled 25/40 MiB/s until you run the bench. Live repair ${formatMibps(etaPriors()?.repairBps ?? 25 * 1024 * 1024)}, unpack ${formatMibps(etaPriors()?.unpackBps ?? 40 * 1024 * 1024)}.`}
        </Muted>
        <Muted class="mt-1">Results may vary under load.</Muted>
        <div class="mt-2 flex items-center gap-2">
          <Button
            variant="secondary"
            class="!px-2 !py-1 !text-xs"
            disabled={calibrating()}
            onClick={() => {
              setCalibrateError(null);
              setCalibrating(true);
              void calibrateHardware()
                .then((p) => setEtaPriors(p))
                .catch((e) =>
                  setCalibrateError(e instanceof Error ? e.message : String(e)),
                )
                .finally(() => setCalibrating(false));
            }}
          >
            {calibrating() ? "Measuring…" : "Recalibrate hardware"}
          </Button>
        </div>
        <Show when={calibrateError()}>
          <ErrorText>{calibrateError()}</ErrorText>
        </Show>
      </div>
    </Card>
  );
};

function formatMibps(bps: number): string {
  if (!Number.isFinite(bps) || bps <= 0) return "—";
  return `${(bps / (1024 * 1024)).toFixed(1)} MiB/s`;
}

function formatCalibratedAt(rfc: string): string {
  if (!rfc) return "unknown time";
  const d = new Date(rfc);
  if (Number.isNaN(d.getTime())) return rfc;
  return d.toLocaleString();
}

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

// Claude 2026-10-15: searchable dual-list + Apply recommended (was raw LIST ACTIVE).
// Reason: full provider lists are daunting; curated seeds + filter, Save still required.
// Troubleshooting: recommended empty → provider lacks seed groups; check wildmats.
// Review if: catalog cache adds perDay/usablePct columns beside the name.
// Related files: internal/usenetsearch/seeds/, usenetnntp.go groups API
const listBoxClass =
  "h-48 w-full overflow-y-auto rounded-md border border-border bg-bg px-2 py-1 font-mono text-xs text-fg outline-none focus:border-accent";

// Claude 2026-10-15: resetLabel/onReset replaces hard-coded "Apply recommended"
// so the advanced-collapse context can say "Reset to current step" instead.
// Reason: slider step is the source of truth; custom edits branch to package="custom".
// Troubleshooting: onReset undefined → button hidden; resetLabel controls copy.
// Review if: a third reset variant is needed.
const GroupTransferToolbox: Component<{
  title: string;
  items: UsenetNNTPGroupItem[];
  recommended: UsenetNNTPGroupItem[];
  monitored: string[];
  onChange: (monitored: string[]) => void;
  resetLabel?: string;
  onReset?: () => void;
}> = (props) => {
  const [leftSel, setLeftSel] = createSignal<string[]>([]);
  const [rightSel, setRightSel] = createSignal<string[]>([]);
  const [query, setQuery] = createSignal("");

  const monitoredSet = () => new Set(props.monitored);
  const itemByName = createMemo(() => {
    const m = new Map<string, UsenetNNTPGroupItem>();
    for (const it of props.items) m.set(it.name, it);
    return m;
  });
  const matches = (name: string) => {
    const needle = query().trim().toLowerCase();
    if (!needle) return true;
    if (name.toLowerCase().includes(needle)) return true;
    return !!itemByName().get(name)?.note?.toLowerCase().includes(needle);
  };
  const recommendedFirst = (g: string) =>
    itemByName().get(g)?.recommended ? 0 : 1;
  const unmonitored = () =>
    props.items
      .map((it) => it.name)
      .filter((g) => !monitoredSet().has(g) && matches(g))
      .sort(
        (a, b) =>
          recommendedFirst(a) - recommendedFirst(b) || a.localeCompare(b),
      );
  const monitored = () =>
    props.monitored.filter(matches).sort((a, b) => a.localeCompare(b));

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

  const labelFor = (g: string) => {
    const it = itemByName().get(g);
    if (!it?.recommended) return g;
    return it.note ? `★ ${g} — ${it.note}` : `★ ${g}`;
  };

  return (
    <div class="mb-4">
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div class={labelClass}>{props.title}</div>
        <Show when={props.onReset}>
          <Button
            class="!px-2 !py-1 !text-xs"
            title={props.resetLabel ?? "Reset"}
            aria-label={`${props.title} ${props.resetLabel ?? "reset"}`}
            onClick={() => props.onReset?.()}
          >
            {props.resetLabel ?? "Reset"}
          </Button>
        </Show>
      </div>
      <label class="mb-2 block min-w-0">
        <span class={labelClass}>Filter</span>
        <input
          type="search"
          class={inputClass + " font-mono text-sm"}
          placeholder="Search newsgroups…"
          aria-label={`${props.title} filter newsgroups`}
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
        />
      </label>
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
                  {labelFor(g)}
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
            title="Monitor all filtered"
            aria-label={`${props.title} monitor all filtered`}
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
            title="Unmonitor all filtered"
            aria-label={`${props.title} unmonitor all filtered`}
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
                  {labelFor(g)}
                </option>
              )}
            </For>
          </select>
        </label>
      </div>
    </div>
  );
};

// Claude 2026-10-15: groupsFromPackageStep returns names for steps 1..N from
// the packages map, falling back to items filtered by step<=N.
// Reason: slider preview must match what the server would expand on Save.
// Troubleshooting: packages undefined → falls back to items step filter.
// Review if: server changes package expansion logic.
function groupsFromPackageStep(
  step: number,
  avail: UsenetNNTPAvailableGroups,
): string[] {
  if (avail.packages) {
    const names = new Set<string>();
    for (let s = 1; s <= step; s++) {
      const key = String(s);
      for (const it of avail.packages[key] ?? []) {
        if (it.name) names.add(it.name);
      }
    }
    return [...names].sort((a, b) => a.localeCompare(b));
  }
  // Fallback: use items annotated with step<=N.
  return (avail.items ?? [])
    .filter((it) => it.step !== undefined && it.step <= step)
    .map((it) => it.name)
    .sort((a, b) => a.localeCompare(b));
}

// PackageSliderPanel renders the step slider + optional custom chip + advanced
// collapse for one mode (movies/series/adult).
//
// Claude 2026-10-15: per-mode slider panel replacing always-visible toolbox.
// Reason: slider is the primary UX; toolbox is advanced/expert territory.
// Troubleshooting: steps undefined → cumulative count hidden (server older build).
// Review if: step count changes from 5 or steps shape changes.
const STEP_MAX = 5;

const PackageSliderPanel: Component<{
  title: string;
  pkg: string;
  steps?: UsenetNNTPAvailableGroups["steps"];
  avail: UsenetNNTPAvailableGroups;
  monitored: string[];
  onPackageChange: (pkg: string, groups: string[]) => void;
  onGroupsChange: (groups: string[]) => void;
}> = (props) => {
  const [advOpen, setAdvOpen] = createSignal(false);

  const currentStep = () => {
    const n = parseInt(props.pkg, 10);
    return Number.isFinite(n) && n >= 1 && n <= STEP_MAX ? n : 1;
  };

  const isCustom = () => props.pkg === "custom";

  const cumulativeCount = () => {
    if (!props.steps || isCustom()) return null;
    const step = String(currentStep());
    return props.steps[step]?.cumulativeCount ?? null;
  };

  const applyStep = (step: number) => {
    const groups = groupsFromPackageStep(step, props.avail);
    props.onPackageChange(String(step), groups);
  };

  const resetToStep = () => applyStep(currentStep());

  const stepLabels: Record<number, string> = {
    1: "Most reliable",
    2: "High coverage",
    3: "Broad coverage",
    4: "Extended",
    5: "Largest selection",
  };

  return (
    <div class="mb-4 rounded border border-border p-3">
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
        <span class={labelClass}>{props.title}</span>
        <Show when={isCustom()}>
          <span class="rounded bg-accent/10 px-2 py-0.5 text-xs font-medium text-accent">
            Custom
          </span>
        </Show>
      </div>
      <Show when={!isCustom()}>
        <div class="mb-3">
          <div class="flex items-center justify-between text-xs text-muted">
            <span>Most reliable</span>
            <span>Largest selection</span>
          </div>
          <input
            type="range"
            min={1}
            max={STEP_MAX}
            step={1}
            class="mt-1 w-full accent-accent"
            aria-label={`${props.title} package step`}
            value={currentStep()}
            onInput={(e) => applyStep(Number(e.currentTarget.value))}
          />
          <div class="mt-1 flex items-center gap-2 text-xs text-muted">
            <span>
              Step {currentStep()} — {stepLabels[currentStep()] ?? ""}
            </span>
            <Show when={cumulativeCount() !== null}>
              <span class="text-fg">
                ({cumulativeCount()} group{cumulativeCount() === 1 ? "" : "s"})
              </span>
            </Show>
          </div>
        </div>
      </Show>
      <Show when={isCustom()}>
        <div class="mb-3 flex items-center gap-2">
          <Muted>
            Groups edited manually. Slider disabled while custom.
          </Muted>
          <Button
            class="!px-2 !py-1 !text-xs"
            onClick={() => applyStep(1)}
          >
            Reset to step 1
          </Button>
        </div>
      </Show>
      <details
        open={advOpen()}
        onToggle={(e) => setAdvOpen((e.currentTarget as HTMLDetailsElement).open)}
      >
        <summary class="cursor-pointer select-none text-xs text-muted hover:text-fg">
          Customize groups
        </summary>
        <div class="mt-3">
          <GroupTransferToolbox
            title={props.title}
            items={props.avail.items ?? []}
            recommended={props.avail.recommended ?? []}
            monitored={props.monitored}
            onChange={(groups) => {
              props.onGroupsChange(groups);
            }}
            resetLabel={`Reset to step ${currentStep()}`}
            onReset={resetToStep}
          />
        </div>
      </details>
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
  // Claude 2026-10-15: package signals for the 5-step slider.
  // Reason: "1".."5" drives server-side group expansion; "custom" = manual list.
  // Troubleshooting: package undefined from older server → default to "1".
  // Review if: step count or package key format changes.
  const [moviesPackage, setMoviesPackage] = createSignal("1");
  const [seriesPackage, setSeriesPackage] = createSignal("1");
  const [adultPackage, setAdultPackage] = createSignal("1");
  const [availMovies, setAvailMovies] = createSignal<UsenetNNTPAvailableGroups>({ mode: "movies", groups: [] });
  const [availSeries, setAvailSeries] = createSignal<UsenetNNTPAvailableGroups>({ mode: "series", groups: [] });
  const [availAdult, setAvailAdult] = createSignal<UsenetNNTPAvailableGroups>({ mode: "adult", groups: [] });
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
    setMoviesPackage(r.moviesPackage ?? "1");
    setSeriesPackage(r.seriesPackage ?? "1");
    setAdultPackage(r.adultPackage ?? "1");
    setIndexDir(r.indexDir ?? "");
    setIndexMaxGb(r.indexMaxGb || 20);
    setWindowDays(r.windowDays || 14);
    setCrawlInterval(r.crawlIntervalSeconds || 0);
    setProbeState(r.probeState || "unknown");
    setProbeDetail(r.probeDetail || "");
  };

  // Claude 2026-10-15: first-run prefill for package="1" with empty groups.
  // Reason: cold-start UX — draft step-1 groups unsaved so Save is still required.
  // Troubleshooting: dirty after load with empty groups = expected on first visit.
  // Review if: wizard step sets groups before Settings opens.
  const prefillStepIfEmpty = (
    pkg: string,
    current: string[],
    setter: (v: string[]) => void,
    avail: UsenetNNTPAvailableGroups,
  ): boolean => {
    if (current.length > 0) return false;
    const step = parseInt(pkg, 10);
    const stepNum = Number.isFinite(step) && step >= 1 && step <= STEP_MAX ? step : 1;
    const groups = groupsFromPackageStep(stepNum, avail);
    if (groups.length === 0) return false;
    setter(groups);
    return true;
  };

  const loadAvailable = async (opts?: { prefillEmpty?: boolean }) => {
    setGroupsError("");
    try {
      const [m, s, a] = await Promise.all([
        fetchUsenetNNTPGroups("movies"),
        fetchUsenetNNTPGroups("series"),
        fetchUsenetNNTPGroups("adult"),
      ]);
      setAvailMovies(m);
      setAvailSeries(s);
      setAvailAdult(a);
      if (opts?.prefillEmpty) {
        // Array literal prevents short-circuit so all three modes are checked.
        const filled = [
          prefillStepIfEmpty(moviesPackage(), moviesGroups(), setMoviesGroups, m),
          prefillStepIfEmpty(seriesPackage(), seriesGroups(), setSeriesGroups, s),
          prefillStepIfEmpty(adultPackage(), adultGroups(), setAdultGroups, a),
        ].includes(true);
        if (filled) {
          setDirty(true);
          status.set("Most reliable package pre-selected — Save to apply");
        }
      }
    } catch (e) {
      setGroupsError(e instanceof Error ? e.message : String(e));
    }
  };

  onMount(() => {
    void fetchUsenetNNTPNative()
      .then(async (r) => {
        apply(r);
        await loadAvailable({ prefillEmpty: true });
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
        moviesPackage: moviesPackage(),
        seriesPackage: seriesPackage(),
        adultPackage: adultPackage(),
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

  const handlePackageChange = (
    setPkg: (v: string) => void,
    setGrp: (v: string[]) => void,
    pkg: string,
    groups: string[],
  ) => {
    setPkg(pkg);
    setGrp(groups);
    mark();
  };

  const handleGroupsChange = (
    setPkg: (v: string) => void,
    setGrp: (v: string[]) => void,
    groups: string[],
  ) => {
    setPkg("custom");
    setGrp(groups);
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
      {/* Claude 2026-10-15: per-mode panels shown only when mode toggle is on.
          Reason: slider + advanced collapse replaces always-visible toolboxes.
          Troubleshooting: panel hidden despite mode on → check movies()/series()/adult() signal.
          Review if: all-mode single slider is preferred over per-mode. */}
      <Show when={movies()}>
        <PackageSliderPanel
          title="Movies"
          pkg={moviesPackage()}
          steps={availMovies().steps}
          avail={availMovies()}
          monitored={moviesGroups()}
          onPackageChange={(pkg, groups) =>
            handlePackageChange(setMoviesPackage, setMoviesGroups, pkg, groups)
          }
          onGroupsChange={(groups) =>
            handleGroupsChange(setMoviesPackage, setMoviesGroups, groups)
          }
        />
      </Show>
      <Show when={series()}>
        <PackageSliderPanel
          title="Series"
          pkg={seriesPackage()}
          steps={availSeries().steps}
          avail={availSeries()}
          monitored={seriesGroups()}
          onPackageChange={(pkg, groups) =>
            handlePackageChange(setSeriesPackage, setSeriesGroups, pkg, groups)
          }
          onGroupsChange={(groups) =>
            handleGroupsChange(setSeriesPackage, setSeriesGroups, groups)
          }
        />
      </Show>
      <Show when={adult()}>
        <PackageSliderPanel
          title="Adult"
          pkg={adultPackage()}
          steps={availAdult().steps}
          avail={availAdult()}
          monitored={adultGroups()}
          onPackageChange={(pkg, groups) =>
            handlePackageChange(setAdultPackage, setAdultGroups, pkg, groups)
          }
          onGroupsChange={(groups) =>
            handleGroupsChange(setAdultPackage, setAdultGroups, groups)
          }
        />
      </Show>
      <Muted class="mb-3">
        Use the slider to pick a coverage level from "Most reliable" (step 1 —
        fewest groups, highest completion) to "Largest selection" (step 5 — most
        groups). Expand "Customize groups" to edit the list manually; doing so
        switches that mode to Custom and disables the slider until you reset it.
        Crawl indexes the union of monitored groups; search uses only the active
        mode&apos;s list.
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
          <span class={labelClass}>Soft age budget (days)</span>
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
          <span class={labelClass}>Idle backoff (seconds, 0=default 60)</span>
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
      <Muted class="mb-2">
        When enabled, the index crawls continuously (rate-limited) and STATs
        headers for liveness — articles are removed only when unavailable. Soft
        age/size budgets never delete still-available headers. Idle backoff is
        the pause after catch-up.
      </Muted>
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
