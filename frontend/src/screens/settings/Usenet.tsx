// Usenet — a sub-tab of the Settings > Download tab (see Download.tsx), not a
// row in a connections table.
//
// It has its own page precisely because a Usenet subscription's field set is
// richer than the URL + API-key shape ConnectionRow renders: label, host, port,
// TLS, username, password, max-connections. NNTP rows do not reuse
// ConnectionRow. NZB host API is a stacked URL+key card on this page, not a
// ConnectionMiniTable.
//
// Three things this page must never grow:
//   - Priority / fallback ordering between subscriptions. When a download needs
//     Usenet, every enabled subscription is tried, with no priority ordering at
//     all — an operator runs several providers for differing retention and
//     takedown coverage, not as a ranked chain. There is no priority field and
//     no reorder UI beyond cosmetic sorting.
//   - A generic "add any connection type" framework. This page adds NNTP
//     subscriptions and optional Newznab hosts (Usenet grab catalog). Nothing else.
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
  buildConnectionUpsertBody,
  deleteConnection,
  fetchConnections,
  testConnection,
  upsertConnection,
  type ConnectionSummary,
} from "../../api/settings";
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
  fetchUsenetSegmentResume,
  putUsenetAutoGrabEnabled,
  putUsenetMaxConcurrentDownloads,
  putUsenetSegmentResume,
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
    <NewznabHostCard />
  </div>
);

// Claude 2026-10-23: stacked URL+key card for Newznab grab catalog.
// Reason: native OVER index removed; this host is the Usenet grab indexer
//   before Prowlarr. Passwords are not recoverable from NZBGeek.
// Troubleshooting: operator filled URL+key, no Save visible, live connections had 0 newznab rows.
// Review if: Prowlarr already includes the same indexer.
// Related files: internal/api/newznabgrab.go
//
// Claude 2026-10-22: second host `newznab2` for an indexer that lists obfuscated NZBs.
// Reason: NZBGeek does not list ngPost hash NZBs. Paste URL+key only — no secret in git.
//   NZBFinder is the recommended second host (real email; films first).
// Troubleshooting: grab never queries host 2 → connections.service newznab2 empty.
// Review if: Prowlarr already includes both hosts.
// Claude 2026-10-23: both hosts are queried in parallel, not host-1-then-2.
// Reason: sequential t=search hid NZBFinder behind NZBGeek. Prowlarr stays last.
const NewznabHostCard: Component = () => {
  const [conns, { refetch }] = createResource(fetchConnections);
  return (
    <>
      <NewznabHostPanel
        service="newznab"
        saveId="usenet-newznab"
        title="NZB host API"
        urlAria="newznab URL"
        keyAria="newznab API key"
        help="Optional Newznab indexer for Usenet grab search. Queried in parallel with host 2, before Prowlarr. This is the grab catalog. Not used for torrents. Example: https://api.nzbgeek.info/api"
        rows={conns}
        rowsError={conns.error}
        onChanged={() => void refetch()}
      />
      <NewznabHostPanel
        service="newznab2"
        saveId="usenet-newznab2"
        title="NZB host API 2"
        urlAria="newznab2 URL"
        keyAria="newznab2 API key"
        help="Optional second Newznab indexer (NZBFinder recommended: https://nzbfinder.ws/api). Sign up with a real email, then paste URL and API key. Films first. Queried in parallel with host 1, before Prowlarr. Nothing is stored in the repo."
        rows={conns}
        rowsError={conns.error}
        onChanged={() => void refetch()}
      />
    </>
  );
};

const NewznabHostPanel: Component<{
  service: string;
  saveId: string;
  title: string;
  urlAria: string;
  keyAria: string;
  help: string;
  rows: () => ConnectionSummary[] | undefined;
  rowsError: unknown;
  onChanged: () => void;
}> = (props) => {
  const existing = () =>
    (props.rows() ?? []).find((c) => c.service === props.service);
  return (
    <Card title={props.title}>
      <Muted class="mb-3">{props.help}</Muted>
      <Show when={props.rowsError}>
        <ErrorText>{(props.rowsError as Error)?.message}</ErrorText>
      </Show>
      <Show when={!props.rowsError && props.rows() !== undefined}>
        <NewznabHostFields
          service={props.service}
          saveId={props.saveId}
          urlAria={props.urlAria}
          keyAria={props.keyAria}
          existing={existing()}
          onChanged={props.onChanged}
        />
      </Show>
    </Card>
  );
};

const NewznabHostFields: Component<{
  service: string;
  saveId: string;
  urlAria: string;
  keyAria: string;
  existing: ConnectionSummary | undefined;
  onChanged: () => void;
}> = (props) => {
  const [url, setUrl] = createSignal(props.existing?.url ?? "");
  const [key, setKey] = createSignal("");
  const [keyTouched, setKeyTouched] = createSignal(false);
  const [dirty, setDirty] = createSignal(false);
  const status = useSaveStatus();

  const hasExistingKey = () => !!props.existing?.hasApiKey;
  const body = () =>
    buildConnectionUpsertBody({
      url: url(),
      needsUsername: false,
      keyTouched: keyTouched(),
      keyValue: key(),
      hasExistingKey: hasExistingKey(),
    });

  const save = async () => {
    if (!url().trim()) {
      const err = new Error("url is required");
      status.failed(err);
      throw err;
    }
    try {
      await upsertConnection(props.service, body());
      status.set("✓ saved");
      setKey("");
      setKeyTouched(false);
      setDirty(false);
      props.onChanged();
    } catch (e) {
      status.failed(e);
      throw e;
    }
  };

  const batched = useSectionSaveItem({
    id: props.saveId,
    label: props.service === "newznab2" ? "NZB host API 2" : "NZB host API",
    dirty,
    save,
  });

  const test = async () => {
    status.set("testing…");
    try {
      const b = body();
      const r = await testConnection({
        service: props.service,
        url: b.url,
        username: b.username,
        apiKey: b.apiKey,
      });
      if (r.ok) status.set("✓ ok");
      else status.failed(new Error(r.error || "connection failed"));
    } catch (e) {
      status.failed(e);
    }
  };

  const remove = async () => {
    if (!confirm("Remove this NZB host API connection?")) return;
    try {
      await deleteConnection(props.service);
      setUrl("");
      setKey("");
      setKeyTouched(false);
      setDirty(false);
      props.onChanged();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div>
      <label class="mb-3 block">
        <span class={labelClass}>URL</span>
        <input
          type="text"
          class={`${inputClass} mt-1`}
          placeholder="https://api.example.com/api"
          aria-label={props.urlAria}
          value={url()}
          onInput={(e) => {
            setUrl(e.currentTarget.value);
            setDirty(true);
          }}
        />
      </label>
      <label class="mb-3 block">
        <span class={labelClass}>API key</span>
        <input
          type="password"
          class={`${inputClass} mt-1`}
          placeholder={
            hasExistingKey()
              ? `unchanged (••••${props.existing?.keySuffix ?? ""})`
              : "api key"
          }
          aria-label={props.keyAria}
          value={key()}
          onInput={(e) => {
            setKey(e.currentTarget.value);
            setKeyTouched(true);
            setDirty(true);
          }}
        />
      </label>
      <div class="mt-3 flex items-center gap-2">
        <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
          Test
        </Button>
        <Show when={!batched()}>
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            disabled={!dirty()}
            onClick={() => void save().catch(() => {})}
          >
            Save
          </Button>
        </Show>
        <Button
          class="!px-2 !py-1 !text-xs"
          disabled={!props.existing}
          onClick={() => void remove()}
        >
          Delete
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </div>
  );
};

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
