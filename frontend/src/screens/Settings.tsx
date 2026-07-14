// Settings — ported from the vanilla-JS frontend's renderSettings
// (internal/web/static/index.html) plus the new Advanced Settings section.
// SECTION TABS (registered with the app shell via useScreenTabs, so the shell
// draws the bar in its one consistent location; inline fallback when rendered
// standalone in a unit test): Connections; Auth (Authentication mode + API
// Access break-glass key together); AI; Library (per-mode root folder, quality
// prefs, naming preset, kids path — Movies/Series only); Advanced (per-mode
// phash-threshold; match-confidence-threshold for Movies/Series; identify-
// enabled for Adult only; recheck-interval is global).
//
// There are TWO INDEPENDENT selectors here and they must not be conflated: the
// section-tab selector above, and a Movies/Series/Adult MODE selector rendered
// INLINE inside the Library and Advanced tabs (the only tabs with per-mode
// content). The mode selector is a plain ScreenTabBar — it is NOT registered
// with the shell, since the shell's single tab slot already holds the section
// tabs. One shared `mode` signal backs both per-mode tabs, so switching between
// Library and Advanced preserves the selected mode.
//
// THE SAFETY-CRITICAL PIECE is the Connections table's save path: it goes
// through buildConnectionUpsertBody (src/api/settings.ts), which OMITS the
// apiKey property when the operator didn't touch the key field of an
// already-configured connection — so an unrelated edit (e.g. changing the URL)
// never wipes the stored secret. See settings.test.tsx's dedicated assertion.
//
// LAN-discovery (netscan) hints are RELOCATED here from the old setup wizard's
// buildConnectedServicesWizardStep (they never lived in the old Settings table)
// — the task's "buildNetscanHint equivalent". Unlike the old DOM-event hack,
// the "Use this URL" / "Fetch API key" buttons set the row's signals directly
// (Solid reactivity), and Fetch-key marks the key field TOUCHED so the freshly
// fetched key survives buildConnectionUpsertBody's three-state gate.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  on,
  onCleanup,
  Show,
} from "solid-js";
import type { Mode } from "../api/discover";
import {
  AI_PROVIDERS,
  CONNECTION_SERVICES,
  MAX_RESOLUTIONS,
  NAMING_PRESETS,
  QUALITY_TIERS,
  SERVICES_WITH_USERNAME,
  buildConnectionUpsertBody,
  deleteConnection,
  fetchAIModel,
  fetchAIProvider,
  fetchAPIKeyStatus,
  fetchAuthMode,
  fetchConfidenceThreshold,
  fetchConnections,
  fetchIdentifyEnabled,
  fetchKidsRootPath,
  fetchLibraryRootFolder,
  fetchNamingPreset,
  fetchNetscanKnown,
  fetchOIDCStatus,
  fetchPHashThreshold,
  fetchProwlarrKey,
  fetchQualityPrefs,
  fetchRecheckInterval,
  probeNetscanHost,
  putAIModel,
  putAIProvider,
  putAuthMode,
  putConfidenceThreshold,
  putIdentifyEnabled,
  putKidsRootPath,
  putLibraryRootFolder,
  putNamingPreset,
  putOIDCConfig,
  putPHashThreshold,
  putQualityPrefs,
  putRecheckInterval,
  regenerateAPIKey,
  testConnection,
  upsertConnection,
} from "../api/settings";
import type { ConnectionSummary, NetscanFinding } from "../api/settings";
import {
  buildTraktCredentialsBody,
  disconnectTrakt,
  fetchTraktStatus,
  pollTraktDevice,
  saveTraktCredentials,
  startTraktDeviceFlow,
  type TraktDeviceStartResponse,
} from "../api/trakt";
import { SliderAdminSection } from "./SliderAdmin";
import {
  Button,
  Card,
  ErrorText,
  MODES,
  Muted,
  SaveStatus,
  ScreenTabBar,
  ScreenTabs,
  type TabDef,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../components/ui";

const MODE_LABELS: Record<Mode, string> = {
  movies: "Movies",
  series: "Series",
  adult: "Adult",
};

// Card/SaveStatus/useSaveStatus moved to components/ui.tsx (2026-07-14) so the
// admin slider editor can share the same panel frame/status-line convention
// instead of duplicating it — imported from "../components/ui" below.

// ---- Connections ----------------------------------------------------------

// ConnectionRow is one service's controls: URL / Username (if needed) / key or
// password, plus Test / Save / Delete and, when a netscan finding exists, the
// LAN-discovery hint buttons. keyTouched tracks whether the operator (or the
// Fetch-key button) actually edited the key field — the input for a configured
// connection is blank (the real key is never sent back), so an untouched blank
// key must NOT be persisted as "".
const ConnectionRow: Component<{
  service: string;
  existing: ConnectionSummary | undefined;
  finding: NetscanFinding | undefined;
  onChanged: () => void;
}> = (props) => {
  const needsUsername = SERVICES_WITH_USERNAME.includes(props.service);
  const allowHostProbe = props.service === "jellyfin";
  const [url, setUrl] = createSignal(props.existing?.url ?? "");
  const [username, setUsername] = createSignal(props.existing?.username ?? "");
  const [key, setKey] = createSignal("");
  const [keyTouched, setKeyTouched] = createSignal(false);
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
  };

  const save = async () => {
    try {
      await upsertBody();
      status.set("✓ saved");
      props.onChanged();
    } catch (e) {
      status.failed(e);
    }
  };
  // upsertBody is split out so the URL-required guard mirrors the backend
  // (url is required) with a clear inline message rather than a 400 round-trip.
  const upsertBody = async () => {
    if (!url().trim()) throw new Error("url is required");
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
    setHint("URL pre-filled — verify it's really yours, then Save.");
  };
  const fetchKey = async (u: string) => {
    setHint("fetching key…");
    try {
      const k = await fetchProwlarrKey(u);
      setKey(k);
      setKeyTouched(true); // survive the three-state gate (no DOM event to lean on)
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
      <td class="px-2 py-2 text-fg">{props.service}</td>
      <td class="px-2 py-2">
        <input
          type="text"
          class={`${inputClass} !w-52`}
          placeholder="https://..."
          aria-label={`${props.service} URL`}
          value={url()}
          onInput={(e) => setUrl(e.currentTarget.value)}
        />
        <Show when={props.service === "prowlarr"}>
          <a
            href="https://wiki.servarr.com/en/prowlarr"
            target="_blank"
            rel="noreferrer"
            class="mt-1 block text-xs text-accent underline"
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
      </td>
      <td class="px-2 py-2">
        <Show when={needsUsername}>
          <input
            type="text"
            class={`${inputClass} !w-32`}
            placeholder="username"
            aria-label={`${props.service} username`}
            value={username()}
            onInput={(e) => setUsername(e.currentTarget.value)}
          />
        </Show>
      </td>
      <td class="px-2 py-2">
        <input
          type="password"
          class={`${inputClass} !w-52`}
          placeholder={keyPlaceholder()}
          aria-label={`${props.service} API key`}
          value={key()}
          onInput={(e) => {
            setKey(e.currentTarget.value);
            setKeyTouched(true);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void save();
            }
          }}
        />
      </td>
      <td class="px-2 py-2">
        <div class="flex gap-1">
          <Button class="!px-2 !py-1 !text-xs" onClick={() => void test()}>
            Test
          </Button>
          <Button
            variant="primary"
            class="!px-2 !py-1 !text-xs"
            onClick={() => void save()}
          >
            Save
          </Button>
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

const ConnectionsSection: Component = () => {
  const [conns, { refetch }] = createResource(fetchConnections);
  const [findings] = createResource(fetchNetscanKnown);
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

// ---- Trakt (Watchlist connection) ------------------------------------------
//
// Trakt is NOT a row in the generic Connections table above — it needs an
// OAuth device-code flow (user_code + verification_url + poll-until-linked)
// on top of the plain client_id/client_secret save, which the generic table
// has no room for. Its own Card, same section tab, same three-state secret
// convention (buildTraktCredentialsBody mirrors buildConnectionUpsertBody).
//
// PLACEHOLDER: every endpoint this section calls (src/api/trakt.ts) is a
// proposed contract, not yet confirmed — task #5 (worker-1) is still wiring
// the real backend routes/DTOs as of this writing. See trakt.ts's file doc
// comment for the full list of what's provisional.
const TraktConnectionSection: Component = () => {
  const [status, { refetch }] = createResource(fetchTraktStatus);
  const [clientId, setClientId] = createSignal("");
  const [clientSecret, setClientSecret] = createSignal("");
  const [secretTouched, setSecretTouched] = createSignal(false);
  createEffect(() => {
    const s = status();
    if (s?.clientId !== undefined) setClientId(s.clientId);
  });
  const saveStatus = useSaveStatus();

  const hasExistingSecret = () => !!status()?.configured;
  const secretPlaceholder = () =>
    hasExistingSecret() ? "unchanged (configured)" : "client secret";

  const saveCredentials = async () => {
    if (!clientId().trim()) {
      saveStatus.failed(new Error("client id is required"));
      return;
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
      saveStatus.saved();
      await refetch();
    } catch (e) {
      saveStatus.failed(e);
    }
  };

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
        onSubmit={(e) => (e.preventDefault(), void saveCredentials())}
      >
        <label class="mb-2 block">
          <span class={labelClass}>Client ID</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Trakt client ID"
            value={clientId()}
            onInput={(e) => setClientId(e.currentTarget.value)}
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
            }}
          />
        </label>
        <div class="flex items-center gap-2">
          <Button variant="primary" type="submit">
            Save credentials
          </Button>
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
                    class="text-accent underline"
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

// ---- API Access -----------------------------------------------------------

const APIAccessSection: Component = () => {
  const [s, { refetch }] = createResource(fetchAPIKeyStatus);
  const status = useSaveStatus();
  const [revealed, setRevealed] = createSignal("");

  const envManaged = () => s()?.source === "env";
  const regenerate = async () => {
    if (!confirm("This immediately invalidates the current key. Continue?"))
      return;
    try {
      const r = await regenerateAPIKey();
      setRevealed(r.apiKey);
      status.set("");
      await refetch();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <Card title="API Access">
      <Show when={s.error}>
        <ErrorText>{(s.error as Error)?.message}</ErrorText>
      </Show>
      <Muted>
        {s()?.hasKey
          ? `Current key: ••••${s()?.keySuffix ?? ""}`
          : "No API key configured yet."}
      </Muted>
      <div class="mt-2 flex items-center gap-2">
        <Button
          variant="primary"
          disabled={envManaged()}
          onClick={() => void regenerate()}
        >
          {s()?.hasKey ? "Regenerate key" : "Generate key"}
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Show when={envManaged()}>
        <Muted class="mt-2">
          This key is supplied by the SAKMS_API_KEY environment variable and is
          managed outside the app. Regenerate is disabled; unset the variable to
          manage the key here.
        </Muted>
      </Show>
      <Show when={revealed()}>
        <div class="mt-2">
          <input
            type="text"
            readOnly
            class={inputClass}
            aria-label="New API key"
            value={revealed()}
            ref={(el) => queueMicrotask(() => el.select())}
          />
          <div class="mt-1 text-sm text-danger">
            Shown once — copy it now; it cannot be retrieved later.
          </div>
        </div>
      </Show>
      <Muted class="mt-2">
        Send this key as the X-Api-Key request header to call /api/... without a
        browser session.
      </Muted>
    </Card>
  );
};

// ---- Authentication mode --------------------------------------------------

const AuthModeSection: Component<{ onReboot: () => void }> = (props) => {
  const [current] = createResource(fetchAuthMode);
  const [oidc] = createResource(fetchOIDCStatus);
  const [selected, setSelected] = createSignal<string>("password");
  createEffect(() => {
    const m = current()?.mode;
    if (m) setSelected(m);
  });

  const status = useSaveStatus();
  const oidcStatus = useSaveStatus();
  const [issuer, setIssuer] = createSignal("");
  const [clientId, setClientId] = createSignal("");
  const [clientSecret, setClientSecret] = createSignal("");
  const [redirect, setRedirect] = createSignal("");
  createEffect(() => {
    const o = oidc();
    if (o) {
      setIssuer(o.issuerUrl);
      setClientId(o.clientId);
      setRedirect(o.redirectUrl);
    }
  });

  const saveOidc = async () => {
    try {
      await putOIDCConfig({
        issuerUrl: issuer(),
        clientId: clientId(),
        clientSecret: clientSecret(),
        redirectUrl: redirect(),
      });
      oidcStatus.saved();
      setClientSecret("");
    } catch (e) {
      oidcStatus.failed(e);
    }
  };

  const switchMode = async () => {
    status.set("");
    const mode = selected();
    const body: { mode: string; acknowledgeInsecure?: boolean } = { mode };
    if (mode === "none") {
      if (
        !confirm(
          "Disabling authentication leaves this instance and every connected service open to anyone who can reach it. Continue?",
        )
      )
        return;
      body.acknowledgeInsecure = true;
    }
    try {
      // Preconditions (password needs an existing hash, oidc needs saved
      // config) are enforced server-side and surface as this thrown error.
      await putAuthMode({
        mode: body.mode,
        acknowledgeInsecure: body.acknowledgeInsecure ?? false,
      });
      status.set("switched");
      props.onReboot();
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <Card title="Authentication mode">
      <Show when={current()?.mode === "none"}>
        <ErrorText>
          Authentication is currently disabled — this instance and every
          connected service is open to anyone who can reach it.
        </ErrorText>
      </Show>
      <label class="mb-3 block">
        <span class={labelClass}>Mode</span>
        <div class="mt-1">
          <select
            class={inputClass}
            value={selected()}
            onChange={(e) => setSelected(e.currentTarget.value)}
          >
            <option value="password">Password</option>
            <option value="oidc">OIDC (single sign-on)</option>
            <option value="none">None (no authentication)</option>
          </select>
        </div>
      </label>

      <Show when={selected() === "password"}>
        <Muted>
          Switches back to the username/password login already set up for this
          instance. There's no way to set or change the password from here —
          that only happens at first-run setup.
        </Muted>
      </Show>

      <Show when={selected() === "oidc"}>
        <div>
          <Muted class="mb-2">
            sakms runs a real OpenID Connect Authorization Code flow (with PKCE)
            as the Relying Party — it verifies the IdP's signed ID token against
            its published JWKS, so no proxy-held shared secret is needed.
          </Muted>
          <form onSubmit={(e) => (e.preventDefault(), void saveOidc())}>
            <label class="mb-2 block">
              <span class={labelClass}>Issuer URL</span>
              <input
                type="text"
                class={`${inputClass} mt-1`}
                placeholder="https://sso.example.com/application/o/sakms/"
                value={issuer()}
                onInput={(e) => setIssuer(e.currentTarget.value)}
              />
            </label>
            <label class="mb-2 block">
              <span class={labelClass}>Client ID</span>
              <input
                type="text"
                class={`${inputClass} mt-1`}
                value={clientId()}
                onInput={(e) => setClientId(e.currentTarget.value)}
              />
            </label>
            <label class="mb-2 block">
              <span class={labelClass}>Client secret</span>
              <input
                type="password"
                class={`${inputClass} mt-1`}
                placeholder={
                  oidc()?.hasSecret ? "unchanged (configured)" : "client secret"
                }
                value={clientSecret()}
                onInput={(e) => setClientSecret(e.currentTarget.value)}
              />
            </label>
            <label class="mb-2 block">
              <span class={labelClass}>Redirect URL</span>
              <input
                type="text"
                class={`${inputClass} mt-1`}
                placeholder="https://media-admin.example.com/api/auth/oidc/callback"
                value={redirect()}
                onInput={(e) => setRedirect(e.currentTarget.value)}
              />
            </label>
            <div class="flex items-center gap-2">
              <Button type="submit">Save OIDC config</Button>
              <SaveStatus
                text={oidcStatus.status().text}
                error={oidcStatus.status().error}
              />
            </div>
          </form>
          <Muted class="mt-2">
            The redirect URL must be registered as an allowed callback in your
            IdP's client config, and must point at this instance's
            /api/auth/oidc/callback.
          </Muted>
        </div>
      </Show>

      <Show when={selected() === "none"}>
        <ErrorText>
          Disables authentication entirely — this instance and every connected
          service becomes reachable by anyone who can reach it. You'll be asked
          to confirm before this takes effect.
        </ErrorText>
      </Show>

      <div class="mt-3 flex items-center gap-2">
        <Button variant="primary" onClick={() => void switchMode()}>
          Switch to this mode
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-2">
        Save OIDC's config above before switching into it — switching enforces
        the config already exists.
      </Muted>
    </Card>
  );
};

// ---- AI -------------------------------------------------------------------

const AISection: Component = () => {
  const [provider] = createResource(fetchAIProvider);
  const [model] = createResource(fetchAIModel);
  const [prov, setProv] = createSignal("ollama");
  const [mdl, setMdl] = createSignal("");
  createEffect(() => {
    const p = provider();
    if (p) setProv(p);
  });
  createEffect(() => {
    const m = model();
    if (m !== undefined) setMdl(m);
  });
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putAIProvider(prov());
      await putAIModel(mdl());
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <Card title="AI (shared by Adult identification and the Movies/Series title-guess fallback)">
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="block">
            <span class={labelClass}>Provider</span>
            <select
              class={`${inputClass} mt-1`}
              value={prov()}
              onChange={(e) => setProv(e.currentTarget.value)}
            >
              <For each={AI_PROVIDERS}>
                {(p) => <option value={p}>{p}</option>}
              </For>
            </select>
          </label>
          <label class="block">
            <span class={labelClass}>Model</span>
            <input
              type="text"
              class={`${inputClass} mt-1`}
              placeholder="e.g. qwen2.5vl:7b, gpt-4o-mini, gemini-2.5-flash, claude-haiku-4-5"
              value={mdl()}
              onInput={(e) => setMdl(e.currentTarget.value)}
            />
          </label>
        </div>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            Save
          </Button>
          <SaveStatus
            text={status.status().text}
            error={status.status().error}
          />
        </div>
      </form>
      <Muted class="mt-2">
        Configure a connection for whichever provider you pick above (same
        Connections table) — the model must be able to return structured JSON.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: library root folder ----------------------------------------

const LibraryRootFolderSection: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchLibraryRootFolder);
  const [path, setPath] = createSignal("");
  createEffect(
    on(current, (p) => {
      if (p !== undefined) setPath(p ?? "");
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putLibraryRootFolder(props.mode(), path());
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <Card title={`${MODE_LABELS[props.mode()]} library`}>
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="block">
          <span class={labelClass}>Root folder</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            placeholder={`/path/to/${MODE_LABELS[props.mode()]}`}
            aria-label="Library root folder"
            value={path()}
            onInput={(e) => setPath(e.currentTarget.value)}
          />
        </label>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            Save
          </Button>
          <SaveStatus
            text={status.status().text}
            error={status.status().error}
          />
        </div>
      </form>
      <Muted class="mt-2">
        Where Rename/Purge/Dedup and Search's Check &amp; Import look for and
        place {MODE_LABELS[props.mode()]} files — no{" "}
        {props.mode() === "movies" ? "Radarr" : "Sonarr"} involved.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: quality preferences ----------------------------------------

const QualityPrefsSection: Component<{ mode: () => Mode }> = (props) => {
  const [prefs] = createResource(props.mode, fetchQualityPrefs);
  const [tier, setTier] = createSignal("high");
  const [maxRes, setMaxRes] = createSignal(0);
  createEffect(
    on(prefs, (p) => {
      if (p) {
        setTier(p.tier);
        setMaxRes(p.maxResolution);
      }
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putQualityPrefs(props.mode(), {
        tier: tier(),
        maxResolution: maxRes(),
      });
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <Card title={`Search quality preferences (${MODE_LABELS[props.mode()]})`}>
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="block">
            <span class={labelClass}>Tier (bitrate/codec)</span>
            <select
              class={`${inputClass} mt-1`}
              value={tier()}
              onChange={(e) => setTier(e.currentTarget.value)}
            >
              <For each={QUALITY_TIERS}>{(t) => <option value={t}>{t}</option>}</For>
            </select>
          </label>
          <label class="block">
            <span class={labelClass}>Maximum resolution</span>
            <select
              class={`${inputClass} mt-1`}
              value={String(maxRes())}
              onChange={(e) => setMaxRes(Number(e.currentTarget.value))}
            >
              <For each={MAX_RESOLUTIONS}>
                {(r) => (
                  <option value={String(r)}>{r === 0 ? "no cap" : `${r}p`}</option>
                )}
              </For>
            </select>
          </label>
        </div>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            Save
          </Button>
          <SaveStatus
            text={status.status().text}
            error={status.status().error}
          />
        </div>
      </form>
      <Muted class="mt-2">
        Tier prefers smaller/more-compressed releases (Low) up to the
        least-compressed remux/Blu-ray (Lossless) — it never changes what
        resolution is preferred. Maximum resolution softly prefers at-or-below-cap
        results, falling back to whatever's available if nothing meets it.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: naming preset ----------------------------------------------

const NamingPresetSection: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchNamingPreset);
  const [preset, setPreset] = createSignal("jellyfin");
  createEffect(
    on(current, (p) => {
      if (p) setPreset(p);
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putNamingPreset(props.mode(), preset());
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <Card title={`File/folder naming (${MODE_LABELS[props.mode()]})`}>
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="block">
          <span class={labelClass}>Naming convention</span>
          <select
            class={`${inputClass} mt-1`}
            value={preset()}
            onChange={(e) => setPreset(e.currentTarget.value)}
          >
            <For each={NAMING_PRESETS}>
              {(p) => <option value={p.value}>{p.label}</option>}
            </For>
          </select>
        </label>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            Save
          </Button>
          <SaveStatus
            text={status.status().text}
            error={status.status().error}
          />
        </div>
      </form>
      <Muted class="mt-2">
        Jellyfin/Emby standard renames into "Title (Year) [tmdbid-N]"
        folders/files. Legacy keeps this project's original convention, so an
        already-renamed library's shape never silently changes after an upgrade.
      </Muted>
    </Card>
  );
};

// ---- Per-mode: kids root path ---------------------------------------------

const KidsRootPathSection: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchKidsRootPath);
  const [path, setPath] = createSignal("");
  createEffect(
    on(current, (p) => {
      if (p !== undefined) setPath(p ?? "");
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putKidsRootPath(props.mode(), path());
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <Card title={`Kids classification (${MODE_LABELS[props.mode()]})`}>
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="block">
          <span class={labelClass}>Kids root folder path</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            placeholder={`/path/to/${MODE_LABELS[props.mode()]} (Kids)`}
            aria-label="Kids root folder path"
            value={path()}
            onInput={(e) => setPath(e.currentTarget.value)}
          />
        </label>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            Save
          </Button>
          <SaveStatus
            text={status.status().text}
            error={status.status().error}
          />
        </div>
      </form>
      <Muted class="mt-2">
        Leave blank to turn Kids classification off. Applies to both newly-found
        files and already-tracked items whose classification has drifted.
      </Muted>
    </Card>
  );
};

// ---- Advanced Settings (new) ----------------------------------------------

// NumberSetting is one bounded integer field (phash-threshold,
// match-confidence-threshold, recheck-interval). It mirrors the backend's range
// client-side (min/max) before submitting; the backend re-validates. save
// disabled while out of range so the operator sees the bound, never a 400.
const NumberSetting: Component<{
  label: string;
  help: string;
  value: () => number | undefined;
  min: number;
  max?: number;
  onSave: (v: number) => Promise<void>;
}> = (props) => {
  const [val, setVal] = createSignal(0);
  createEffect(() => {
    const v = props.value();
    if (v !== undefined) setVal(v);
  });
  const status = useSaveStatus();
  const outOfRange = () =>
    val() < props.min || (props.max !== undefined && val() > props.max);
  const save = async () => {
    if (outOfRange()) {
      status.failed(
        new Error(
          props.max !== undefined
            ? `must be between ${props.min} and ${props.max}`
            : `must be ${props.min} or greater`,
        ),
      );
      return;
    }
    try {
      await props.onSave(val());
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <div class="mb-3">
      <label class="block">
        <span class={labelClass}>{props.label}</span>
        <input
          type="number"
          class={`${inputClass} mt-1 !w-40`}
          min={props.min}
          max={props.max}
          aria-label={props.label}
          value={val()}
          onInput={(e) => setVal(Number(e.currentTarget.value))}
        />
      </label>
      <div class="mt-2 flex items-center gap-2">
        <Button variant="primary" onClick={() => void save()}>
          Save
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-1">{props.help}</Muted>
    </div>
  );
};

const IdentifyEnabledSetting: Component<{ mode: () => Mode }> = (props) => {
  const [current] = createResource(props.mode, fetchIdentifyEnabled);
  const [enabled, setEnabled] = createSignal(true);
  createEffect(
    on(current, (v) => {
      if (v !== undefined) setEnabled(v);
    }),
  );
  const status = useSaveStatus();
  const save = async () => {
    try {
      await putIdentifyEnabled(props.mode(), enabled());
      status.saved();
    } catch (e) {
      status.failed(e);
    }
  };
  return (
    <div class="mb-3">
      <label class="flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Adult phash-first identification enabled"
          checked={enabled()}
          onChange={(e) => setEnabled(e.currentTarget.checked)}
        />
        <span class="text-sm text-fg">
          Adult phash-first identification enabled
        </span>
      </label>
      <div class="mt-2 flex items-center gap-2">
        <Button variant="primary" onClick={() => void save()}>
          Save
        </Button>
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
      <Muted class="mt-1">
        When on, Adult Rename identifies scenes by perceptual hash first (no live
        Stash required). Turn off to skip identification.
      </Muted>
    </div>
  );
};

const AdvancedSection: Component<{ mode: () => Mode }> = (props) => {
  // recheck-interval is GLOBAL, not per-mode — fetched once, independent of the
  // mode tab.
  const [recheck] = createResource(fetchRecheckInterval);
  // phash-threshold is per-mode-generic; confidence is Movies/Series only;
  // identify-enabled is Adult only. Each keyed on the mode accessor.
  const [phash] = createResource(props.mode, fetchPHashThreshold);
  const [confidence] = createResource(
    () => (props.mode() === "adult" ? undefined : props.mode()),
    fetchConfidenceThreshold,
  );

  return (
    <Card title={`Advanced Settings (${MODE_LABELS[props.mode()]})`}>
      <NumberSetting
        label="Background recheck interval (seconds) — global"
        help="0 turns the background recheck job off (the opt-in default). Any positive number of seconds enables it; a change takes effect on the running loop's next tick, or on next restart if it was off at boot."
        value={() => recheck()}
        min={0}
        onSave={(v) => putRecheckInterval(v)}
      />
      <NumberSetting
        label="Dedup phash similarity threshold (0–64)"
        help="Per-frame average Hamming bits below which two files are treated as perceptual duplicates by Dedup. Lower is stricter."
        value={() => phash()}
        min={0}
        max={64}
        onSave={(v) => putPHashThreshold(props.mode(), v)}
      />
      <Show when={props.mode() !== "adult"}>
        <NumberSetting
          label="Rename match-confidence threshold (0–100)"
          help="Minimum TMDB match confidence (a percentage) before Rename auto-accepts a match instead of surfacing it for manual re-pick."
          value={() => confidence()}
          min={0}
          max={100}
          onSave={(v) => putConfidenceThreshold(props.mode(), v)}
        />
      </Show>
      <Show when={props.mode() === "adult"}>
        <IdentifyEnabledSetting mode={props.mode} />
      </Show>
    </Card>
  );
};

// ---- Settings root --------------------------------------------------------

// SECTION_TABS is the section-level tab set (distinct from the Movies/Series/
// Adult mode selector). Connections is first so it is the default tab — that
// keeps the safety-critical Connections table (and its three-state secret gate)
// on screen at mount with zero navigation.
const SECTION_TABS: TabDef[] = [
  { id: "connections", label: "Connections" },
  { id: "auth", label: "Auth" },
  { id: "ai", label: "AI" },
  { id: "library", label: "Library" },
  { id: "advanced", label: "Advanced" },
  { id: "sliders", label: "Sliders" },
];

// ModeSelector is the inline Movies/Series/Adult tab bar shared by the Library
// and Advanced sections. It is a plain ScreenTabBar (NOT registered with the
// shell) so it never competes with the section tabs for the shell's tab slot.
const ModeSelector: Component<{
  mode: () => Mode;
  onSelect: (m: Mode) => void;
}> = (props) => (
  <ScreenTabBar
    tabs={MODES}
    current={props.mode}
    onSelect={(id) => props.onSelect(id as Mode)}
    class="mb-4 flex gap-1"
  />
);

export const Settings: Component<{ onReboot: () => void }> = (props) => {
  const [section, setSection] = createSignal<string>("connections");
  const [mode, setMode] = createSignal<Mode>("movies");
  const perModeApplies = () => mode() !== "adult"; // library/quality/naming/kids

  return (
    <div>
      <h2 class="mb-4 text-lg font-semibold text-fg">Settings</h2>

      <ScreenTabs tabs={SECTION_TABS} current={section} onSelect={setSection} />

      <Show when={section() === "connections"}>
        <ConnectionsSection />
        <TraktConnectionSection />
      </Show>

      <Show when={section() === "auth"}>
        <AuthModeSection onReboot={props.onReboot} />
        <APIAccessSection />
      </Show>

      <Show when={section() === "ai"}>
        <AISection />
      </Show>

      <Show when={section() === "library"}>
        <ModeSelector mode={mode} onSelect={setMode} />
        <Show
          when={perModeApplies()}
          fallback={
            <Muted>
              Adult has no per-mode library, quality, naming, or kids settings —
              those apply to Movies and Series only. Adult's own settings live in
              the Advanced tab.
            </Muted>
          }
        >
          <LibraryRootFolderSection mode={mode} />
          <QualityPrefsSection mode={mode} />
          <NamingPresetSection mode={mode} />
          <KidsRootPathSection mode={mode} />
        </Show>
      </Show>

      <Show when={section() === "advanced"}>
        <ModeSelector mode={mode} onSelect={setMode} />
        <AdvancedSection mode={mode} />
      </Show>

      <Show when={section() === "sliders"}>
        <SliderAdminSection />
      </Show>
    </div>
  );
};
