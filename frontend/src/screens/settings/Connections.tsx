// Connections — the safety-critical Settings section. Its save path goes through
// buildConnectionUpsertBody (src/api/settings.ts), which OMITS the apiKey
// property when the operator didn't touch the key field of an already-configured
// connection — so an unrelated edit (e.g. changing the URL) never wipes the
// stored secret. See settings.test.tsx's dedicated assertion. LAN-discovery
// (netscan) hints live here too. Extracted from the original single-file
// Settings.tsx.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  onCleanup,
  Show,
} from "solid-js";
import {
  CONNECTION_SERVICES,
  SERVICES_WITH_FIXED_URL,
  SERVICES_WITH_USERNAME,
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
import {
  buildTraktCredentialsBody,
  disconnectTrakt,
  fetchTraktStatus,
  pollTraktDevice,
  saveTraktCredentials,
  startTraktDeviceFlow,
  type TraktDeviceStartResponse,
} from "../../api/trakt";
import {
  Button,
  ErrorText,
  inputClass,
  labelClass,
  Muted,
} from "../../components/ui";
import {
  Card,
  SaveStatus,
  SectionSave,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

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
  const needsUsername = SERVICES_WITH_USERNAME.includes(props.service);
  // needsFixedUrl services have a hardcoded server-side base URL — the row shows
  // no URL input, and save/test skip the "url is required" guard for them.
  const needsFixedUrl = SERVICES_WITH_FIXED_URL.includes(props.service);
  const allowHostProbe = props.service === "jellyfin" || props.service === "stash";
  const [url, setUrl] = createSignal(props.existing?.url ?? "");
  const [username, setUsername] = createSignal(props.existing?.username ?? "");
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
      : needsUsername
        ? "password"
        : "api key (if needed)";

  const body = () =>
    buildConnectionUpsertBody({
      url: url(),
      username: username(),
      needsUsername,
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
      <td class="px-2 py-2 text-fg">{props.service === "jellyfin" ? "Jellyfin/Emby" : props.service}</td>
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
                On a different host? Probe a specific LAN IP:
                <div class="mt-1 flex gap-2">
                  <input
                    type="text"
                    class={`${inputClass} !w-40 !py-1 !text-xs`}
                    placeholder="e.g. 10.1.10.4"
                    aria-label={`Probe host for ${props.service}`}
                    value={probeHost()}
                    onInput={(e) => setProbeHost(e.currentTarget.value)}
                  />
                  <Button
                    class="!px-2 !py-1 !text-xs"
                    onClick={() => void doProbe()}
                  >
                    Probe
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
      </td>
      <td class="px-2 py-2">
        <Show when={needsUsername}>
          <input
            type="text"
            class={`${inputClass} !w-40`}
            placeholder="username"
            aria-label={`${props.service} username`}
            value={username()}
            onInput={(e) => {
              setUsername(e.currentTarget.value);
              setDirty(true);
            }}
          />
        </Show>
      </td>
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

// ---- Trakt (Watchlist connection) ------------------------------------------
//
// Trakt is NOT a row in the generic Connections table above — it needs an
// OAuth device-code flow (user_code + verification_url + poll-until-linked)
// on top of the plain client_id/client_secret save, which the generic table
// has no room for. Its own Card, same section tab, same three-state secret
// convention (buildTraktCredentialsBody mirrors buildConnectionUpsertBody).
const TraktConnectionSection: Component = () => {
  const [status, { refetch }] = createResource(fetchTraktStatus);
  const [clientId, setClientId] = createSignal("");
  const [clientSecret, setClientSecret] = createSignal("");
  const [secretTouched, setSecretTouched] = createSignal(false);
  // dirty flips true on any credential edit and resets when fresh server state
  // arrives or a save succeeds — same role as ConnectionRow's dirty signal, so
  // the Connections tab's one Save button can drive Trakt as one batched item.
  const [dirty, setDirty] = createSignal(false);
  createEffect(() => {
    const s = status();
    if (s?.clientId !== undefined) {
      setClientId(s.clientId);
      setDirty(false);
    }
  });
  const saveStatus = useSaveStatus();

  const hasExistingSecret = () => !!status()?.configured;
  const secretPlaceholder = () =>
    hasExistingSecret() ? "unchanged (configured)" : "client secret";

  // saveCredentials keeps Trakt's OWN three-state secret gate
  // (buildTraktCredentialsBody omits clientSecret when untouched) and its own
  // inline status; it rethrows on failure — including the empty-clientId
  // early-out — so the section batcher never reports a false "saved".
  const saveCredentials = async () => {
    if (!clientId().trim()) {
      const err = new Error("client id is required");
      saveStatus.failed(err);
      throw err;
    }
    try {
      await saveTraktCredentials(
        buildTraktCredentialsBody({
          clientId: clientId(),
          secretTouched: secretTouched(),
          secretValue: clientSecret(),
        }),
      );
      setClientSecret("");
      setSecretTouched(false);
      setDirty(false);
      saveStatus.saved();
      await refetch();
    } catch (e) {
      saveStatus.failed(e);
      throw e;
    }
  };
  // Trakt folds into the Connections tab's one Save button as one batched item
  // (its own save function, never a merged payload). Connect/Disconnect and the
  // device-code OAuth flow below stay independent immediate actions.
  const batched = useSectionSaveItem({
    id: "trakt",
    label: "Trakt",
    dirty,
    save: saveCredentials,
  });

  // --- Device-code OAuth flow ---
  const [device, setDevice] = createSignal<TraktDeviceStartResponse | null>(
    null,
  );
  const [connecting, setConnecting] = createSignal(false);
  const [connectError, setConnectError] = createSignal("");
  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  let pollDeadline = 0;

  const stopPolling = () => {
    if (pollTimer !== undefined) clearTimeout(pollTimer);
    pollTimer = undefined;
  };
  onCleanup(stopPolling);

  const schedulePoll = (intervalSeconds: number) => {
    stopPolling();
    pollTimer = setTimeout(() => void doPoll(), Math.max(1, intervalSeconds) * 1000);
  };

  const doPoll = async () => {
    if (Date.now() > pollDeadline) {
      setConnecting(false);
      setDevice(null);
      setConnectError("device code expired — click Connect to try again");
      return;
    }
    try {
      const r = await pollTraktDevice();
      if (r.linked) {
        setConnecting(false);
        setDevice(null);
        await refetch();
        return;
      }
      schedulePoll(device()?.interval ?? 5);
    } catch (e) {
      setConnecting(false);
      setDevice(null);
      setConnectError((e as Error).message);
    }
  };

  const connect = async () => {
    setConnectError("");
    setConnecting(true);
    try {
      const dc = await startTraktDeviceFlow();
      setDevice(dc);
      pollDeadline = Date.now() + dc.expiresIn * 1000;
      schedulePoll(dc.interval);
    } catch (e) {
      setConnecting(false);
      setConnectError((e as Error).message);
    }
  };

  const cancelConnect = () => {
    stopPolling();
    setConnecting(false);
    setDevice(null);
    setConnectError("");
  };

  const disconnect = async () => {
    if (!confirm("Disconnect Trakt? The Watchlist row will stop appearing until you reconnect.")) return;
    try {
      await disconnectTrakt();
      await refetch();
    } catch (e) {
      saveStatus.failed(e);
    }
  };

  return (
    <Card title="Trakt (Watchlist)">
      <Show when={status.error}>
        <ErrorText>{(status.error as Error)?.message}</ErrorText>
      </Show>
      <Muted class="mb-3">
        Connect a Trakt.tv application to surface a "Trakt Watchlist" row on
        Discover — titles marked "want to watch" there but not yet in your
        library. Create an application at trakt.tv/oauth/applications, then
        paste its client ID/secret below.
      </Muted>

      <form
        class="mb-3"
        onSubmit={(e) => (
          e.preventDefault(), void saveCredentials().catch(() => {})
        )}
      >
        <label class="mb-2 block">
          <span class={labelClass}>Client ID</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Trakt client ID"
            value={clientId()}
            onInput={(e) => {
              setClientId(e.currentTarget.value);
              setDirty(true);
            }}
          />
        </label>
        <label class="mb-2 block">
          <span class={labelClass}>Client secret</span>
          <input
            type="password"
            class={`${inputClass} mt-1`}
            placeholder={secretPlaceholder()}
            aria-label="Trakt client secret"
            value={clientSecret()}
            onInput={(e) => {
              setClientSecret(e.currentTarget.value);
              setSecretTouched(true);
              setDirty(true);
            }}
          />
        </label>
        <div class="flex items-center gap-2">
          {/* Own Save button only when standalone; inside the Connections tab's
              SectionSave the one section button commits Trakt too. */}
          <Show when={!batched()}>
            <Button variant="primary" type="submit">
              Save credentials
            </Button>
          </Show>
          <SaveStatus
            text={saveStatus.status().text}
            error={saveStatus.status().error}
          />
        </div>
      </form>

      <Show when={status() && !status()!.linked}>
        <div class="rounded-md border border-dashed border-border p-3">
          <Show
            when={device()}
            fallback={
              <div class="flex items-center gap-2">
                <Button
                  variant="primary"
                  disabled={!status()!.configured || connecting()}
                  onClick={() => void connect()}
                >
                  {connecting() ? "Starting…" : "Connect"}
                </Button>
                <Show when={!status()!.configured}>
                  <Muted>Save credentials first.</Muted>
                </Show>
              </div>
            }
          >
            {(dc) => (
              <div>
                <p class="text-sm text-fg">
                  Go to{" "}
                  <a
                    href={dc().verificationUrl}
                    target="_blank"
                    rel="noreferrer"
                    class="text-fg underline decoration-accent underline-offset-2"
                  >
                    {dc().verificationUrl}
                  </a>{" "}
                  and enter this code:
                </p>
                <div class="my-2 text-2xl font-bold tracking-widest text-fg">
                  {dc().userCode}
                </div>
                <div class="flex items-center gap-2">
                  <Muted>Waiting for approval…</Muted>
                  <Button class="!px-2 !py-1 !text-xs" onClick={cancelConnect}>
                    Cancel
                  </Button>
                </div>
              </div>
            )}
          </Show>
          <Show when={connectError()}>
            <ErrorText>{connectError()}</ErrorText>
          </Show>
        </div>
      </Show>

      <Show when={status()?.linked}>
        <div class="flex items-center gap-3">
          <span class="text-sm text-ok">✓ Connected</span>
          <Show when={status()?.tokenExpiresAt}>
            <Muted>
              Token valid until{" "}
              {new Date(status()!.tokenExpiresAt!).toLocaleString()}
            </Muted>
          </Show>
          <Button class="!px-2 !py-1 !text-xs" onClick={() => void disconnect()}>
            Disconnect
          </Button>
        </div>
      </Show>
    </Card>
  );
};

const ConnectionsTable: Component = () => {
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
  createEffect(
    on(conns, (list) => {
      if (!list) return;
      for (const c of list) {
        if (!c.hasApiKey) continue;
        void testConnectionStored(c.service)
          .then((r) =>
            setFailing((prev) => ({ ...prev, [c.service]: !r.ok })),
          )
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
    <Card title="Connections">
      <Show when={conns.error}>
        <ErrorText>{(conns.error as Error)?.message}</ErrorText>
      </Show>
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
          <tbody>
            {/* Rows must mount only AFTER the connections resource resolves —
                each ConnectionRow seeds its local signals (URL, hasExistingKey)
                from props.existing at mount. Mounting while conns() is still
                undefined would seed hasExistingKey=false and a blank URL, so an
                untouched save would send apiKey="" and WIPE the stored secret
                (the exact Guardrail #5 bug). */}
            <Show when={conns() !== undefined}>
              <For each={CONNECTION_SERVICES}>
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
          </tbody>
        </table>
      </div>
    </Card>
  );
};

// One Save button for the whole Connections tab: it commits every dirty
// ConnectionRow plus the Trakt form in a single click, each still built by its
// own per-row logic. Per-row Test/Delete and Trakt's Connect/Disconnect stay
// independent immediate actions.
export const ConnectionsSection: Component = () => (
  <SectionSave>
    <ConnectionsTable />
    <TraktConnectionSection />
  </SectionSave>
);
