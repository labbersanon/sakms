// Trakt (Watchlist) connection card.
//
// Trakt was never a row in the old Connections table — it needs an OAuth
// device-code flow (user_code + verification_url + poll-until-linked) on top of
// the plain client_id/client_secret save, which the generic table has no room
// for. It has always been its own Card with its own three-state secret gate
// (buildTraktCredentialsBody mirrors buildConnectionUpsertBody).
//
// Moved verbatim out of Connections.tsx (where it was module-private) when the
// Connections tab was dismantled; it now renders under Settings -> UI ->
// Discover, next to the other controls that shape what appears on the Discover
// screen. Behaviour is deliberately unchanged, including the batched save: the
// caller MUST keep it inside a <SectionSave>, or useSectionSaveItem falls back
// to returning false, this card's own "Save credentials" button reappears, and
// it silently drops out of the section's batch. See UI.tsx's wrapper.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  onCleanup,
  Show,
} from "solid-js";
import {
  buildTraktCredentialsBody,
  disconnectTrakt,
  fetchTraktStatus,
  pollTraktDevice,
  saveTraktCredentials,
  startTraktDeviceFlow,
  type TraktDeviceStartResponse,
} from "../../api/trakt";
import { Button, ErrorText, inputClass, labelClass, Muted } from "../../components/ui";
import { Card, SaveStatus, useSaveStatus, useSectionSaveItem } from "./shared";

export const TraktConnectionSection: Component = () => {
  const [status, { refetch }] = createResource(fetchTraktStatus);
  const [clientId, setClientId] = createSignal("");
  const [clientSecret, setClientSecret] = createSignal("");
  const [secretTouched, setSecretTouched] = createSignal(false);
  // dirty flips true on any credential edit and resets when fresh server state
  // arrives or a save succeeds — same role as ConnectionRow's dirty signal, so
  // the enclosing section's one Save button can drive Trakt as one batched item.
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
  // Trakt folds into the enclosing section's one Save button as one batched item
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
          {/* Own Save button only when standalone; inside a SectionSave the one
              section button commits Trakt too. */}
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
