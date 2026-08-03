// HTTP client for SAK's /api/* surface. Ported near-verbatim from the current
// vanilla-JS frontend (internal/web/static/index.html's api()/apiWithKey()) —
// the behavior here is auth-boot-load-bearing, so it is a faithful port, not a
// reimplementation. Two subtleties carried over intact:
//
//   1. A 401 on any path that is NOT under /api/auth/ means the session
//      expired (or never existed). The safe recovery is to re-run the boot
//      sequence, which re-checks /api/auth/status and shows the login screen
//      instead of a broken view. Because this module must not import the boot
//      logic (circular), it calls a registered onSessionExpired handler that
//      App wires to re-boot. The /api/auth/ prefix is EXEMPT so that (a) a
//      status/login 401 can't loop boot, and (b) break-glass recovery calls
//      (wrong key -> 401 on /api/auth/oidc or /api/auth/mode) surface their
//      error inline where the recovery form can show it, instead of resetting.
//
//   2. api()'s options merge is SHALLOW (Object.assign): passing opts.headers
//      REPLACES the default header object. apiWithKey() therefore sets
//      Content-Type explicitly when it injects X-Api-Key, or the JSON content
//      type would be dropped.
//
//   3. (added with the section PIN lock) A section-lock refusal is a 403 with
//      a body code, NOT a 401, and is handled on a completely separate branch
//      from note 1 above — see SectionLockedError. Keeping those two apart is
//      the whole reason the lock answers 403: routing it through note 1 would
//      reboot the SPA every time an operator browsed into a locked section.

// ApiError is what api() throws on a non-ok response: a plain Error (same
// message, so every existing `catch (e) { (e as Error).message }` and every
// `rejects.toThrow("…")` keeps working) that additionally carries the HTTP
// status. It exists because some routes answer with several DISTINCT non-ok
// meanings that a caller must tell apart, and the message alone can't do it
// without matching on a server-side string literal that nothing pins:
// PUT /api/downloader/config answers 400 for a validation failure but 409 for
// "the engine can't restart right now, nothing was saved, retry in a moment" —
// a retryable not-now, not a failure. Callers branch on `.status`, never on
// message text.
export class ApiError extends Error {
  readonly status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// SectionLockedError is what api() throws when the section PIN lock refuses a
// request: a 403 whose JSON body carries code "section_locked" — the single
// rejection shape internal/auth's gate and internal/api/sectionlock.go both
// write. It extends ApiError (status 403) so every existing `catch (e) {
// (e as Error).message }` and every `.status` branch keeps working unchanged.
//
// THE DISCRIMINATOR IS THE BODY CODE, NOT THE STATUS, AND THAT IS
// LOAD-BEARING. A section-lock refusal must never reach the 401 branch above:
// that branch reboots the whole SPA, so a locked Adult poster would log the
// operator out. Two independent things keep them apart — the lock answers 403
// (never 401, a deliberate spec deviation recorded in the plan's §10.1
// precisely because of the reboot), and this branch additionally requires
// code === "section_locked", so a plain 403 from any other route still throws
// an ordinary ApiError. The 401 branch returns before the body is ever parsed;
// this one runs only after it, inside the !res.ok arm.
export class SectionLockedError extends ApiError {
  // section is the locked section id the server named, or "" when the refusal
  // was of the lock's own control surface (which classifies as no section).
  readonly section: string;
  constructor(message: string, section: string) {
    super(message, 403);
    this.name = "SectionLockedError";
    this.section = section;
  }
}

// SectionLockLockoutError is the brute-force refusal: 429 with code
// "section_lock_lockout" and a retryAfter in seconds. It is deliberately a
// DIFFERENT class from SectionLockedError — an operator who is locked out
// needs to be told to wait, not shown a PIN box that will refuse them again.
//
// This shape has no internal/apidto mirror (it is not in the plan's §6 route
// table), so it is hand-written here, next to the code that parses it, rather
// than imported from @dto.
export class SectionLockLockoutError extends ApiError {
  readonly retryAfter: number;
  constructor(message: string, retryAfter: number) {
    super(message, 429);
    this.name = "SectionLockLockoutError";
    this.retryAfter = retryAfter;
  }
}

let sessionExpiredHandler: (() => void) | null = null;
let sectionLockedHandler: ((section: string) => void) | null = null;

// setOnSessionExpired registers the callback api() invokes when a non-/api/auth
// request returns 401. App uses it to re-run the boot sequence (falling back to
// the login branch) without this module importing the boot code.
export function setOnSessionExpired(handler: (() => void) | null): void {
  sessionExpiredHandler = handler;
}

// setOnSectionLocked registers the callback api() invokes when a request is
// refused by the section lock. Mirrors setOnSessionExpired's shape and exists
// for the same reason (this module must not import the shell): AppShell wires
// it to refetch the lock status, so a mid-session ticket expiry raises the PIN
// overlay instead of failing silently. Unlike the session-expiry handler this
// one never reboots anything — the operator stays logged in.
export function setOnSectionLocked(
  handler: ((section: string) => void) | null,
): void {
  sectionLockedHandler = handler;
}

// api issues a JSON request to path and returns the parsed body (null on 204).
// A non-ok response throws an Error carrying the server's message. A 401 on a
// non-/api/auth path triggers the registered session-expiry handler and throws.
export async function api<T = unknown>(
  path: string,
  opts?: RequestInit,
): Promise<T> {
  const res = await fetch(
    path,
    Object.assign({ headers: { "Content-Type": "application/json" } }, opts),
  );
  if (res.status === 401 && !path.startsWith("/api/auth/")) {
    if (sessionExpiredHandler) sessionExpiredHandler();
    throw new Error("session expired");
  }
  if (res.status === 204) return null as T;
  const isJSON = (res.headers.get("Content-Type") || "").includes(
    "application/json",
  );
  const body: unknown = isJSON ? await res.json() : await res.text();
  if (!res.ok) {
    const msg: string =
      typeof body === "string"
        ? body
        : ((body as { error?: string } | null)?.error ?? JSON.stringify(body));
    const code: string =
      typeof body === "string"
        ? ""
        : ((body as { code?: string } | null)?.code ?? "");
    if (res.status === 403 && code === "section_locked") {
      const section: string =
        typeof body === "string"
          ? ""
          : ((body as { section?: string } | null)?.section ?? "");
      if (sectionLockedHandler) sectionLockedHandler(section);
      throw new SectionLockedError(msg || "section locked", section);
    }
    if (res.status === 429 && code === "section_lock_lockout") {
      const retryAfter: number =
        typeof body === "string"
          ? 0
          : ((body as { retryAfter?: number } | null)?.retryAfter ?? 0);
      throw new SectionLockLockoutError(
        msg || "too many failed PIN attempts",
        retryAfter,
      );
    }
    throw new ApiError(msg || "HTTP " + res.status, res.status);
  }
  return body as T;
}

// apiWithKey is api() authenticated by an explicit break-glass API key
// (X-Api-Key header) instead of the session cookie — the recovery path for an
// operator locked out of SSO (the server checks this header independent of the
// active auth mode). It delegates to api() to reuse its parsing/error logic;
// only the auth mechanism differs. Content-Type MUST be set explicitly here
// because api()'s options merge is shallow (see note 2 above). A wrong key
// comes back 401; since every recovery call targets an /api/auth/ path, api()'s
// 401 -> session-expiry reset is skipped, leaving the error to surface inline.
export async function apiWithKey<T = unknown>(
  path: string,
  key: string,
  opts?: RequestInit,
): Promise<T> {
  const headers = Object.assign(
    { "Content-Type": "application/json", "X-Api-Key": key },
    opts?.headers as Record<string, string> | undefined,
  );
  return api<T>(path, Object.assign({}, opts, { headers }));
}
