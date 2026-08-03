# Break-glass recovery after a wipe

Operational runbook. Follow this cold, during an incident, without prior
context. It covers the one case where you cannot log in to SAK through the
normal UI and need the out-of-band recovery credential.

## When you need this

**Corrected 2026-07-13**: as of 2026-07-12, `sakms-auto-update.py`'s `deploy()`
no longer wipes `/mnt/iscsi/sakms` on a *normal* successful deploy — that
wipe-every-run pre-alpha policy was removed (Wade's explicit ask, see the
script's own header comment on server1). Data now only gets wiped in two
cases: **(1)** the failure-path `rollback()` still wipes unconditionally
before restoring the last-known-good version, and **(2)** a manual, opt-in
`--wipe-data` CLI flag run directly on server1 (not reachable via the
systemd service). A normal, successful deploy — even one that ships a
runtime-broken frontend shell — does **not** wipe anything, because
`health_ok()` only checks the unconditional `GET /healthz` and never
exercises the frontend/auth-boot path, so it can report "succeeded" on a
broken build without ever reaching the rollback (and therefore the wipe)
path at all.

This means the scenario below is now **two separate cases with different
recovery paths**, not one:

- **A rolled-back or manually-wiped instance** genuinely has an empty auth
  store, and SAK regenerates `secret.key` + mints a **fresh `X-Api-Key`**,
  logged **exactly once**:

  ```
  API key generated (shown once, store it now): <KEY>
  ```

  (Source: `cmd/sakms/main.go:104`, gated on `internal/auth`'s
  `EnsureAPIKey` returning non-empty — only true when the store is empty,
  i.e. first boot, post-rollback, or post-manual-wipe. A normal successful
  deploy does **not** hit this path and logs nothing new.)

- **A normal deploy that ships a runtime-broken shell** leaves the *existing*
  auth store and `X-Api-Key` completely intact — no new key is minted, no
  data is lost, but the frontend/auth-boot may be too broken to use the UI
  at all. The **already-existing** `X-Api-Key` (from whenever the instance
  was last actually rolled-back or wiped — possibly a long time ago) is
  still the recovery credential in this case; you just aren't looking for a
  *freshly logged* one.

There is deliberately **no `/legacy` frontend fallback** (dead code, against
this project's conventions), so the `X-Api-Key` — fresh or pre-existing,
depending on which case above applies — is the actual safety net for the
frontend cutover.

## Step 1 — Retrieve the key from the container log

On server1 (`root@10.1.10.3`), grep the container log **narrowly** — the key
is a full-access secret and the log ships to OpenObserve (O2); do not dump the
whole log:

```sh
ssh root@10.1.10.3 'docker logs sakms 2>&1 | grep "API key generated"'
```

Expected output — one line, the key after the colon:

```
API key generated (shown once, store it now): 1DHqR_96XYBzYrDtPMz645iN2q8iqj2HUayON0si8MA
```

Notes:
- **The line is logged once, at the first start after the wipe.** If the
  container has restarted since (without a wipe) and the original start has
  scrolled out of Docker's retained logs, query O2 instead for that exact
  string around the deploy time (`o2cli grep "API key generated" --since <window>`
  from wade-pc). It is the same secret until the next wipe.
- **If instead you see `API key: using SAKMS_API_KEY from environment`**, the
  key was **not** generated — `SAKMS_API_KEY` is set in the container's
  environment/compose, and the real key is whatever that env var holds. Do not
  look for a generated key; retrieve the configured value from its source
  (e.g. BW SM / the compose env on server1) instead.

## Step 2 — Figure out which state you're in, then recover

First check the instance state — `GET /api/auth/status` is **public** (no key
needed):

```sh
curl -s https://media-admin.zaena.us/api/auth/status
# {"configured":false|true,"authenticated":false,"mode":"password|oidc|none"}
```

The right recovery depends on `configured`, and **the break-glass key is only
actually required in Case B** — a fresh post-rollback/post-wipe instance
(Case A) recovers through public endpoints. **`configured:false` is no
longer the normal state after an ordinary deploy** (see the corrected note
above — normal deploys preserve data now); you'll see it after a
`rollback()` or a manual `--wipe-data` run specifically, not routinely.

### Case A — `configured:false` (after a rollback or manual wipe only)

A wiped/rolled-back instance has no login yet, so boot lands on the **public
setup wizard**. If the wizard renders, just use it — no key needed. If the
wizard shell is broken at runtime, POST the setup body directly. This route
is public (first-run bootstrap) — password mode returns `204` and configures
the operator login in one call:

```sh
curl -X POST https://media-admin.zaena.us/api/auth/setup \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"<new-password>","mode":"password","acknowledgeInsecure":false}'
```

Then log in normally at the password screen. (For an OIDC setup instead, send
`"mode":"oidc"` with the four `oidc*` fields; that response returns its own
one-time break-glass key for the newly-configured instance.)

### Case B — `configured:true` but locked out (this is what the key is for)

The instance is set up but you cannot get a session — e.g. it's in `oidc` mode
and SSO is failing. The key authenticates the protected recovery routes that a
missing session cookie otherwise blocks.

**Through the UI (preferred):** the login screen's **"Trouble logging in?"**
section takes the key directly (see `frontend/src/screens/OidcLogin.tsx`):

1. Open `https://media-admin.zaena.us` — the SSO notice renders even when SSO
   itself is broken.
2. Expand **"Trouble logging in?"**, paste the key, click **Unlock**.
3. Either fix the OIDC config and **Save fix**, or **Switch to password mode
   instead**.

**By curl** (if the shell is too broken to use): fix the OIDC config with the
key so SSO works again —

```sh
curl -X PUT https://media-admin.zaena.us/api/auth/oidc \
  -H "X-Api-Key: <KEY>" -H "Content-Type: application/json" \
  -d '{"issuerUrl":"...","clientId":"...","clientSecret":"...","redirectUrl":".../api/auth/oidc/callback"}'
```

**Gotcha — switching to password mode has a precondition.** `PUT
/api/auth/mode {"mode":"password",...}` returns **400** ("password auth is not
configured yet") unless a password hash already exists — `SetAuthMode` never
strands you in a mode with no way in (`internal/api/authmode.go`). So only use
the mode-switch if a password was previously set:

```sh
curl -X PUT https://media-admin.zaena.us/api/auth/mode \
  -H "X-Api-Key: <KEY>" -H "Content-Type: application/json" \
  -H "X-Section-Pin: <PIN>" \
  -d '{"mode":"password","acknowledgeInsecure":false}'
```

**Second gotcha — `X-Section-Pin` is required if a section PIN is set.** This
route is the section lock's own disarm surface: switching to `none` makes the
lock inert, so without a gate here one request would permanently disarm it.
Omit the header when no PIN is configured (the route then behaves exactly as
it always did); a missing header while a PIN IS set answers **400** ("a PIN is
required") and a wrong one answers **403** `section_locked`, escalating to
**429** after five consecutive wrong attempts.

**If the PIN itself is forgotten**, restart the container with
`SAKMS_SECTION_LOCK_DISABLE=1` in its environment. That disarms the section
lock entirely — this route stops requiring the header, no section is enforced,
and `PUT`/`DELETE /api/section-lock/pin` stop requiring `currentPin`, which is
the only way out of a corrupt PIN hash. It needs host access, which Step 1
above already required, so it adds inconvenience rather than a new access
class.

If no password exists and OIDC can't be fixed quickly, switching to `none`
mode (`{"mode":"none","acknowledgeInsecure":true}`) restores access with **no
authentication at all** — acceptable only briefly, behind the internal-only
Traefik/CrowdSec middleware, and reverted the moment real auth is back.

(All auth-gated status calls — e.g. `GET /api/setup/status` — likewise need
the `X-Api-Key` header; `GET /api/auth/status` above is the one public probe.)

## Step 3 — After recovery

- The generated key is **ephemeral**: the next deploy wipes `secret.key` and
  mints a new one, so there is no long-lived secret to rotate here. If you
  needed a stable out-of-band key across deploys, set `SAKMS_API_KEY` in the
  container environment (then the "using SAKMS_API_KEY from environment" branch
  applies and no key is ever logged).
- Treat any key you copied out of the log as sensitive while it's live: it
  grants full operator access until the next wipe.
