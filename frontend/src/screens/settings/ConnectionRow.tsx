// ConnectionRow / ConnectionMiniTable — the shared per-service connection
// controls (URL / Username / key or password, Test / Save / Delete, plus
// LAN-discovery hints) and the <table> chrome that wraps one or more rows.
// Extracted out of Connections.tsx so other Settings sections (e.g. AI.tsx's
// provider/Brave connection rows) can import these without depending on the
// whole Connections screen module. Its save path goes through
// buildConnectionUpsertBody (src/api/settings.ts), which OMITS the apiKey
// property when the operator didn't touch the key field of an already-configured
// connection — so an unrelated edit (e.g. changing the URL) never wipes the
// stored secret. See settings.test.tsx's dedicated assertion.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  Show,
} from "solid-js";
import {
  API_KEY_HELP_URLS,
  SERVICES_WITH_FIXED_URL,
  SERVICES_WITH_HOST_LOOKUP,
  buildConnectionUpsertBody,
  deleteConnection,
  fetchConnections,
  fetchNetscanKnown,
  fetchProwlarrKey,
  probeNetscanHost,
  testConnection,
  testConnectionStored,
  upsertConnection,
} from "../../api/settings";
import type { ConnectionSummary, NetscanFinding } from "../../api/settings";
import { Button, ErrorText, inputClass } from "../../components/ui";
import { SaveStatus, useSaveStatus, useSectionSaveItem } from "./shared";

// serviceLabel maps a connection service id to its display name. Every current
// singleton service's id already reads correctly as its own label, so the map is
// empty — it stays as the one place to add an override without touching the JSX.
// The former "jellyfin"/"nntp" entries are gone with the services themselves:
// media players and Usenet subscriptions moved to the multi-connection registry
// (migration 0053) and are no longer ConnectionRows.
const serviceLabel = (service: string): string => {
  const labels: Record<string, string> = {};
  return labels[service] ?? service;
};

// ConnectionRow is one service's controls: URL / Username (if needed) / key or
// password, plus Test / Save / Delete and, when a netscan finding exists, the
// LAN-discovery hint buttons. keyTouched tracks whether the operator (or the
// Fetch-key button) actually edited the key field — the input for a configured
// connection is blank (the real key is never sent back), so an untouched blank
// key must NOT be persisted as "".
export const ConnectionRow: Component<{
  service: string;
  existing: ConnectionSummary | undefined;
  finding: NetscanFinding | undefined;
  onChanged: () => void;
  // failing/onManualTestResult drive the shared red-tint state owned by the
  // parent table (auto-test-all + manual Test converge on one map). Optional so
  // the AI tab's ConnectionRows, which have no auto-test, keep compiling and
  // behave exactly as before.
  failing?: () => boolean;
  onManualTestResult?: (ok: boolean) => void;
}> = (props) => {
  // isFailing is the local red-tint accessor: true when the parent marked this
  // service's saved-connection test (or last manual Test) as failing.
  const isFailing = () => props.failing?.() ?? false;
  // needsFixedUrl services have a hardcoded server-side base URL — the row shows
  // no URL input, and save/test skip the "url is required" guard for them.
  const needsFixedUrl = SERVICES_WITH_FIXED_URL.includes(props.service);
  const allowHostProbe = SERVICES_WITH_HOST_LOOKUP.includes(props.service);
  const [url, setUrl] = createSignal(props.existing?.url ?? "");
  const [key, setKey] = createSignal("");
  const [keyTouched, setKeyTouched] = createSignal(false);
  // dirty flips true on any operator edit and resets after a successful save, so
  // the section's one Save button knows whether this row has pending changes.
  const [dirty, setDirty] = createSignal(false);
  const status = useSaveStatus();
  const [hint, setHint] = createSignal("");

  const hasExistingKey = () => !!props.existing?.hasApiKey;
  const keyPlaceholder = () =>
    hasExistingKey()
      ? `unchanged (••••${props.existing?.keySuffix ?? ""})`
      : "api key (if needed)";

  // needsUsername is always false here: no SINGLETON service authenticates with
  // username+password any more. nntp, the only one that ever did, moved to the
  // multi-connection registry with migration 0053 and has its own richer form on
  // the Usenet settings page. buildConnectionUpsertBody still TAKES the flag —
  // Discover's GrabDialog passes true for qbittorrent/nzbget — so only this
  // row's answer to it is fixed, not the helper's contract.
  const body = () =>
    buildConnectionUpsertBody({
      url: url(),
      needsUsername: false,
      keyTouched: keyTouched(),
      keyValue: key(),
      hasExistingKey: hasExistingKey(),
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
      setStatusFromTest(r.ok, r.error);
    } catch (e) {
      status.failed(e);
    }
  };
  const setStatusFromTest = (ok: boolean, err?: string) => {
    if (ok) status.set("✓ ok");
    else status.failed(new Error(err || "connection failed"));
    // Feed the outcome into the parent's shared failing-map so a manual Test
    // updates the SAME red-tint the auto-test-all populates (and a passing
    // manual Test clears it).
    props.onManualTestResult?.(ok);
  };

  // save sets its OWN inline status (per-row failure visibility) and rethrows on
  // failure so the section batcher can report which rows failed. On success it
  // clears the touched/key state so a subsequent untouched save omits apiKey
  // again (the connection is now configured; the real key is never sent back).
  const save = async () => {
    try {
      await upsertBody();
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
  // batched is true when this row lives inside a SectionSave — then the row hides
  // its own Save button and the section's one button drives it. Registration is a
  // no-op standalone (returns false), so ConnectionRow still works on its own.
  const batched = useSectionSaveItem({
    id: `connection:${props.service}`,
    label: props.service,
    dirty,
    save,
  });
  const upsertBody = async () => {
    await upsertConnection(props.service, body());
  };

  const remove = async () => {
    if (!confirm(`Remove the ${props.service} connection?`)) return;
    try {
      await deleteConnection(props.service);
      props.onChanged();
    } catch (e) {
      status.failed(e);
    }
  };

  const useURL = (u: string) => {
    setUrl(u);
    setDirty(true);
    setHint("URL pre-filled — verify it's really yours, then Save.");
  };
  const fetchKey = async (u: string) => {
    setHint("fetching key…");
    try {
      const k = await fetchProwlarrKey(u);
      setKey(k);
      setKeyTouched(true); // survive the three-state gate (no DOM event to lean on)
      setDirty(true);
      setHint(`API key retrieved from ${u} — verify before saving.`);
    } catch (e) {
      status.failed(e);
    }
  };

  // host-probe (Jellyfin lives off SAK's docker network) — fills the row's URL
  // from a discovered finding, same as a known-host finding does.
  const [probeHost, setProbeHost] = createSignal("");
  const [probed, setProbed] = createSignal<NetscanFinding | undefined>();
  const doProbe = async () => {
    setHint("probing…");
    setProbed(undefined);
    try {
      const findings = await probeNetscanHost(probeHost());
      const match = findings.find((f) => f.service === props.service);
      if (match) {
        setProbed(match);
        setHint("");
      } else if (findings.length) {
        setHint(
          `Found other services there (${findings
            .map((f) => f.service)
            .join(", ")}) but no ${props.service}.`,
        );
      } else {
        setHint(`No ${props.service} found at that host.`);
      }
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <tr class="border-b border-border/60 align-top">
      <td class="px-2 py-2 text-fg">{serviceLabel(props.service)}</td>
      <td class="px-2 py-2">
        <Show when={!needsFixedUrl}>
        <input
          type="text"
          class={`${inputClass} !w-72 ${isFailing() ? "border-danger bg-danger/10" : ""}`}
          placeholder="https://..."
          aria-label={`${props.service} URL`}
          value={url()}
          onInput={(e) => {
            setUrl(e.currentTarget.value);
            setDirty(true);
          }}
        />
        <Show when={props.service === "prowlarr"}>
          <a
            href="https://wiki.servarr.com/en/prowlarr"
            target="_blank"
            rel="noreferrer"
            class="mt-1 block text-xs text-fg underline decoration-accent underline-offset-2"
          >
            wiki.servarr.com/en/prowlarr
          </a>
        </Show>
        <Show when={props.finding || allowHostProbe}>
          <div class="mt-1 rounded border border-dashed border-border p-2 text-xs text-muted">
            <Show when={props.finding}>
              <div>
                Possible {props.service} at {props.finding!.url} — a hint only,
                verify it's yours.
              </div>
              <div class="mt-1 flex gap-2">
                <Button
                  class="!px-2 !py-1 !text-xs"
                  onClick={() => useURL(props.finding!.url)}
                >
                  Use this URL
                </Button>
                <Show when={props.service === "prowlarr"}>
                  <Button
                    class="!px-2 !py-1 !text-xs"
                    onClick={() => void fetchKey(props.finding!.url)}
                  >
                    Fetch API key
                  </Button>
                </Show>
              </div>
            </Show>
            <Show when={allowHostProbe}>
              <div class="mt-1">
                On a different host? Look up by IP or hostname:
                <div class="mt-1 flex gap-2">
                  <input
                    type="text"
                    class={`${inputClass} !w-40 !py-1 !text-xs`}
                    placeholder="e.g. 10.1.10.4"
                    aria-label={`Look up host for ${props.service}`}
                    value={probeHost()}
                    onInput={(e) => setProbeHost(e.currentTarget.value)}
                  />
                  <Button
                    class="!px-2 !py-1 !text-xs"
                    onClick={() => void doProbe()}
                  >
                    Look up
                  </Button>
                </div>
                <Show when={probed()}>
                  <div class="mt-1 flex items-center gap-2">
                    <span>Found at {probed()!.url}</span>
                    <Button
                      class="!px-2 !py-1 !text-xs"
                      onClick={() => useURL(probed()!.url)}
                    >
                      Use this URL
                    </Button>
                  </div>
                </Show>
              </div>
            </Show>
            <Show when={hint()}>
              <div class="mt-1">{hint()}</div>
            </Show>
          </div>
        </Show>
        </Show>
        <Show when={needsFixedUrl}>
          <input
            type="text"
            class={`${inputClass} !w-72 disabled:opacity-50 disabled:cursor-not-allowed`}
            aria-label={`${props.service} fixed URL (read-only)`}
            value={props.existing?.fixedUrl ?? ""}
            disabled
          />
        </Show>
      </td>
      {/* Username column: empty for every singleton service (see the body()
          comment above), kept so this row's cell count still matches
          ConnectionMiniTable's 6-column header. */}
      <td class="px-2 py-2" />
      <td class="px-2 py-2">
        <input
          type="password"
          class={`${inputClass} !w-64 ${isFailing() ? "border-danger bg-danger/10" : ""}`}
          placeholder={keyPlaceholder()}
          aria-label={`${props.service} API key`}
          value={key()}
          onInput={(e) => {
            setKey(e.currentTarget.value);
            setKeyTouched(true);
            setDirty(true);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              // Enter saves just this row; swallow so a rejection (surfaced
              // inline already) doesn't become an unhandled promise rejection.
              void save().catch(() => {});
            }
          }}
        />
        <Show when={API_KEY_HELP_URLS[props.service]}>
          <a
            href={API_KEY_HELP_URLS[props.service]}
            target="_blank"
            rel="noreferrer"
            class="mt-1 block text-xs text-fg underline decoration-accent underline-offset-2"
          >
            Get API key →
          </a>
        </Show>
        <Show when={props.service === "stash"}>
          <div class="mt-1 text-xs text-muted">
            Get your key: Stash → Settings → Security
          </div>
        </Show>
      </td>
      <td class="px-2 py-2">
        <div class="flex gap-1">
          <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
            Test
          </Button>
          {/* Own Save button only when standalone; inside a SectionSave the
              section's one button drives this row. Test/Delete stay per-row. */}
          <Show when={!batched()}>
            <Button
              variant="primary"
              class="!px-2 !py-1 !text-xs"
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
        </div>
      </td>
      <td class="px-2 py-2">
        <SaveStatus text={status.status().text} error={status.status().error} />
      </td>
    </tr>
  );
};

// ConnectionServiceTable is a ConnectionMiniTable over a given set of singleton
// services, owning the two pieces of state that must be shared ACROSS its rows:
// the /api/connections + netscan resources, and the auto-test-all red-tint map.
//
// Moved verbatim (minus the hardcoded CONNECTION_SERVICES list, now a prop) out
// of Connections.tsx's ConnectionsTable when the Connections tab was dismantled
// and its rows were redistributed to Advanced -> API Connections and Library ->
// per-mode. Two callers need this exact behaviour, so it lives here beside the
// row rather than being duplicated into each.
//
// Deliberately renders NO <Card> and NO <SectionSave> of its own — the caller
// supplies both, because the right answer differs per host. Advanced -> API
// Connections wraps it in its own SectionSave (nothing else there would batch
// it), while the Library tab's rows must join that tab's ONE existing
// SectionSave, so a private one here would split the Library tab into two Save
// buttons and quietly stop its main one from committing connection edits.
export const ConnectionServiceTable: Component<{
  services: () => string[];
}> = (props) => {
  const [conns, { refetch }] = createResource(fetchConnections);
  const [findings] = createResource(fetchNetscanKnown);
  // failing maps service → true when its saved connection failed its most recent
  // test (auto-test-all below, or a manual per-row Test). One source of truth in
  // the parent so both paths drive the same red-tint.
  const [failing, setFailing] = createSignal<Record<string, boolean>>({});

  // Auto-test every configured connection whenever the list (re)resolves — on
  // first load AND after any save (a successful ConnectionRow.save calls
  // onChanged → refetch → conns() re-resolves → this re-runs). Tests fire
  // concurrently (fire-and-forget per service, not awaited in sequence). Only
  // services with a stored key are tested; the rest have nothing to test and are
  // left unmarked. A thrown/transport error leaves the prior value rather than
  // tinting, so a backend hiccup doesn't red-tint every row at once — the
  // endpoint is a boolean signal, not an exception channel.
  //
  // Scoped to this table's own services: /api/connections returns every stored
  // singleton connection, but a table must never fire test-stored for a row it
  // doesn't render (that would make the Library tab test Prowlarr, and vice
  // versa).
  createEffect(
    on(conns, (list) => {
      if (!list) return;
      const mine = props.services();
      for (const c of list) {
        if (!c.hasApiKey || !mine.includes(c.service)) continue;
        void testConnectionStored(c.service)
          .then((r) => setFailing((prev) => ({ ...prev, [c.service]: !r.ok })))
          .catch(() => {});
      }
    }),
  );

  const byService = () => {
    const m: Record<string, ConnectionSummary> = {};
    for (const c of conns() ?? []) m[c.service] = c;
    return m;
  };
  const findingByService = () => {
    const m: Record<string, NetscanFinding> = {};
    for (const f of findings() ?? []) m[f.service] = f;
    return m;
  };

  return (
    <>
      <Show when={conns.error}>
        <ErrorText>{(conns.error as Error)?.message}</ErrorText>
      </Show>
      <ConnectionMiniTable>
        {/* Rows must mount only AFTER the connections resource resolves — each
            ConnectionRow seeds its local signals (URL, hasExistingKey) from
            props.existing at mount. Mounting while conns() is still undefined
            would seed hasExistingKey=false and a blank URL, so an untouched save
            would send apiKey="" and WIPE the stored secret (the exact Guardrail
            #5 bug). */}
        <Show when={conns() !== undefined}>
          <For each={props.services()}>
            {(service) => (
              <ConnectionRow
                service={service}
                existing={byService()[service]}
                finding={findingByService()[service]}
                onChanged={() => void refetch()}
                failing={() => failing()[service] === true}
                onManualTestResult={(ok) =>
                  setFailing((prev) => ({ ...prev, [service]: !ok }))
                }
              />
            )}
          </For>
        </Show>
      </ConnectionMiniTable>
    </>
  );
};

// ConnectionMiniTable is the shared <table> chrome (the overflow wrapper +
// the Service/URL/Username/API-Key header row) that wraps one or more
// ConnectionRows. The big Connections table renders this shape inline; the AI
// tab reuses it for its per-provider and Brave single-row tables so the column
// layout stays identical without duplicating the markup.
export const ConnectionMiniTable: Component<{ children: JSX.Element }> = (
  props,
) => (
  <div class="overflow-x-auto">
    <table class="w-full text-left text-sm">
      <thead>
        <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
          <th class="px-2 py-2 font-medium">Service</th>
          <th class="px-2 py-2 font-medium">URL</th>
          <th class="px-2 py-2 font-medium">Username</th>
          <th class="px-2 py-2 font-medium">API Key / Password</th>
          <th class="px-2 py-2 font-medium" />
          <th class="px-2 py-2 font-medium" />
        </tr>
      </thead>
      <tbody>{props.children}</tbody>
    </table>
  </div>
);
