// Auth section — the Authentication mode selector (password / oidc / none, with
// the OIDC config form) and the API Access break-glass key panel, rendered
// together under the Settings "Auth" tab. Extracted from the original single-file
// Settings.tsx.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  Show,
} from "solid-js";
import {
  fetchAPIKeyStatus,
  fetchAuthMode,
  fetchOIDCStatus,
  putAuthMode,
  putOIDCConfig,
  regenerateAPIKey,
} from "../../api/settings";
import {
  Button,
  ErrorText,
  Muted,
  inputClass,
  labelClass,
} from "../../components/ui";
import { Card, SaveStatus, useSaveStatus } from "./shared";

export const APIAccessSection: Component = () => {
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

export const AuthModeSection: Component<{ onReboot: () => void }> = (props) => {
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

  // Section PIN lock, §4.4: PUT /api/auth/mode is the lock's own disarm surface
  // (switching to "none" makes the lock inert), so the backend requires the PIN
  // whenever one is set — and refuses with a 400 "a PIN is required", not the
  // 403 section_locked shape the overlay reacts to. Without this field the
  // Settings-side switcher is simply unusable on any instance with a section
  // PIN, with an error message the screen offers no way to satisfy.
  //
  // Optional, and deliberately not conditioned on GET /api/section-lock/status:
  // most instances have no PIN, the field costs nothing when unused, and this
  // mirrors the pattern OidcLogin.tsx's pre-session recovery form already
  // established — which cannot read that status at all.
  const [sectionPin, setSectionPin] = createSignal("");

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
      await putAuthMode(
        {
          mode: body.mode,
          acknowledgeInsecure: body.acknowledgeInsecure ?? false,
        },
        sectionPin(),
      );
      // Clear the entered PIN on success, matching the PIN-set/PIN-clear
      // handlers in SectionLock.tsx. onReboot() unmounts this anyway, but a
      // PIN left sitting in component state is the wrong default to keep.
      setSectionPin("");
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

      <label class="mt-3 block">
        <span class={labelClass}>Section PIN (only if one is set)</span>
        <input
          type="password"
          class={`${inputClass} mt-1`}
          placeholder="leave blank if no section PIN is configured"
          name="section-pin"
          autocomplete="new-password"
          value={sectionPin()}
          onInput={(e) => setSectionPin(e.currentTarget.value)}
        />
      </label>
      <Muted class="mb-2">
        Switching auth mode is gated by the section PIN, because switching to
        "None" would disarm the section lock. Not needed unless a PIN is set; if
        it's forgotten, restart with SAKMS_SECTION_LOCK_DISABLE=1.
      </Muted>

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
