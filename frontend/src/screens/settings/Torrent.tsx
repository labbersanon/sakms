// Torrent — a sub-tab of the Settings > Download tab (see Download.tsx), the
// torrent engine's counterpart to the Usenet sub-tab beside it (a symmetric
// Usenet / Torrent pair in Download's inner tab bar).
//
// It has its own page for the same reason Usenet.tsx does: the field set is far
// richer than any generic connection row renders — engine (staging directory,
// listen port, DHT/PEX, protocol obfuscation), performance (max connections,
// download rate limit), seeding (master switch, ratio and duration limits), and
// stale-torrent handling — four logical groups over one settings document.
//
// Three things this file must not get wrong:
//
//   1. THE PUT IS A FULL-DOCUMENT REPLACE. `config` below deliberately holds one
//      signal carrying the ENTIRE DownloaderConfig, not a signal per key. Every
//      control mutates a field of that object and the save PUTs the whole thing
//      back. Do not refactor this into per-key signals and reassemble a body
//      from the keys that happen to have controls — omitted fields arrive as
//      zero values and overwrite stored settings, silently for the booleans and
//      the rate/ratio/duration/stale numbers (see api/torrent.ts).
//   2. `maxConcurrent` RENDERS NO CONTROL BUT MUST STILL ROUND-TRIP. It is
//      documented server-side as reserved/unimplemented (all torrents download
//      concurrently today), so exposing a knob for it would be a knob that does
//      nothing. But the PUT handler rejects `maxConcurrent < 1` with a 400, so
//      dropping it fails EVERY save — including a save that touched nothing but
//      the seed ratio. Because the field is invisible on screen, that 400 looks
//      like it came from an unrelated control. Rule 1 is what keeps this correct
//      for free.
//   3. THERE IS NO LOCAL-PEER-DISCOVERY TOGGLE, deliberately. The torrent
//      library this ships against has no LPD implementation at all — there is no
//      field to wire one to, and a toggle wired to nothing is a stored-and-
//      ignored setting. Do not add one back.
//
// One more thing worth knowing before writing copy or error handling here:
// `stagingDir` comes back "" when unset. That is a normal not-configured state,
// not a load failure — the server has no data directory to synthesize its boot
// default from, so it reports empty rather than guessing.

import {
  type Component,
  type JSX,
  Show,
  createSignal,
  onMount,
} from "solid-js";
import type { DownloaderConfig } from "@dto";
import {
  fetchDownloaderConfig,
  isRebuildRefused,
  putDownloaderConfig,
} from "../../api/torrent";
import { fetchUsenetAutoGrabEnabled } from "../../api/usenet";
import { ErrorText, Muted, inputClass, labelClass } from "../../components/ui";
import {
  Card,
  SaveStatus,
  SectionSave,
  type SectionSaveOutcome,
  useSaveStatus,
  useSectionSaveItem,
} from "./shared";

// Bounds mirrored from the PUT handler's own validation so the one Save button
// disables itself while a field is out of range, instead of letting the
// operator click Save and read a 400 afterwards. The server still re-validates.
const LISTEN_PORT_MIN = 1024;
const LISTEN_PORT_MAX = 65535;

// Rate-limit units. The wire value is bytes/sec; these are display only.
const RATE_UNITS = { "KB/s": 1024, "MB/s": 1024 * 1024 };

// Seed-duration units. The wire value is minutes; these are display only.
const DURATION_UNITS = { hours: 60, days: 1440 };

export const TorrentSection: Component = () => (
  <div>
    <SectionSave>
      <TorrentSettingsCard />
    </SectionSave>
  </div>
);

// Group is one of §6.2's four labelled control groups inside the single card.
const Group: Component<{ title: string; children: JSX.Element }> = (props) => (
  <div class="mb-5">
    <h4 class="mb-2 text-sm font-semibold text-fg">{props.title}</h4>
    {props.children}
  </div>
);

// UnitAmount is the shared "number + unit selector" control (download rate
// limit, seed duration). It owns the display split — how many of which unit —
// and reports back ONLY the value in base units, so the config document never
// learns that a unit selector exists.
//
// The unit and amount are derived from the stored value EXACTLY ONCE, at mount,
// and thereafter only ever change from an operator event. A createEffect that
// re-derived them on every config change would be a silent-corruption bug: 90
// stored minutes displayed as "2 hours" would rewrite itself to 120 the next
// time any unrelated field was saved. Deriving once, plus preferring the
// largest unit that divides the value exactly, keeps the round trip lossless.
const UnitAmount = <U extends string>(props: {
  label: string;
  help?: string;
  // units maps each unit label to how many base units one of it is worth.
  units: Record<U, number>;
  // initial is the stored value in base units, read once at mount.
  initial: number;
  onChange: (baseValue: number) => void;
}) => {
  const unitNames = () => Object.keys(props.units) as U[];
  // Largest unit that divides the value exactly; falls back to the smallest so
  // a value like 1500 B/s still displays (as a fractional KB/s) rather than
  // being rounded into a different number.
  const fit = (): { unit: U; amount: number } => {
    const names = unitNames();
    const ordered = [...names].sort((a, b) => props.units[b] - props.units[a]);
    for (const u of ordered) {
      if (props.initial !== 0 && props.initial % props.units[u] === 0)
        return { unit: u, amount: props.initial / props.units[u] };
    }
    const smallest = ordered[ordered.length - 1]!;
    return { unit: smallest, amount: props.initial / props.units[smallest] };
  };
  const seed = fit();
  const [unit, setUnit] = createSignal<U>(seed.unit);
  const [amount, setAmount] = createSignal(seed.amount);

  // emit rounds to a whole base unit — bytes/sec and minutes are both integers
  // on the wire, and the backend stores them with strconv.Itoa.
  const emit = () => props.onChange(Math.round(amount() * props.units[unit()]));

  return (
    <label class="mb-3 block">
      <span class={labelClass}>{props.label}</span>
      <div class="mt-1 flex items-center gap-2">
        <input
          type="number"
          min={0}
          step="any"
          class={`${inputClass} !w-32`}
          aria-label={props.label}
          value={amount()}
          onInput={(e) => {
            const n = Number(e.currentTarget.value);
            if (Number.isNaN(n)) return;
            setAmount(n);
            emit();
          }}
        />
        {/* A single-unit field (the stale threshold) shows its unit as a
            suffix rather than a dropdown with one choice in it. */}
        <Show
          when={unitNames().length > 1}
          fallback={
            <span class="text-xs text-muted">{unitNames()[0]}</span>
          }
        >
          <select
            class={`${inputClass} !w-28`}
            aria-label={`${props.label} unit`}
            value={unit()}
            onChange={(e) => {
              // Changing the unit PRESERVES the total and re-expresses it (2
              // days -> 48 hours), matching DurationSetting in Advanced.tsx so
              // two unit pickers in the same Settings surface don't behave
              // oppositely. The re-expression is lossless: fractional amounts
              // are allowed and emit() rounds only on the way to the wire.
              const total = amount() * props.units[unit()];
              const next = e.currentTarget.value as U;
              setUnit(() => next);
              setAmount(total / props.units[next]);
              emit();
            }}
          >
            {unitNames().map((u) => (
              <option value={u}>{u}</option>
            ))}
          </select>
        </Show>
      </div>
      <Show when={props.help}>
        <Muted class="mt-1">{props.help}</Muted>
      </Show>
    </label>
  );
};

// TorrentSettingsCard owns the whole settings document. Load state follows
// Usenet's AutoGrabCard rather than a createResource: the document is editable,
// so it lives in a plain signal the controls mutate, and a scoped loadError
// signal keeps a fetch failure from taking down anything rendered beside it.
const TorrentSettingsCard: Component = () => {
  const [config, setConfig] = createSignal<DownloaderConfig | null>(null);
  const [dirty, setDirty] = createSignal(false);
  const [loadError, setLoadError] = createSignal<Error | null>(null);
  // autoGrab is only used for one honest caveat in the stale-handling copy; it
  // stays null when unknown (not yet loaded, or the request failed) so a
  // failure shows no note at all rather than an incorrect one.
  const [autoGrab, setAutoGrab] = createSignal<boolean | null>(null);
  // applyNote holds the outcome of the last save. Three kinds, deliberately
  // distinct: "live" (nothing was interrupted), "rebuilt" (the engine
  // restarted — a warning, not an error, so it is never rendered red), and
  // "refused" (a 409: nothing saved, nothing applied, retry in a moment).
  const [applyNote, setApplyNote] = createSignal<{
    kind: "live" | "rebuilt" | "refused";
    text: string;
  } | null>(null);
  const status = useSaveStatus();

  onMount(() => {
    void fetchDownloaderConfig()
      .then((c) => setConfig(c))
      .catch((e) => setLoadError(e instanceof Error ? e : new Error(String(e))));
    void fetchUsenetAutoGrabEnabled()
      .then((v) => setAutoGrab(v))
      .catch(() => setAutoGrab(null));
  });

  // patch mutates ONE field of the whole document — never rebuilds it from the
  // rendered controls. See rule 1 in this file's header: that is what keeps
  // maxConcurrent (which has no control) round-tripping correctly for free.
  const patch = <K extends keyof DownloaderConfig>(
    key: K,
    value: DownloaderConfig[K],
  ) => {
    setConfig((c) => (c ? { ...c, [key]: value } : c));
    setDirty(true);
  };

  // valid mirrors the PUT handler's validation client-side. Registered with the
  // enclosing SectionSave so its one Save button blocks while any field is out
  // of range, rather than reporting a 400 after the click.
  const valid = () => {
    const c = config();
    if (!c) return true;
    return (
      c.listenPort >= LISTEN_PORT_MIN &&
      c.listenPort <= LISTEN_PORT_MAX &&
      c.maxConnections >= 1 &&
      c.maxConcurrent >= 1 &&
      c.downloadRateLimitBytes >= 0 &&
      c.seedRatioLimit >= 0 &&
      c.seedDurationMinutes >= 0 &&
      c.staleThresholdMinutes >= 0
    );
  };

  // save PUTs the whole loaded document back, untouched fields included. It
  // refuses to fire at all when the initial GET never landed — PUTting a
  // fabricated document would overwrite every stored setting with defaults.
  const save = async (): Promise<SectionSaveOutcome> => {
    const cfg = config();
    if (!cfg) {
      const err = new Error("settings not loaded");
      status.failed(err);
      throw err;
    }
    setApplyNote(null);
    try {
      const result = await putDownloaderConfig(cfg);
      setDirty(false);
      // The server's message is written to be shown verbatim — it is the only
      // place the live-vs-rebuilt consequence is described, and duplicating it
      // here would let the two drift.
      setApplyNote({
        kind: result.restartRequired ? "rebuilt" : "live",
        text: result.message,
      });
      status.set("✓ saved");
    } catch (e) {
      if (isRebuildRefused(e)) {
        // A 409 is a "not right now", not a rejection of the settings: nothing
        // was persisted and nothing was applied, and the server's message names
        // the download that blocked the engine restart. Saying "failed" here
        // would send the operator hunting for a bad value that doesn't exist.
        setApplyNote({
          kind: "refused",
          text: `Not applied yet — the torrent engine couldn't restart just now, so nothing was saved or changed. Try saving again once that download finishes. (${(e as Error).message})`,
        });
        status.set("not applied — see below");
        // Reported to the section as "not-applied" rather than rethrown. A
        // rejection would print a red "failed: torrent settings" summary right
        // under the amber note above, which says the opposite — retry in a
        // moment. Resolving plainly would be worse still: the section would
        // claim a save that never reached the server. `dirty` deliberately
        // stays true (setDirty(false) runs only on the success path), so the
        // section's Save button stays live for the retry the note asks for.
        return "not-applied";
      }
      status.failed(e);
      throw e;
    }
  };

  // Registered with the enclosing SectionSave so the page's one Save button
  // drives this card — TorrentSection always provides one, so the card renders
  // no Save button of its own, only the shared inline status line.
  useSectionSaveItem({
    id: "torrent-config",
    label: "torrent settings",
    dirty,
    valid,
    save,
  });

  return (
    <Card title="Torrent behavior">
      {/* "not loaded" is two states, not one: still in flight, or the GET
          failed. The fallback is gated on loadError so a failure shows the
          error alone rather than a permanent "Loading…" line above it. */}
      <Show
        when={config()}
        fallback={
          <Show when={!loadError()}>
            <Muted>Loading torrent settings…</Muted>
          </Show>
        }
      >
        <Group title="Engine">
          <Muted class="mb-2">
            Changing anything in this group restarts the torrent engine, which
            briefly interrupts downloads already in flight.
          </Muted>
          <label class="mb-3 block">
            <span class={labelClass}>Staging directory</span>
            <input
              type="text"
              class={`${inputClass} mt-1`}
              placeholder="leave empty for the default"
              aria-label="Staging directory"
              value={config()!.stagingDir}
              onInput={(e) => patch("stagingDir", e.currentTarget.value)}
            />
            <Muted class="mt-1">
              Where in-progress downloads are written before they are imported
              into your library. Empty means the default inside SAK's data
              directory.
            </Muted>
          </label>
          <label class="mb-3 block">
            <span class={labelClass}>Listen port</span>
            <input
              type="number"
              min={LISTEN_PORT_MIN}
              max={LISTEN_PORT_MAX}
              class={`${inputClass} mt-1 !w-40`}
              aria-label="Listen port"
              value={config()!.listenPort}
              onInput={(e) => {
                const n = Number(e.currentTarget.value);
                if (Number.isNaN(n)) return;
                patch("listenPort", n);
              }}
            />
            <Muted class="mt-1">
              The port peers connect to, {LISTEN_PORT_MIN}–{LISTEN_PORT_MAX}.
            </Muted>
          </label>
          <label class="mb-2 flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable DHT"
              checked={config()!.dhtEnabled}
              onChange={(e) => patch("dhtEnabled", e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Enable DHT</span>
          </label>
          <label class="mb-3 flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable PEX"
              checked={config()!.pexEnabled}
              onChange={(e) => patch("pexEnabled", e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Enable PEX</span>
          </label>
          <Muted class="mb-3">
            DHT and PEX are the two ways SAK finds peers beyond the ones a
            tracker hands it. Turning both off shrinks the reachable swarm.
          </Muted>
          <label class="mb-3 block">
            <span class={labelClass}>Protocol obfuscation</span>
            <select
              class={`${inputClass} mt-1 !w-64`}
              aria-label="Protocol obfuscation"
              value={config()!.obfuscationMode}
              onChange={(e) => patch("obfuscationMode", e.currentTarget.value)}
            >
              <option value="require">Require</option>
              <option value="prefer">Prefer</option>
              {/* "Off" here is the STRICTEST setting, not the most permissive
                  one: it maps to "unobfuscated is required", so every
                  encrypted peer connection is refused outright. The qualifier
                  in this label is load-bearing — a bare "Off" would promise
                  softer behavior than what actually ships. */}
              <option value="off">Off (reject encrypted peers)</option>
            </select>
            <Muted class="mt-1">
              Off refuses encrypted peer connections entirely, which may reduce
              the number of peers you can connect to. Prefer is the usual
              choice.
            </Muted>
          </label>
        </Group>

        <Group title="Performance">
          <label class="mb-3 block">
            <span class={labelClass}>Max connections per torrent</span>
            <input
              type="number"
              min={1}
              class={`${inputClass} mt-1 !w-40`}
              aria-label="Max connections per torrent"
              value={config()!.maxConnections}
              onInput={(e) => {
                const n = Number(e.currentTarget.value);
                if (Number.isNaN(n)) return;
                patch("maxConnections", n);
              }}
            />
            <Muted class="mt-1">
              At least 1. Applies immediately — no engine restart.
            </Muted>
          </label>
          <UnitAmount
            label="Download rate limit"
            help="0 is unlimited. Applies immediately — no engine restart."
            units={RATE_UNITS}
            initial={config()!.downloadRateLimitBytes}
            onChange={(v) => patch("downloadRateLimitBytes", v)}
          />
        </Group>

        <Group title="Seeding">
          <label class="mb-2 flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Enable seeding"
              checked={config()!.seedingEnabled}
              onChange={(e) => patch("seedingEnabled", e.currentTarget.checked)}
            />
            <span class="text-sm text-fg">Enable seeding</span>
          </label>
          <Muted class="mb-3">
            Off by default. Seeding keeps a second copy of the completed file in
            the staging directory until a limit is reached, so it uses roughly
            twice the disk space for that period. The copy in your library is
            never affected. Turning seeding on restarts the torrent engine;
            turning it off applies immediately.
          </Muted>
          <label class="mb-3 block">
            <span class={labelClass}>Seed ratio limit</span>
            <input
              type="number"
              min={0}
              step="0.1"
              class={`${inputClass} mt-1 !w-40`}
              aria-label="Seed ratio limit"
              value={config()!.seedRatioLimit}
              onInput={(e) => {
                const n = Number(e.currentTarget.value);
                if (Number.isNaN(n)) return;
                patch("seedRatioLimit", n);
              }}
            />
            <Muted class="mt-1">0 is no ratio limit.</Muted>
          </label>
          <UnitAmount
            label="Seed duration limit"
            help="0 is no duration limit."
            units={DURATION_UNITS}
            initial={config()!.seedDurationMinutes}
            onChange={(v) => patch("seedDurationMinutes", v)}
          />
          <Muted>Both limits apply — whichever is reached first stops seeding.</Muted>
        </Group>

        <Group title="Stale handling">
          <UnitAmount
            label="Stale torrent threshold"
            units={{ hours: 60 }}
            initial={config()!.staleThresholdMinutes}
            onChange={(v) => patch("staleThresholdMinutes", v)}
          />
          <Muted>
            A torrent with no progress and no connected peers for this long is
            cancelled, its partial files deleted, and the title requeued in
            Requests for another attempt. 0 disables the check.
          </Muted>
          {/* Honest about what a requeue actually does on a default install:
              the retry loop only re-searches parked titles when unattended
              auto-grab is on, so with it off the title waits for the operator
              rather than silently retrying. Rendered only when the setting is
              known to be off — never guessed. */}
          <Show when={autoGrab() === false}>
            <Muted class="mt-1">
              Unattended auto-grab is off, so a requeued title waits in Requests
              for you to search for it again — it will not be retried on its
              own.
            </Muted>
          </Show>
        </Group>
      </Show>
      <Show when={loadError()}>
        <ErrorText>
          Couldn't load the torrent settings: {loadError()?.message}
        </ErrorText>
      </Show>
      {/* The apply note is deliberately not routed through SaveStatus: a
          rebuild and a 409 are both non-error outcomes that still need visual
          weight, and SaveStatus only knows "muted" and "red". */}
      <Show when={applyNote()}>
        <p
          class={`mt-3 text-sm ${
            applyNote()!.kind === "live" ? "text-muted" : "text-warn"
          }`}
        >
          {applyNote()!.text}
        </p>
      </Show>
      <div class="mt-3">
        <SaveStatus text={status.status().text} error={status.status().error} />
      </div>
    </Card>
  );
};
