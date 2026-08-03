// SSO login notice shown in oidc mode when there is no valid session yet.
// Ported from the current frontend's renderOIDCLoginNotice + renderRecoveryFix.
//
// Load-bearing details carried over verbatim:
//   - "Log in with SSO" is a FULL-PAGE navigation (window.location) to
//     /api/auth/oidc/login, NOT a fetch/XHR — the OIDC Authorization Code flow
//     is a top-level browser redirect to the IdP and back.
//   - The break-glass recovery <details> is ALWAYS rendered, never gated on
//     authError: the worst failure (the IdP rejecting a malformed redirect_uri)
//     happens entirely IdP-side and never redirects back with any query param,
//     so recovery must be reachable even on a clean URL.
//   - Recovery calls use apiWithKey (X-Api-Key header) — the browser can attach
//     that header on a JS request even when it can't on a top-level navigation.
//     A wrong key comes back 401 on an /api/auth/ path, which api() leaves to
//     surface inline (it never trips the global session-expiry reset).

import { type Component, createSignal, Show } from "solid-js";
import type {
  AuthModeRequest,
  OIDCConfigRequest,
  OIDCStatusResponse,
} from "@dto";
import { apiWithKey } from "../api/client";
import { authErrorMessage } from "../auth/messages";
import {
  AuthScreen,
  Button,
  ErrorText,
  Field,
  Muted,
  inputClass,
} from "../components/ui";

const gotoSSO = () => {
  window.location.href = "/api/auth/oidc/login";
};

export const OidcLogin: Component<{
  authError: string | null;
  onSwitchedToPassword: () => void;
}> = (props) => {
  const [key, setKey] = createSignal("");
  const [unlockError, setUnlockError] = createSignal("");
  // cfg holds the OIDC config once the break-glass key unlocks it; while set,
  // the repair form is shown.
  const [cfg, setCfg] = createSignal<OIDCStatusResponse | null>(null);

  const unlock = async (e: Event) => {
    e.preventDefault();
    setUnlockError("");
    setCfg(null);
    const k = key().trim();
    if (!k) {
      setUnlockError("Enter your break-glass API key.");
      return;
    }
    try {
      const config = await apiWithKey<OIDCStatusResponse>("/api/auth/oidc", k);
      setCfg(config);
    } catch (err) {
      setUnlockError("✗ " + (err as Error).message);
    }
  };

  return (
    <AuthScreen title="Log in to SAK">
      <Show when={props.authError}>
        {(code) => <ErrorText>{authErrorMessage(code())}</ErrorText>}
      </Show>
      <Muted class="mb-4 mt-2">
        This instance uses single sign-on. Log in through your identity provider
        to continue.
      </Muted>
      <Button variant="primary" onClick={gotoSSO}>
        Log in with SSO
      </Button>

      <Muted class="mt-4">
        If SSO login keeps failing and you saved the one-time break-glass API key
        from setup, expand "Trouble logging in?" below to paste it and fix the
        OIDC config (or switch to password mode) here. A header-capable client
        (e.g. curl) sending it as an X-Api-Key header works too.
      </Muted>

      <details class="mt-4">
        <summary class="cursor-pointer text-sm text-fg">
          Trouble logging in?
        </summary>
        <Muted class="mt-2">
          Paste the one-time break-glass API key you saved at setup to recover
          access without SSO.
        </Muted>
        <form onSubmit={unlock}>
          <Field label="Break-glass API key">
            <input
              type="password"
              placeholder="break-glass API key"
              class={inputClass}
              value={key()}
              onInput={(e) => setKey(e.currentTarget.value)}
            />
          </Field>
          <Button type="submit">Unlock</Button>
          {unlockError() && <ErrorText>{unlockError()}</ErrorText>}
        </form>

        <Show when={cfg()}>
          {(config) => (
            <RecoveryFix
              apiKey={key().trim()}
              cfg={config()}
              onSwitchedToPassword={props.onSwitchedToPassword}
            />
          )}
        </Show>
      </details>
    </AuthScreen>
  );
};

// RecoveryFix is the post-unlock repair form: the same four OIDC fields as the
// setup screen, plus a "switch to password mode instead" escape hatch — all
// authenticated by the break-glass key, not a session cookie.
//
// NOTE (ported edge case, surfaced deliberately): the client-secret field is
// shown with an "unchanged (configured)" placeholder when a secret already
// exists, but the backend's PUT /api/auth/oidc has NO preserve-secret mode
// (OIDCConfigRequest.ClientSecret is required non-empty). So a "Save fix" with
// the secret left blank 400s — the operator must re-enter the secret. This
// matches the current frontend's exact behavior; it is faithfully ported, not
// newly introduced.
const RecoveryFix: Component<{
  apiKey: string;
  cfg: OIDCStatusResponse;
  onSwitchedToPassword: () => void;
}> = (props) => {
  const [issuer, setIssuer] = createSignal(props.cfg.issuerUrl || "");
  const [clientId, setClientId] = createSignal(props.cfg.clientId || "");
  const [secret, setSecret] = createSignal("");
  const [redirect, setRedirect] = createSignal(props.cfg.redirectUrl || "");
  const [saveError, setSaveError] = createSignal("");
  const [saved, setSaved] = createSignal(false);
  const [switchError, setSwitchError] = createSignal("");
  // Section PIN lock, §4.4: PUT /api/auth/mode is the lock's own disarm
  // surface (switching to "none" makes the lock inert), so it requires the PIN
  // whenever one is set. Optional here because most instances have no PIN, and
  // this form is the pre-session recovery path — it must stay usable on an
  // instance that never enabled the feature. Sent as a header, not a body
  // field, because AuthModeRequest is a generated DTO with a fixed shape.
  const [sectionPin, setSectionPin] = createSignal("");

  const saveFix = async (e: Event) => {
    e.preventDefault();
    setSaveError("");
    setSaved(false);
    const body: OIDCConfigRequest = {
      issuerUrl: issuer(),
      clientId: clientId(),
      clientSecret: secret(),
      redirectUrl: redirect(),
    };
    try {
      await apiWithKey("/api/auth/oidc", props.apiKey, {
        method: "PUT",
        body: JSON.stringify(body),
      });
      setSaved(true);
    } catch (err) {
      setSaveError("✗ " + (err as Error).message);
    }
  };

  const switchToPassword = async () => {
    setSwitchError("");
    const body: AuthModeRequest = { mode: "password", acknowledgeInsecure: false };
    try {
      // The header is omitted entirely when the field is blank: an empty
      // X-Section-Pin is not a wrong PIN and must never count as a failed
      // attempt against the brute-force counter.
      const pin = sectionPin().trim();
      await apiWithKey("/api/auth/mode", props.apiKey, {
        method: "PUT",
        body: JSON.stringify(body),
        ...(pin ? { headers: { "X-Section-Pin": pin } } : {}),
      });
      // Re-gate from scratch: mode is now password, so boot() lands on the
      // password login screen (this key-holder has no session cookie).
      props.onSwitchedToPassword();
    } catch (err) {
      setSwitchError("✗ " + (err as Error).message);
    }
  };

  return (
    <div class="mt-4 rounded-md border border-border bg-bg p-3">
      <form onSubmit={saveFix}>
        <Field label="Issuer URL">
          <input
            type="text"
            placeholder="https://sso.example.com/application/o/sakms/"
            class={inputClass}
            value={issuer()}
            onInput={(e) => setIssuer(e.currentTarget.value)}
          />
        </Field>
        <Field label="Client ID">
          <input
            type="text"
            class={inputClass}
            value={clientId()}
            onInput={(e) => setClientId(e.currentTarget.value)}
          />
        </Field>
        <Field label="Client secret">
          <input
            type="password"
            placeholder={
              props.cfg.hasSecret ? "unchanged (configured)" : "client secret"
            }
            class={inputClass}
            value={secret()}
            onInput={(e) => setSecret(e.currentTarget.value)}
          />
        </Field>
        <Field label="Redirect URL">
          <input
            type="text"
            placeholder="https://media-admin.example.com/api/auth/oidc/callback"
            class={inputClass}
            value={redirect()}
            onInput={(e) => setRedirect(e.currentTarget.value)}
          />
        </Field>
        <Button type="submit">Save fix</Button>
        {saveError() && <ErrorText>{saveError()}</ErrorText>}
      </form>

      <Show when={saved()}>
        <div class="mt-2">
          <Button variant="primary" onClick={gotoSSO}>
            Saved — try logging in again
          </Button>
        </div>
      </Show>

      <div class="mt-3">
        <Field label="Section PIN (only if one is set)">
          <input
            type="password"
            placeholder="leave blank if no section PIN is configured"
            class={inputClass}
            name="section-pin"
            autocomplete="new-password"
            value={sectionPin()}
            onInput={(e) => setSectionPin(e.currentTarget.value)}
          />
        </Field>
        <p class="mb-2 text-xs text-muted">
          PUT /api/auth/mode is gated by the section PIN, because switching to
          "none" would disarm the section lock. Not needed unless a PIN is set;
          if it is forgotten, restart with SAKMS_SECTION_LOCK_DISABLE=1.
        </p>
      </div>

      <div class="mt-3 flex items-center gap-2">
        <Button onClick={switchToPassword}>
          Switch to password mode instead
        </Button>
      </div>
      {switchError() && <ErrorText>{switchError()}</ErrorText>}
    </div>
  );
};
