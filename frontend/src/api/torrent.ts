// Torrent-engine configuration data access — GET/PUT /api/downloader/config,
// the single settings document behind Settings -> Download -> Torrent
// (Torrent.tsx). Every
// call goes through api() (src/api/client.ts) so it inherits the session cookie
// and the global 401 → re-boot session-expiry fallback. The request/response
// shape is the generated DTO (@dto), never hand-duplicated.
//
// Kept out of api/downloads.ts deliberately: that module is the live queue
// (/api/downloads — list, pause/resume, cancel), a different area. This one is
// the engine's operator-tunable settings.
//
// PUT IS A FULL-DOCUMENT REPLACE, NOT A PATCH. Every DTO field is a plain
// non-pointer, non-omitempty Go type, so an omitted field arrives as its zero
// value and OVERWRITES the stored one. That fails in two ways, and the quiet one
// is worse: omitting maxConcurrent/maxConnections/listenPort is a loud 400,
// while omitting dhtEnabled/pexEnabled/downloadRateLimitBytes/seedRatioLimit/
// seedDurationMinutes/staleThresholdMinutes SILENTLY disables or zeroes them.
// putDownloaderConfig therefore takes the WHOLE DownloaderConfig — not a
// Partial — and serializes it verbatim. Do NOT build the body key-by-key here:
// enumerating fields reintroduces the same silent-corruption bug the moment a
// new field is added to the DTO.
//
// The caller's contract follows from that: GET the document, mutate the fields
// it edits, PUT the whole thing back. Never construct a config object from
// scratch out of the controls a screen happens to render.
//
// PUT answers 200 with a DownloaderConfigApplyResult body ({applied,
// restartRequired, message}) — NOT 204. The save does not merely store the
// document, it applies it to the running torrent engine first, so the response
// says WHICH apply path ran: "live" (patched in place, nothing interrupted) or
// "rebuilt" (the engine was torn down and stood back up, briefly pausing every
// in-flight download). `message` is operator-facing copy meant for the save
// status line verbatim — render it rather than writing a client-side
// equivalent, so the UI can't drift from what actually happened.
//
// THE PUT HAS THREE DISTINCT OUTCOMES, and 409 is not a generic error:
//   200 — applied and persisted; the body says live vs. rebuilt.
//   400 — validation failure (plain text). Nothing was applied or stored.
//   409 — the engine REFUSED to rebuild right now, e.g. a torrent is still
//         fetching its metadata and a rebuild can't snapshot what it doesn't
//         have yet. Nothing was applied and nothing was persisted, the message
//         names the blocking download, and the same save succeeds once that
//         download moves on. Surfacing this as a plain error would tell the
//         operator their settings are wrong when the settings are fine and the
//         timing isn't. Use isRebuildRefused() to tell it apart — by status,
//         never by matching the message text.

import { ApiError, api } from "./client";
import type { DownloaderConfig, DownloaderConfigApplyResult } from "@dto";

export type { DownloaderConfig, DownloaderConfigApplyResult };

// fetchDownloaderConfig reads the whole downloader settings document. Note that
// stagingDir comes back "" when unset — a normal "not configured" state (the
// handler has no data directory to synthesize the boot default from), not an
// error condition.
export function fetchDownloaderConfig(): Promise<DownloaderConfig> {
  return api<DownloaderConfig>("/api/downloader/config");
}

// putDownloaderConfig replaces the whole downloader settings document. Pass the
// object returned by fetchDownloaderConfig with the edited fields changed — a
// partial object is a type error here precisely because it would be a silent
// data-loss bug at runtime (see the module note above). Resolves with the
// apply-result the server returns; a 400 validation failure and a 409 refusal
// both throw (see isRebuildRefused for the difference).
export function putDownloaderConfig(
  cfg: DownloaderConfig,
): Promise<DownloaderConfigApplyResult> {
  return api<DownloaderConfigApplyResult>("/api/downloader/config", {
    method: "PUT",
    body: JSON.stringify(cfg),
  });
}

// isRebuildRefused reports whether a putDownloaderConfig rejection was the
// engine's retryable "not right now" (409) rather than a real failure. The
// caller's own copy should say the save can simply be retried; the thrown
// error's message names the download that blocked it.
export function isRebuildRefused(e: unknown): boolean {
  return e instanceof ApiError && e.status === 409;
}
