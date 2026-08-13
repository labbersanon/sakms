// Shared Discover machinery used by BOTH the Mainstream and Adult sub-screens:
// the grab pipeline (GrabTarget → GrabDialog → auto-grab / manual FallbackPickList),
// the not-configured setup modal (ConfigureConnectionModal + its detection helper),
// the TextPoster art fallback, and the generic PaginatedStrip pagination engine.
// These were extracted verbatim from the original single-file Discover.tsx — they
// are pieces already shared within that file, relocated, not newly abstracted.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  on,
  onCleanup,
  onMount,
  createUniqueId,
  For,
  Show,
  Switch,
  Match,
} from "solid-js";
import {
  type Mode,
  type SearchReleaseResult,
  fetchReleaseOptions,
} from "../../api/discover";
import {
  type AutoGrabCandidate,
  type AutoGrabRequest,
  type AutoGrabResponse,
  autoGrab,
  libraryRootFolder,
  manualGrab,
} from "../../api/grab";
import { Button, ErrorText, Muted } from "../../components/ui";
import { ViewAllLink } from "../../components/ViewAllLink";
import {
  buildConnectionUpsertBody,
  fetchNetscanKnown,
  fetchProwlarrKey,
  upsertConnection,
} from "../../api/settings";

// GrabTarget is one pending auto-grab: which mode, a human label for the
// dialog title, and the exact request body the backend needs. For Series the
// season/episode picker has already resolved before a target exists.
export type GrabTarget = { mode: Mode; label: string; request: AutoGrabRequest };

// STATUS_COPY turns an autograb.Grade Status into a short human reason for a
// fallback pick-list row — so the operator sees WHY each release wasn't
// auto-picked, not a bare rejected flag.
const STATUS_COPY: Record<string, string> = {
  qualified: "meets the bar",
  "below-floor": "below the quality floor",
  mislabeled: "looks mislabeled",
  "low-seeders": "too few seeders",
  "unknown-bitrate": "runtime unknown — bitrate not scored",
  "unknown-resolution": "resolution not recognized",
};

// Modal is a lightweight centered overlay for the grab dialog. Clicking the
// backdrop or Close dismisses it; clicks inside don't bubble out. Exported
// (was module-private) so DetailPopup.tsx — the third overlay in this
// codebase — builds on the same primitive instead of a second one.
export const Modal: Component<{
  title: string;
  onClose: () => void;
  children: JSX.Element;
}> = (props) => {
  const titleId = createUniqueId();
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") props.onClose();
    };
    document.addEventListener("keydown", onKey);
    onCleanup(() => document.removeEventListener("keydown", onKey));
  });
  return (
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={props.onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        class="max-h-[85vh] w-full max-w-lg overflow-y-auto rounded-xl border border-border bg-surface p-5 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div class="mb-3 flex items-center justify-between gap-3">
          <h3 id={titleId} class="truncate text-base font-semibold text-fg">
            {props.title}
          </h3>
          <Button onClick={props.onClose}>Close</Button>
        </div>
        {props.children}
      </div>
    </div>
  );
};

// NOT_CONFIGURED_SERVICES maps the two external services Discover itself
// depends on (backend errors are the fixed strings "tmdb isn't configured
// yet — add it in Settings first" / "tpdb isn't configured yet — add it in
// Settings first", see internal/api/discover.go and adultdiscover.go) to
// their fixed base URL (both are external APIs with one canonical endpoint,
// not self-hosted — the operator only ever needs to supply a key, unlike
// Prowlarr/Stash/etc.) and the external page to obtain a key. TMDB's
// is well-known and stable; TPDB's was confirmed directly by Wade
// (2026-07-13) rather than guessed, since it isn't discoverable from a
// plain page fetch (the site is JS-rendered).
const NOT_CONFIGURED_SERVICES: Record<
  "tmdb" | "tpdb",
  { label: string; url: string; keyPageUrl: string; keyPageLabel: string }
> = {
  tmdb: {
    label: "TMDB",
    url: "https://api.themoviedb.org/3",
    keyPageUrl: "https://www.themoviedb.org/settings/api",
    keyPageLabel: "themoviedb.org/settings/api",
  },
  tpdb: {
    label: "TPDB",
    url: "https://api.theporndb.net",
    keyPageUrl: "https://theporndb.net/user/api-tokens",
    keyPageLabel: "theporndb.net/user/api-tokens",
  },
};

// notConfiguredService detects which (if either) of Discover's two external
// dependencies a resource error is reporting missing, by matching the
// backend's fixed error string — returns undefined for any other error (a
// genuine network failure, a 500, etc.), which callers fall back to
// ErrorText for instead of assuming it's a "go configure this" case.
export function notConfiguredService(
  err: unknown,
): "tmdb" | "tpdb" | undefined {
  const msg = (err as Error)?.message ?? "";
  if (!/isn't configured yet/i.test(msg)) return undefined;
  if (/\btmdb\b/i.test(msg)) return "tmdb";
  if (/\btpdb\b/i.test(msg)) return "tpdb";
  return undefined;
}

// ConfigureConnectionModal — shown instead of a bare error message when
// Discover detects TMDB/TPDB isn't configured. Saves directly into the same
// connection store Settings' own form writes to (upsertConnection/
// buildConnectionUpsertBody, reused verbatim, not duplicated) so there's
// exactly one place that actually persists a connection — this is just a
// second, more contextual entry point into it. First-time save, so
// hasExistingKey is always false and keyTouched is always true here (see
// buildConnectionUpsertBody's own doc comment on why that combination is
// safe: a first save always sends the key, even if it were left blank).
export const ConfigureConnectionModal: Component<{
  service: "tmdb" | "tpdb";
  onClose: () => void;
  onSaved: () => void;
}> = (props) => {
  const info = NOT_CONFIGURED_SERVICES[props.service];
  const [key, setKey] = createSignal("");
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal("");

  const save = async () => {
    setError("");
    if (!key().trim()) {
      setError("Enter an API key first.");
      return;
    }
    setSaving(true);
    try {
      await upsertConnection(
        props.service,
        buildConnectionUpsertBody({
          url: info.url,
          needsUsername: false,
          keyTouched: true,
          keyValue: key(),
          hasExistingKey: false,
        }),
      );
      props.onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={`Set up ${info.label}`} onClose={props.onClose}>
      <p class="mb-3 text-sm text-muted">
        {info.label} isn't configured yet — Discover needs it to browse{" "}
        {props.service === "tpdb" ? "Adult scenes" : "titles"}. Paste an API
        key below to enable it now, or add it later in Settings.
      </p>
      <a
        href={info.keyPageUrl}
        target="_blank"
        rel="noreferrer"
        class="mb-3 block text-sm text-accent underline"
      >
        Get an API key at {info.keyPageLabel}
      </a>
      <input
        type="password"
        class="w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
        placeholder="API key"
        value={key()}
        onInput={(e) => setKey(e.currentTarget.value)}
      />
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <div class="mt-3 flex justify-end gap-2">
        <Button onClick={props.onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={saving()}>
          {saving() ? "Saving…" : "Save"}
        </Button>
      </div>
    </Modal>
  );
};

// SelectCheckbox is the select-mode overlay a selectable card shows in the
// corner of its poster — a purely visual checkbox reflecting whether the card
// is currently in the bulk-grab selection. Exported so PosterCard and AdultCard
// (and the Series season chips) render one consistent affordance rather than
// three hand-rolled ones. It is display-only; the card body's onClick owns the
// actual toggle.
export const SelectCheckbox: Component<{ checked: boolean }> = (props) => (
  <div
    data-testid="select-checkbox"
    data-checked={props.checked ? "true" : "false"}
    class="pointer-events-none absolute left-2 top-2 flex h-6 w-6 items-center justify-center rounded-md border-2 text-xs font-bold shadow"
    classList={{
      "border-accent bg-accent text-accent-fg": props.checked,
      "border-white/80 bg-black/50 text-transparent": !props.checked,
    }}
    aria-hidden="true"
  >
    ✓
  </div>
);

// FallbackPickList renders the ranked manual pick list the backend returns when
// nothing auto-qualified. Each row labels why it wasn't auto-picked and offers
// a single "Grab this" — one release per click, never a batch. Exported (was
// module-private) so BulkResultModal reuses it verbatim for a bulk-grab item
// that fell back to a manual pick, instead of a second copy (Pass 2 prereq
// refactor, no behavior change).
export const FallbackPickList: Component<{
  response: AutoGrabResponse;
  onPick: (c: AutoGrabCandidate) => void;
  grabbing: string;
  error: string;
}> = (props) => (
  <div>
    <Muted class="mb-2">{props.response.message}</Muted>
    <Show when={props.error}>
      <ErrorText>{props.error}</ErrorText>
    </Show>
    <Show
      when={(props.response.candidates ?? []).length > 0}
      fallback={<Muted>No releases found for this title.</Muted>}
    >
      <ul class="flex flex-col gap-2">
        <For each={props.response.candidates}>
          {(c) => (
            <li class="flex items-center gap-3 rounded-md border border-border bg-surface-2 p-2">
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm text-fg" title={c.title}>
                  {c.title}
                </div>
                <div class="truncate text-xs text-muted">
                  {[c.indexer, c.protocol, STATUS_COPY[c.status] ?? c.status]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
              </div>
              <Button
                onClick={() => props.onPick(c)}
                disabled={!!props.grabbing}
              >
                {props.grabbing === c.downloadUrl ? "Grabbing…" : "Grab this"}
              </Button>
            </li>
          )}
        </For>
      </ul>
    </Show>
  </div>
);

// formatSize renders a byte count as a compact GB/MB label for a release row;
// 0/absent (e.g. a pool direct-grab enclosure that carries no size) yields ""
// so the row's meta line drops it via .filter(Boolean).
function formatSize(bytes: number): string {
  if (!bytes || bytes <= 0) return "";
  const gb = bytes / 1e9;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${Math.max(1, Math.round(bytes / 1e6))} MB`;
}

// SearchReleasePicker is the catalog-Search release picker: the flat, selectable
// list of scored releases behind one clicked searched card. It accepts EITHER a
// fetch key (Movies/Series: mode+title — fetches the one bounded Prowlarr search
// on open) OR pre-supplied releases (Adult: the scene's already-fetched variants,
// no network call). Selecting a row grabs that exact release via the existing
// /search/grab endpoint (rootFolderPath from libraryRootFolder(mode), exactly as
// GrabDialog's manual pick does), then closes.
//
// The Movies/Series fetch is a SINGLE createResource keyed on a stable
// {mode,title} source — it fires exactly once on open and never re-fires on
// scroll/hover/re-render (the same one-shot pattern DetailPopup's
// fetchAvailabilityPreview uses). When props.releases is supplied the source
// returns null, so the fetcher never runs (Adult makes zero network calls here).
export const SearchReleasePicker: Component<{
  mode: Mode;
  title: string;
  // tmdbId threads a Movies/Series catalog id into the grab; Adult has none.
  tmdbId?: number;
  // releases, when supplied (Adult), are used directly — no fetch.
  releases?: SearchReleaseResult[];
  onClose: () => void;
}> = (props) => {
  const [fetched] = createResource(
    () =>
      props.releases
        ? null
        : { mode: props.mode as Exclude<Mode, "adult">, title: props.title },
    ({ mode, title }) => fetchReleaseOptions(mode, title),
  );
  const releases = () => props.releases ?? fetched() ?? [];
  const loading = () => !props.releases && fetched.loading;

  const [grabbing, setGrabbing] = createSignal("");
  const [error, setError] = createSignal("");

  const pick = async (r: SearchReleaseResult) => {
    setError("");
    setGrabbing(r.downloadUrl);
    try {
      const root = await libraryRootFolder(props.mode);
      if (!root) {
        throw new Error(
          "no root folder configured for this mode — set one in Settings first",
        );
      }
      await manualGrab(props.mode, {
        title: props.title,
        tmdbId: props.mode !== "adult" ? props.tmdbId : undefined,
        indexer: r.indexer,
        protocol: r.protocol,
        downloadUrl: r.downloadUrl,
        rootFolderPath: root,
      });
      props.onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setGrabbing("");
    }
  };

  return (
    <Modal title={`Releases — ${props.title}`} onClose={props.onClose}>
      <Show
        when={!loading()}
        fallback={<Muted>Searching for releases…</Muted>}
      >
        <Show when={error()}>
          <ErrorText>{error()}</ErrorText>
        </Show>
        <Show
          when={releases().length > 0}
          fallback={<Muted>No releases found for this title.</Muted>}
        >
          <ul class="flex flex-col gap-2">
            <For each={releases()}>
              {(r) => (
                <li class="flex items-center gap-3 rounded-md border border-border bg-surface-2 p-2">
                  <div class="min-w-0 flex-1">
                    <div class="truncate text-sm text-fg" title={r.title}>
                      {r.title}
                    </div>
                    <div class="truncate text-xs text-muted">
                      {[
                        r.indexer,
                        r.protocol,
                        formatSize(r.size),
                        r.seeders ? `${r.seeders} seeders` : "",
                        r.score ? `score ${r.score}` : "",
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </div>
                  </div>
                  <Button onClick={() => void pick(r)} disabled={!!grabbing()}>
                    {grabbing() === r.downloadUrl ? "Grabbing…" : "Grab"}
                  </Button>
                </li>
              )}
            </For>
          </ul>
        </Show>
      </Show>
    </Modal>
  );
};

// MISSING_GRAB_SERVICE maps each backend "X isn't configured yet" error
// auto-grab can hit to its setup form's shape. Prowlarr is the only one left:
// it fails first (autoGrabHandler's own check, internal/api/autograb.go),
// needing releases to search for, and is a self-hosted single-key service
// (URL + optional API key, like NOT_CONFIGURED_SERVICES above, just not a
// fixed URL).
//
// Claude 2026-08-10: the qbittorrent/nzbget entries were deleted with the
// internal/qbittorrent and internal/nzbget packages — SAK owns downloads
// natively, so there is no external download client left to prompt for. The
// map is deliberately kept as a Record rather than collapsed into a bare
// constant: GrabError is already generic over it, and a second entry would
// return if another optional grab-time dependency is ever added.
const MISSING_GRAB_SERVICE: Record<
  "prowlarr",
  { label: string; needsUsername: boolean; wikiUrl?: string }
> = {
  prowlarr: {
    label: "Prowlarr",
    needsUsername: false,
    wikiUrl: "https://wiki.servarr.com/en/prowlarr",
  },
};

// missingGrabService detects whether auto-grab's one optional dependency is
// what a failure is reporting missing, by matching the backend's
// fixed error string — returns undefined for any other error (a real
// network/indexer failure, a 500, etc.), which GrabError falls back to a
// bare ErrorText for instead of assuming it's a "go configure this" case.
function missingGrabService(
  err: unknown,
): keyof typeof MISSING_GRAB_SERVICE | undefined {
  const msg = (err as Error)?.message ?? "";
  if (!/isn't configured yet/i.test(msg)) return undefined;
  if (/\bprowlarr\b/i.test(msg)) return "prowlarr";
  return undefined;
}

// GrabError renders a GrabDialog's failure state. A missing-service failure
// (see MISSING_GRAB_SERVICE) gets a same-dialog setup prompt instead of a
// bare message, reusing the same upsertConnection/buildConnectionUpsertBody
// Settings' own form calls. onConfigured re-runs the auto-grab immediately
// after saving so the operator doesn't have to close this dialog and click
// Grab again.
const GrabError: Component<{ error: Error; onConfigured: () => void }> = (
  props,
) => {
  const service = () => missingGrabService(props.error);
  const info = () => {
    const s = service();
    return s ? MISSING_GRAB_SERVICE[s] : undefined;
  };

  const [url, setUrl] = createSignal("");
  const [username, setUsername] = createSignal("");
  const [key, setKey] = createSignal("");
  const [saving, setSaving] = createSignal(false);
  const [saveError, setSaveError] = createSignal("");
  const [hint, setHint] = createSignal("");

  // LAN auto-discovery — same "suggest, never silently apply" convention
  // Settings' ConnectionRow already uses for netscan findings (see its
  // useURL/fetchKey): a match is shown as a clickable hint the operator must
  // confirm, not filled into the URL field automatically.
  const [findings] = createResource(fetchNetscanKnown);
  const finding = () => findings()?.find((f) => f.service === service());

  const useFoundURL = () => {
    const found = finding();
    if (!found) return;
    setUrl(found.url);
    setHint("URL pre-filled — verify it's really yours, then Save.");
  };
  const useFoundKey = async () => {
    const found = finding();
    if (!found || service() !== "prowlarr") return;
    setHint("fetching key…");
    try {
      const k = await fetchProwlarrKey(found.url);
      setKey(k);
      setHint(`API key retrieved from ${found.url} — verify before saving.`);
    } catch (e) {
      setSaveError((e as Error).message);
    }
  };

  const save = async () => {
    const i = info();
    setSaveError("");
    if (!url().trim()) {
      setSaveError(`Enter ${i?.label ?? "its"} URL first.`);
      return;
    }
    setSaving(true);
    try {
      await upsertConnection(
        service()!,
        buildConnectionUpsertBody({
          url: url(),
          username: username(),
          needsUsername: i?.needsUsername ?? false,
          keyTouched: true,
          keyValue: key(),
          hasExistingKey: false,
        }),
      );
      props.onConfigured();
    } catch (e) {
      setSaveError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Show
      when={info()}
      fallback={<ErrorText>{props.error?.message}</ErrorText>}
    >
      {(i) => (
        <>
          <p class="mb-1 text-sm text-muted">
            {i().label} isn't configured yet — auto-grab needs it to{" "}
            {service() === "prowlarr"
              ? "search for releases"
              : "send the picked release to be downloaded"}
            . Enter its details below to enable it now, or add it later in
            Settings.
          </p>
          <Show when={i().wikiUrl}>
            {(wikiUrl) => (
              <a
                href={wikiUrl()}
                target="_blank"
                rel="noreferrer"
                class="mb-3 block text-sm text-accent underline"
              >
                {wikiUrl().replace(/^https?:\/\//, "")}
              </a>
            )}
          </Show>
          <input
            type="text"
            class="mb-2 w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
            placeholder={`https://${service()}.example.com`}
            value={url()}
            onInput={(e) => setUrl(e.currentTarget.value)}
          />
          <Show when={i().needsUsername}>
            <input
              type="text"
              class="mb-2 w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
              placeholder="username"
              value={username()}
              onInput={(e) => setUsername(e.currentTarget.value)}
            />
          </Show>
          <input
            type="password"
            class="w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
            placeholder={i().needsUsername ? "password" : "API key (if needed)"}
            value={key()}
            onInput={(e) => setKey(e.currentTarget.value)}
          />
          <Show when={finding()}>
            {(found) => (
              <div class="mt-2 rounded border border-dashed border-border p-2 text-xs text-muted">
                <div>
                  Possible {i().label} at {found().url} — a hint only, verify
                  it's yours.
                </div>
                <div class="mt-1 flex gap-2">
                  <Button onClick={useFoundURL}>Use this URL</Button>
                  <Show when={service() === "prowlarr"}>
                    <Button onClick={() => void useFoundKey()}>
                      Fetch API key
                    </Button>
                  </Show>
                </div>
              </div>
            )}
          </Show>
          <Show when={hint()}>
            <p class="mt-2 text-xs text-muted">{hint()}</p>
          </Show>
          <Show when={saveError()}>
            <ErrorText>{saveError()}</ErrorText>
          </Show>
          <div class="mt-3 flex justify-end">
            <Button variant="primary" onClick={save} disabled={saving()}>
              {saving() ? "Saving…" : "Save & retry"}
            </Button>
          </div>
        </>
      )}
    </Show>
  );
};

// GrabDialog fires the auto-grab for a target on mount, then shows the outcome:
// a success line when the backend grabbed the top qualifier, or the manual pick
// list when it fell back. The manual pick reuses the existing /search/grab
// endpoint (auto-grab resolves the root folder server-side; the fallback path
// must fetch it explicitly).
//
// IMPORTANT: the error and success branches below are mutually exclusive
// nested Shows, not siblings — reading a Solid resource's accessor (result())
// after its fetcher has thrown RE-THROWS that error (by design, for
// ErrorBoundary integration). A prior version had `<Show when={result.error}>`
// and `<Show when={result()}>` as SIBLINGS, so the second Show's `when` still
// evaluated result() on every render even while erroring — throwing mid-render
// and leaving the dialog stuck on the loading fallback forever (the update that
// would have shown the error never completed). Nesting the success Show inside
// `when={!result.error}` is what actually prevents that read.
export const GrabDialog: Component<{ target: GrabTarget; onClose: () => void }> = (
  props,
) => {
  const [result, { refetch }] = createResource(
    () => props.target,
    (t) => autoGrab(t.mode, t.request),
  );
  const [grabbing, setGrabbing] = createSignal("");
  const [manualError, setManualError] = createSignal("");
  const [manualGrabbed, setManualGrabbed] = createSignal<string | null>(null);

  const pickManual = async (c: AutoGrabCandidate) => {
    setManualError("");
    setGrabbing(c.downloadUrl);
    try {
      const root = await libraryRootFolder(props.target.mode);
      if (!root) {
        throw new Error(
          "no root folder configured for this mode — set one in Settings first",
        );
      }
      await manualGrab(props.target.mode, {
        title: props.target.request.title,
        tmdbId: props.target.request.tmdbId,
        seasonNumber: props.target.request.seasonNumber,
        episodeNumber: props.target.request.episodeNumber,
        seasonSpecified: props.target.request.seasonSpecified,
        indexer: c.indexer,
        protocol: c.protocol,
        downloadUrl: c.downloadUrl,
        rootFolderPath: root,
      });
      setManualGrabbed(c.title);
    } catch (e) {
      setManualError((e as Error).message);
    } finally {
      setGrabbing("");
    }
  };

  return (
    <Modal title={`Grab — ${props.target.label}`} onClose={props.onClose}>
      <Show
        when={!result.loading}
        fallback={<Muted>Searching and scoring releases…</Muted>}
      >
        <Show
          when={!result.error}
          fallback={
            <GrabError error={result.error as Error} onConfigured={refetch} />
          }
        >
          <Show when={result()}>
            {(r) => (
              // The fallback branch handles the not-grabbed/not-a-pick-list
              // outcome the backend's duplicate-grab guard returns
              // ({grabbed:false, fallback:unset, message:"already grabbing
              // this release"}) — without it the Switch would match nothing and
              // render a blank modal, the "silent no-op" the guard exists to
              // avoid. Rendered as an informational Muted note (an expected
              // outcome, not a failure), the same way FallbackPickList surfaces
              // its own response.message.
              <Switch fallback={<Show when={r().message}><Muted>{r().message}</Muted></Show>}>
                <Match when={r().grabbed}>
                  <div class="text-sm text-ok">{r().message}</div>
                  <Muted class="mt-1">
                    Tracked in Calendar's History view — check import there
                    once it finishes downloading.
                  </Muted>
                </Match>
                <Match when={r().fallback}>
                  <Show
                    when={manualGrabbed()}
                    fallback={
                      <FallbackPickList
                        response={r()}
                        onPick={pickManual}
                        grabbing={grabbing()}
                        error={manualError()}
                      />
                    }
                  >
                    <div class="text-sm text-ok">
                      Grabbed “{manualGrabbed()}”. Tracked in Calendar's
                      History view.
                    </div>
                  </Show>
                </Match>
              </Switch>
            )}
          </Show>
        </Show>
      </Show>
    </Modal>
  );
};

// TextPoster is the fallback tile when no art exists (TMDB/TPDB returned a
// blank poster/image) — a titled placeholder that keeps the card's footprint
// identical to an image card so rows don't reflow.
export const TextPoster: Component<{ label: string }> = (props) => (
  <div class="flex h-full w-full items-center justify-center bg-surface-2 p-2 text-center text-xs text-muted">
    {props.label}
  </div>
);

// PaginatedStrip is the generic "Show more" strip every Discover row is built
// from: a title, a horizontal (or, via containerClass, wrapping) list of cards,
// and a "Show more" that APPENDS the next page rather than replacing the strip —
// the accumulator (items) only ever grows. It reloads from page 1 whenever
// reloadToken changes (the setup-modal "I just configured it, refetch" signal).
// Fetch errors are reported up via onError so the parent can raise the
// not-configured setup modal once for the whole page, not per strip. The item
// type T and both the page loader (load) and the per-item renderer (children)
// are supplied by the caller, so one pagination engine backs the Mainstream
// TMDB rows, the Adult scene rows, the Studios/Performers browse rows, and the
// drill-down scene grid alike (plan: reuse the pattern, don't reimplement it).
// defaultStripPageSize matches every current PaginatedStrip caller's actual
// backend page size (adultnewest.defaultResolvePerPage /
// tpdbrest.defaultBrowsePerPage / stashbox.defaultBrowsePerPage — all 20).
// Used only as the exhaustion heuristic's default (see perPage prop below);
// not itself sent to the backend as a request param.
const defaultStripPageSize = 20;

export function PaginatedStrip<T>(props: {
  title: string;
  // reloadToken is any value that changing signals a reload-from-page-1 (the
  // "I just configured it, refetch" numeric signal, or a filter/sort-state
  // string). It's only ever fed to on() as a change trigger — never used
  // numerically — so number and string work identically here.
  reloadToken: () => number | string;
  // load's return type is widened (backward compatible — every existing
  // caller returns a plain T[], unaffected) to ALSO allow an explicit
  // {items, hasMore} envelope for a row whose batch length can no longer be
  // trusted as an exhaustion signal (see the load() function below for why).
  // The Studios and Performers rows (fetchMergedStudios/fetchMergedPerformers)
  // use this today.
  load: (page: number) => Promise<T[] | { items: T[]; hasMore: boolean }>;
  onError: (err: unknown) => void;
  containerClass?: string;
  children: (item: T) => JSX.Element;
  // singlePage suppresses "Show more" even when more data may exist — for
  // rows whose ordering is only meaningful within one fetched page (e.g.
  // Adult's "Highest Rated," a same-page rating re-sort with no true
  // server-side popularity sort behind it: paginating would append an
  // independently-resorted page 2 after page 1, producing a visibly
  // non-monotonic rating order under a "Highest Rated" label).
  singlePage?: boolean;
  // perPage is this row's backend page size, used ONLY to detect exhaustion
  // a page early (see the load() doc comment below) — defaults to
  // defaultStripPageSize, correct for every current caller. Override if a
  // future caller's backend page size ever differs.
  perPage?: number;
  // infiniteScroll opts THIS strip into automatic IntersectionObserver-based
  // load-on-scroll in place of the manual "Show more" button. Defaults to
  // undefined/false — every existing caller is structurally unaffected: no
  // observer is created and "Show more" renders exactly as today. Used ONLY by
  // Adult Discover's merged Studios and gender-split Performers rows.
  infiniteScroll?: boolean;
  // Claude 2026-08-13: opt-in View All header link (Slice 4). Omitted = today's
  // heading. In-row "Show more" is unchanged.
  // Reason: 4B — only PaginatedStrip + Mainstream PaginatedRow. Callers that
  // must not grow View All (Trakt, library, sliders, calendar, search/filter)
  // simply omit this.
  // Review if: entity rows (studio/performer) ever lose their drill-down.
  viewAll?: { href: string };
}): JSX.Element {
  const [items, setItems] = createSignal<T[]>([]);
  const [page, setPage] = createSignal(0);
  const [loading, setLoading] = createSignal(false);
  const [exhausted, setExhausted] = createSignal(false);
  // autoAdvanceCount counts how many extra envelope pages THIS load-more
  // operation (one manual "Show more" click, or the reset-effect's initial
  // load) has auto-fetched on its own — see DE-2 below. Reset to 0 whenever an
  // operation's auto-advance chain ends (enough gathered, row exhausted, or the
  // cap hit) and whenever the row reloads from page 1, so each fresh operation
  // gets the full budget again.
  const [autoAdvanceCount, setAutoAdvanceCount] = createSignal(0);
  const maxAutoAdvance = 3;

  // intersecting mirrors the trailing-edge sentinel's current in-view state
  // under infiniteScroll. The IntersectionObserver callback ONLY writes this
  // signal (it does not call load directly); a separate createEffect below
  // reacts to the combination of this signal + loading() + canAdvance() and
  // drives the actual loads. See that effect's comment for WHY this indirection
  // is required (IntersectionObserver fires only on in/out-of-view TRANSITIONS,
  // not continuously while an element stays visible). Defaults false and is
  // reset to false on sentinel cleanup so a fresh sentinel starts clean.
  const [intersecting, setIntersecting] = createSignal(false);

  // autoLoading guards the infiniteScroll effect against starting a second,
  // overlapping load operation while one is still running. loading() alone can't
  // do this: the DE-2 auto-advance recursion wraps each hop in its own
  // try/finally, so the innermost hop flips loading() false before the outer
  // hops finish unwinding — a spurious mid-operation idle window the effect would
  // otherwise treat as "done, fire the next one," double-loading pages. This flag
  // is set true when the effect starts an operation and cleared only when that
  // operation's whole promise (DE-2 chain included) settles, so the effect
  // re-checks exactly once per completed operation. It is a signal (not a plain
  // bool) so clearing it in the promise's .finally reactively re-runs the effect
  // to drive the NEXT sparse-continuation load.
  const [autoLoading, setAutoLoading] = createSignal(false);

  // load fetches one page and appends it. chainStart records items().length as
  // it was at the START of the current load-more operation (a top-level manual
  // click or the reset-effect's initial call); it defaults to the CURRENT
  // items().length, which is correct for both of those top-level entry points
  // (0 right after the effect clears items on reset; the accumulated length on
  // a manual click). The recursive auto-advance call (see DE-2 below) passes
  // chainStart through UNCHANGED so the whole chain measures "items gained by
  // this one operation" against a single fixed reference point.
  const load = async (reset: boolean, chainStart = items().length) => {
    const next = reset ? 1 : page() + 1;
    setLoading(true);
    try {
      const result = await props.load(next);
      // An envelope ({items, hasMore}) carries an EXPLICIT exhaustion signal
      // computed server-side — used only by rows (currently Studios and
      // Performers, via fetchMergedStudios/fetchMergedPerformers) whose batch
      // length can no longer be trusted for that purpose (see below). Every
      // other caller returns a plain array and falls through to the
      // length-inference branch, unchanged.
      const isEnvelope = !Array.isArray(result);
      const batch = isEnvelope ? result.items : result;
      setItems((prev) => (reset ? batch : [...prev, ...batch]));
      setPage(next);
      let hasMore = true;
      if (isEnvelope) {
        hasMore = result.hasMore;
        if (!hasMore) setExhausted(true);
      } else if (batch.length < (props.perPage ?? defaultStripPageSize)) {
        // A batch smaller than a full page means this WAS the last page —
        // checking only `=== 0` (the old behavior) missed this: a row with
        // fewer than perPage total items returned everything on page 1
        // (batch.length > 0), so "Show more" kept rendering even though
        // nothing remained. Clicking it fetched an empty page 2, appended
        // nothing, and only then hid the button — a silent round trip
        // indistinguishable from the button doing nothing at all (found
        // live, 2026-07-15).
        //
        // The merged Studios and Performers rows (fetchMergedStudios /
        // fetchMergedPerformers) are NOT covered by this length-inference
        // path — both pass through a server-side post-merge filter
        // (zero-scene for Studios, grabbable-availability for both) that can
        // drop items from an already-fetched page, so a short batch no
        // longer reliably means "the catalog is exhausted." That's why both
        // return the explicit-hasMore envelope handled in the branch above
        // instead of relying on this length check.
        setExhausted(true);
      }
      // DE-2: an envelope row's post-merge availability filter can hand back a
      // page that's EMPTY or merely SPARSE (fewer real items than a full page)
      // while the underlying catalog still has more (hasMore=true) — every
      // item on the page, or most of them, was dropped by the filter. Either
      // way, one manual "Show more" click yielded far less than the perPage
      // worth of new cards every other Discover row delivers per click. The
      // originally-empty-only version of this branch (batch.length === 0)
      // missed the sparse case entirely: the live Performers row returns pages
      // of 1 item each against the small curated identify pool, so it never
      // tripped an all-empty check and the operator got exactly one new item
      // per click.
      //
      // Broadened trigger: keep auto-fetching subsequent pages until the items
      // GAINED BY THIS OPERATION reach the row's own perPage target — i.e.
      // gained = items().length (read reactively right after the setItems above;
      // Solid signal writes are synchronous outside batch(), so this sees the
      // just-appended length) minus chainStart, the length when this operation
      // began. chainStart is threaded through the recursion unchanged, so the
      // whole chain accumulates toward one shared full-page goal rather than
      // resetting its measure each hop.
      //
      // Stop (and let the existing manual "Show more" (DE) control take over)
      // on whichever comes first: gained >= target (a full page's worth
      // collected), hasMore === false (row genuinely exhausted, handled above),
      // or maxAutoAdvance extra fetches (the bounded cap, so a pathologically
      // sparse catalog can't turn one click into an unbounded loop). The cap is
      // per-operation: setAutoAdvanceCount(0) fires the moment a chain ends, so
      // the NEXT click re-engages the full budget and again pulls ~a page's
      // worth — not one item like the pre-fix behavior.
      //
      // Plain-array callers are structurally unaffected: isEnvelope is false
      // for them, so this whole branch is skipped and setAutoAdvanceCount(0)
      // runs — identical to before this change and to their all-along behavior.
      const target = props.perPage ?? defaultStripPageSize;
      if (
        isEnvelope &&
        hasMore &&
        items().length - chainStart < target &&
        autoAdvanceCount() < maxAutoAdvance
      ) {
        setAutoAdvanceCount((n) => n + 1);
        await load(false, chainStart);
        return;
      } else {
        setAutoAdvanceCount(0);
      }
    } catch (e) {
      props.onError(e);
    } finally {
      setLoading(false);
    }
  };

  // Initial load AND reload-on-token in one effect (on() runs immediately by
  // default, so no separate onMount is needed).
  createEffect(
    on(props.reloadToken, () => {
      setItems([]);
      setPage(0);
      setExhausted(false);
      setAutoAdvanceCount(0);
      void load(true);
    }),
  );

  // canAdvance is the DE guard: whether a "Show more" control should be
  // reachable. Deliberately does NOT depend on items().length — the old
  // guard nested this control inside <Show when={items().length > 0}>,
  // so a page emptied entirely by a post-merge filter (hasMore=true, but
  // every item on it dropped) hid the button along with the empty item
  // list, leaving no way to advance past it (a dead end). page() > 0
  // excludes the pre-first-load state (no phantom control before anything
  // has been fetched); !exhausted() && !props.singlePage are unchanged from
  // before.
  const canAdvance = () => !exhausted() && !props.singlePage && page() > 0;

  // Auto-load driver for infiniteScroll: fires load(false) whenever the sentinel
  // is intersecting, no fetch is in flight, and the row can still advance. This
  // effect re-runs whenever ANY of its tracked dependencies change — crucially,
  // when a load completes and loading() flips back to false, it re-runs even
  // though intersecting() itself did not change. If the sentinel is STILL
  // intersecting at that point (the signal is still true from its last real
  // callback, because a sparse append never scrolled it out of view), the
  // condition is true again and the next page loads. That reactive re-check is
  // what delivers "keep loading until the viewport fills or the catalog runs
  // out" — it replaces the old, incorrect assumption that the IntersectionObserver
  // would keep re-firing on its own while the sentinel stayed visible (it does
  // not; it fires only on in/out-of-view transitions). The chain terminates when
  // canAdvance() goes false (row exhausted / singlePage / reset to page 0) or a
  // genuine observer callback sets intersecting() to false (sentinel actually
  // scrolled out of view once enough pages filled the strip). Strips without
  // infiniteScroll never construct an observer, so intersecting() stays false and
  // this effect is permanently inert for them. autoLoading() gates re-entry
  // across a whole operation (see its declaration) — without it the DE-2 chain's
  // mid-operation loading() gaps would trigger overlapping duplicate loads.
  createEffect(() => {
    if (intersecting() && !loading() && !autoLoading() && canAdvance()) {
      setAutoLoading(true);
      void load(false).finally(() => setAutoLoading(false));
    }
  });

  // scrollContainer is the horizontal overflow-x-auto strip, captured so it can
  // serve as the IntersectionObserver root when infiniteScroll is on. It is
  // assigned during the initial synchronous render; the sentinel that reads it
  // can only mount after the first load(true) sets page() > 0 (canAdvance()),
  // which happens in a later effect — so it is always defined by then.
  let scrollContainer: HTMLDivElement | undefined;

  // attachSentinel is a REF CALLBACK (deliberately NOT onMount) on the
  // trailing-edge sentinel. The sentinel mounts/unmounts as canAdvance() flips
  // — e.g. a reloadToken reset drops page() to 0, unmounting it, then load(true)
  // sets page() to 1, mounting a FRESH element. A one-shot onMount would observe
  // only the first sentinel and never re-observe the replacement. A ref callback
  // re-runs for every fresh element instead. onCleanup, registered in the
  // <Show> branch's reactive owner (Solid runs the ref via untrack, which keeps
  // the Owner intact), disconnects the observer when that sentinel unmounts, so
  // no observer ever outlives its element. Because this is the ONLY place an
  // IntersectionObserver is constructed, a strip without infiniteScroll never
  // creates one — the absent-prop path is completely inert.
  const attachSentinel = (el: HTMLDivElement) => {
    const observer = new IntersectionObserver(
      (entries) => {
        // The callback only records the sentinel's latest in-view state into the
        // intersecting() signal; the load-driving effect above reacts to it. It
        // does NOT call load() here — doing so was a real dead-end bug (fixed
        // 2026-07-28): IntersectionObserver invokes this callback only when the
        // sentinel crosses the threshold (a TRANSITION into or out of view), not
        // continuously while it stays visible. On the sparse merged Adult rows
        // (as little as 1 item per fetched page), appending a page often doesn't
        // move the sentinel out of view, so no second callback ever fired and the
        // chain silently stopped even though canAdvance() was still true and
        // there's no "Show more" fallback under infiniteScroll. Recording state +
        // reacting in an effect makes the "keep loading until the viewport fills"
        // behavior come from reactive re-evaluation after each load, not from a
        // (false) assumption that the observer keeps re-firing on its own.
        //
        // Only one sentinel is ever observed by a given observer, so entries has
        // one element in practice; take the last defensively.
        const entry = entries[entries.length - 1];
        if (entry) setIntersecting(entry.isIntersecting);
      },
      {
        root: scrollContainer,
        // Prefetch 400px before the trailing edge, matching
        // Carousel.LOAD_MORE_THRESHOLD_PX.
        rootMargin: "0px 400px 0px 0px",
        threshold: 0,
      },
    );
    observer.observe(el);
    onCleanup(() => {
      observer.disconnect();
      // Reset so a fresh sentinel (e.g. after a reloadToken reset re-mounts a new
      // element and constructs a new observer) starts from a clean not-in-view
      // state until its own first real callback fires — a stale true from the
      // prior sentinel must not leak in and auto-fire a load before the new
      // sentinel has actually been observed intersecting.
      setIntersecting(false);
    });
  };

  return (
    <section class="mt-6">
      <div class="mb-2 flex items-center justify-between gap-3">
        <h2 class="text-sm font-semibold uppercase tracking-wide text-muted">
          {props.title}
        </h2>
        <Show when={props.viewAll}>
          {(va) => <ViewAllLink href={va().href} title={props.title} />}
        </Show>
      </div>
      <Show
        when={items().length > 0 || canAdvance()}
        fallback={
          <Muted>{loading() ? "Loading…" : "Nothing here yet."}</Muted>
        }
      >
        <div ref={scrollContainer} class={props.containerClass ?? "flex items-stretch gap-3 overflow-x-auto pb-2"}>
          <For each={items()}>{(item) => props.children(item)}</For>
          {/* Manual "Show more" — rendered EXCEPT when infiniteScroll is on.
              With the prop absent this reduces to <Show when={canAdvance()}>,
              reactively and DOM-identical to before. */}
          <Show when={!props.infiniteScroll && canAdvance()}>
            <div class="flex w-28 shrink-0 items-center justify-center">
              <Button
                class="!py-1 text-xs"
                onClick={() => void load(false)}
                disabled={loading()}
              >
                {loading() ? "Loading…" : "Show more"}
              </Button>
            </div>
          </Show>
          {/* Auto-load sentinel — only when infiniteScroll is on. The observer
              is attached via attachSentinel's ref callback; while a fetch is in
              flight it shows a small "Loading…" affordance at the trailing edge
              (matching Carousel's), otherwise it is an empty sized target. */}
          <Show when={props.infiniteScroll && canAdvance()}>
            <div
              ref={attachSentinel}
              class="flex w-28 shrink-0 items-center justify-center"
            >
              <Show when={loading()}>
                <Muted>Loading…</Muted>
              </Show>
            </div>
          </Show>
        </div>
      </Show>
    </section>
  );
}
