// First-run setup wizard: the 3-way auth-mode selector (password / oidc /
// none). Ported from the current frontend's renderCredentialsGate(container,
// "setup"). This is the "not-set-up" boot branch — the instance has no login
// yet. Load-bearing behaviors carried over verbatim:
//
//   - OIDC setup does NOT re-run boot() on success. It reveals the one-time
//     break-glass API key, then navigates the WHOLE PAGE to
//     /api/auth/oidc/login to complete first login through the IdP. Calling
//     boot() instead would just re-show this screen (no session exists yet).
//   - Every button that isn't the form's submit control is type="button"
//     (our Button default). A submit-type button inside the form re-fires
//     submit(), whose first act clears the reveal panel — the live "copy
//     button breaks login" incident (index.html:2009).
//   - Password and none modes DO re-run boot() on success (a session cookie /
//     none-mode state now exists, so boot lands in the app).

import { type Component, createSignal, Show } from "solid-js";
import type { SetupRequest, SetupResponse } from "@dto";
import {
  AuthScreen,
  Button,
  ErrorText,
  Field,
  Muted,
  inputClass,
  labelClass,
} from "../components/ui";

type Mode = "password" | "oidc" | "none";

const NONE_CONFIRM =
  "This instance will have no authentication at all — anyone who can reach it will have full control. Continue?";

export const SetupWizard: Component<{ onSetupComplete: () => void }> = (
  props,
) => {
  const [mode, setMode] = createSignal<Mode>("password");
  const [error, setError] = createSignal("");
  // reveal holds the oidc break-glass response once setup succeeds; while set,
  // the form is locked and the "Continue to SSO login" button is shown.
  const [reveal, setReveal] = createSignal<SetupResponse | null>(null);

  const [username, setUsername] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [confirmPw, setConfirmPw] = createSignal("");

  const [issuer, setIssuer] = createSignal("");
  const [clientId, setClientId] = createSignal("");
  const [clientSecret, setClientSecret] = createSignal("");
  const [redirect, setRedirect] = createSignal("");

  const submitLabel = () =>
    mode() === "password"
      ? "Create login"
      : mode() === "oidc"
        ? "Save and continue to SSO"
        : "Continue without authentication";

  // postSetup sends the setup body and returns the Response, or null if the
  // caller already handled a validation short-circuit.
  const postSetup = async (body: SetupRequest): Promise<Response> =>
    fetch("/api/auth/setup", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });

  // doNone performs the none-mode setup (shared by the mode=none submit and the
  // password-mode "Skip" quick path).
  const doNone = async () => {
    setError("");
    if (!window.confirm(NONE_CONFIRM)) return;
    try {
      const res = await postSetup({
        username: "",
        password: "",
        mode: "none",
        acknowledgeInsecure: true,
      });
      if (!res.ok) {
        setError((await res.text()) || "HTTP " + res.status);
        return;
      }
      props.onSetupComplete();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const submit = async (e: Event) => {
    e.preventDefault();
    setError("");
    setReveal(null);

    const m = mode();
    if (m === "none") {
      await doNone();
      return;
    }

    if (m === "password") {
      if (password() !== confirmPw()) {
        setError("Passwords don't match.");
        return;
      }
      if (!username() || !password()) {
        setError("Username and password are both required.");
        return;
      }
      try {
        const res = await postSetup({
          username: username(),
          password: password(),
          mode: "password",
          acknowledgeInsecure: false,
        });
        if (!res.ok) {
          setError((await res.text()) || "HTTP " + res.status);
          return;
        }
        props.onSetupComplete();
      } catch (err) {
        setError((err as Error).message);
      }
      return;
    }

    // m === "oidc"
    const iss = issuer().trim();
    const cid = clientId().trim();
    const csecret = clientSecret();
    const redir = redirect().trim();
    if (!iss || !cid || !csecret || !redir) {
      setError(
        "Issuer URL, client ID, client secret, and redirect URL are all required.",
      );
      return;
    }
    try {
      const res = await postSetup({
        username: "",
        password: "",
        mode: "oidc",
        acknowledgeInsecure: false,
        oidcIssuerUrl: iss,
        oidcClientId: cid,
        oidcClientSecret: csecret,
        oidcRedirectUrl: redir,
      });
      if (!res.ok) {
        setError((await res.text()) || "HTTP " + res.status);
        return;
      }
      const isJSON = (res.headers.get("Content-Type") || "").includes(
        "application/json",
      );
      const data: SetupResponse = isJSON ? await res.json() : {};
      // Reveal-once, then send the browser to the IdP (NOT boot()).
      setReveal(data);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const [copyStatus, setCopyStatus] = createSignal<"idle" | "copied" | "failed">(
    "idle",
  );
  let copyStatusTimer: ReturnType<typeof setTimeout> | undefined;

  const copyKey = async () => {
    const key = reveal()?.apiKey;
    if (!key) return;
    clearTimeout(copyStatusTimer);
    try {
      if (!navigator.clipboard) throw new Error("clipboard API unavailable");
      await navigator.clipboard.writeText(key);
      setCopyStatus("copied");
    } catch {
      setCopyStatus("failed");
    }
    copyStatusTimer = setTimeout(() => setCopyStatus("idle"), 2000);
  };

  const copyLabel = () => {
    switch (copyStatus()) {
      case "copied":
        return "Copied!";
      case "failed":
        return "Couldn't copy — select the field instead";
      default:
        return "Copy";
    }
  };

  const downloadKey = () => {
    const key = reveal()?.apiKey;
    if (!key) return;
    const blob = new Blob([key + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "sakms-break-glass-api-key.txt";
    link.click();
    URL.revokeObjectURL(url);
  };

  return (
    <AuthScreen title="Create your SAK login">
      <Muted class="mb-4">
        This is the one login that protects this instance — anyone who can reach
        it can otherwise control every connected service. Pick how this instance
        authenticates now, before connecting anything.
      </Muted>

      <form onSubmit={submit}>
        <Field label="Authentication mode">
          <select
            class={inputClass}
            value={mode()}
            disabled={reveal() !== null}
            onChange={(e) => {
              setMode(e.currentTarget.value as Mode);
              setError("");
            }}
          >
            <option value="password">Password (username + password login)</option>
            <option value="oidc">OIDC / single sign-on</option>
            <option value="none">None (no authentication — not recommended)</option>
          </select>
        </Field>

        <Show when={mode() === "password"}>
          <Field label="Username">
            <input
              type="text"
              autocomplete="username"
              class={inputClass}
              value={username()}
              onInput={(e) => setUsername(e.currentTarget.value)}
            />
          </Field>
          <Field label="Password">
            <input
              type="password"
              autocomplete="new-password"
              class={inputClass}
              value={password()}
              onInput={(e) => setPassword(e.currentTarget.value)}
            />
          </Field>
          <Field label="Confirm password">
            <input
              type="password"
              autocomplete="new-password"
              class={inputClass}
              value={confirmPw()}
              onInput={(e) => setConfirmPw(e.currentTarget.value)}
            />
          </Field>
        </Show>

        <Show when={mode() === "oidc"}>
          <Muted class="mb-3">
            SAK runs a real OpenID Connect Authorization Code flow (with PKCE) as
            the Relying Party — it verifies the IdP's signed ID token, so no
            proxy-held shared secret is needed. Register the redirect URL below as
            an allowed callback in your IdP's client config.
          </Muted>
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
              class={inputClass}
              value={clientSecret()}
              onInput={(e) => setClientSecret(e.currentTarget.value)}
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
          <Muted class="mb-2">
            A one-time break-glass API key is shown after setup, in case SSO login
            ever fails and you need to reach Settings to fix or switch modes.
          </Muted>
        </Show>

        <Show when={mode() === "none"}>
          <Muted class="mb-3">
            No credentials, no header, nothing — anyone who can reach this
            instance has full control. You'll be asked to confirm before this
            takes effect.
          </Muted>
        </Show>

        <div class="mt-2 flex items-center gap-2">
          <Button type="submit" variant="primary" disabled={reveal() !== null}>
            {submitLabel()}
          </Button>
          {/* Skip is a quick none-mode path, meaningful only while looking at
              the password default (none already has its own submit). */}
          <Show when={mode() === "password"}>
            <Button onClick={doNone}>Skip — no authentication</Button>
          </Show>
        </div>

        {error() && <ErrorText>{error()}</ErrorText>}

        <Show when={reveal()}>
          {(data) => (
            <div class="mt-4 rounded-md border border-border bg-bg p-3">
              <Show when={data().apiKey}>
                <span class={labelClass}>
                  Break-glass API key (save this now):
                </span>
                <input
                  type="text"
                  readonly
                  class={`${inputClass} mt-1`}
                  value={data().apiKey}
                />
                <div class="mt-2 flex items-center gap-2">
                  <Button onClick={copyKey}>{copyLabel()}</Button>
                  <Button onClick={downloadKey}>Download as text file</Button>
                </div>
                <ErrorText>
                  Shown once — if SSO login ever fails, send this as an X-Api-Key
                  header to reach Settings. It cannot be retrieved later.
                </ErrorText>
              </Show>
              <Show when={!data().apiKey && data().apiKeyNote}>
                <Muted>{data().apiKeyNote}</Muted>
              </Show>
              <div class="mt-3">
                <Button
                  variant="primary"
                  onClick={() => {
                    window.location.href = "/api/auth/oidc/login";
                  }}
                >
                  Continue to SSO login
                </Button>
              </div>
            </div>
          )}
        </Show>
      </form>
    </AuthScreen>
  );
};
