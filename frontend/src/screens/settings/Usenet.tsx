// Usenet — its own top-level Settings tab, not a row in a connections table.
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
  putUsenetAutoGrabEnabled,
} from "../../api/usenet";
import {
  Button,
  ErrorText,
  Muted,
  inputClass,
  labelClass,
} from "../../components/ui";
import {
  Card,
  SaveStatus,
  SectionSave,
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
      <AutoGrabCard />
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
// source of truth is the whole point of coupling them there.
const AutoGrabCard: Component = () => {
  const [enabled, setEnabled] = createSignal(false);
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
    void fetchUsenetAutoGrabEnabled()
      .then((v) => setEnabled(v))
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
  });

  const save = async () => {
    try {
      await putUsenetAutoGrabEnabled(enabled());
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
