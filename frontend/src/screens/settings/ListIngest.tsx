// TMDB and IMDb list-ingest cards. Sit next to Trakt on Settings → UI →
// Discover. Same movie/series semantics as the Trakt watchlist switch.

import { type Component, createEffect, createResource, createSignal, Show } from "solid-js";
import {
  fetchIMDbListIngest,
  fetchTMDBListIngest,
  putIMDbListIngest,
  putTMDBListIngest,
} from "../../api/listIngest";
import { Button, ErrorText, inputClass, labelClass, Muted, Switch } from "../../components/ui";
import { Card } from "./shared";

const ingestHelp =
  "Each auto-grab cycle turns movies into Requests (or holds them until a US digital release) and adds new series with every season monitored. Titles already in the library are left alone. Auto-grab must be on. Turning this off cancels never-dispatched Requests; it does not un-monitor series.";

const textareaClass = `${inputClass} mt-1 min-h-[5.5rem] resize-y whitespace-pre-wrap`;

export const TMDBListIngestCard: Component = () => {
  const [cfg, { mutate }] = createResource(fetchTMDBListIngest);
  const [listIds, setListIds] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  createEffect(() => {
    const c = cfg();
    if (c) setListIds(c.listIds);
  });

  const save = async (enabled: boolean, ids: string) => {
    setBusy(true);
    setWriteError("");
    try {
      await putTMDBListIngest({ enabled, listIds: ids });
      mutate({
        enabled,
        listIds: ids,
        hasAccountSession: cfg()?.hasAccountSession ?? false,
      });
    } catch (e) {
      setWriteError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="TMDB lists">
      <Muted class="mb-3">
        Ingest public TMDB list IDs (one per line or comma-separated). If a
        TMDB session is signed in under Advanced (give-back), the account
        movie and TV watchlists are included too.
      </Muted>
      <Show when={cfg()?.hasAccountSession}>
        <p class="mb-2 text-xs text-ok">Account watchlist will be included.</p>
      </Show>
      <label class="mb-3 block">
        <span class={labelClass}>List IDs</span>
        <textarea
          class={textareaClass}
          aria-label="TMDB list IDs"
          placeholder={"12345\nhttps://www.themoviedb.org/list/7075775"}
          value={listIds()}
          onInput={(e) => setListIds(e.currentTarget.value)}
        />
      </label>
      <div class="mb-3">
        <Button
          variant="primary"
          disabled={busy() || cfg.loading}
          onClick={() => void save(cfg()?.enabled === true, listIds())}
        >
          Save list IDs
        </Button>
      </div>
      <div class="flex items-start justify-between gap-3">
        <div>
          <p class="text-xs font-medium text-fg">Add list titles automatically</p>
          <Muted class="mt-0.5">{ingestHelp}</Muted>
          <Show when={writeError()}>
            <ErrorText>{writeError()}</ErrorText>
          </Show>
        </div>
        <Switch
          checked={cfg()?.enabled === true}
          disabled={busy() || cfg.loading}
          ariaLabel="Add TMDB list titles automatically"
          onChange={(next) => void save(next, listIds())}
        />
      </div>
    </Card>
  );
};

export const IMDbListIngestCard: Component = () => {
  const [cfg, { mutate }] = createResource(fetchIMDbListIngest);
  const [listIds, setListIds] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  createEffect(() => {
    const c = cfg();
    if (c) setListIds(c.listIds);
  });

  const save = async (enabled: boolean, ids: string) => {
    setBusy(true);
    setWriteError("");
    try {
      await putIMDbListIngest({ enabled, listIds: ids });
      mutate({ enabled, listIds: ids });
    } catch (e) {
      setWriteError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="IMDb lists">
      <Muted class="mb-3">
        Ingest IMDb list IDs (ls…) or user watchlist IDs (ur…), one per line.
        Titles are resolved through TMDB. Full IMDb URLs are accepted.
      </Muted>
      <label class="mb-3 block">
        <span class={labelClass}>List IDs</span>
        <textarea
          class={textareaClass}
          aria-label="IMDb list IDs"
          placeholder={"ls123456789\nur987654321"}
          value={listIds()}
          onInput={(e) => setListIds(e.currentTarget.value)}
        />
      </label>
      <div class="mb-3">
        <Button
          variant="primary"
          disabled={busy() || cfg.loading}
          onClick={() => void save(cfg()?.enabled === true, listIds())}
        >
          Save list IDs
        </Button>
      </div>
      <div class="flex items-start justify-between gap-3">
        <div>
          <p class="text-xs font-medium text-fg">Add list titles automatically</p>
          <Muted class="mt-0.5">{ingestHelp}</Muted>
          <Show when={writeError()}>
            <ErrorText>{writeError()}</ErrorText>
          </Show>
        </div>
        <Switch
          checked={cfg()?.enabled === true}
          disabled={busy() || cfg.loading}
          ariaLabel="Add IMDb list titles automatically"
          onChange={(next) => void save(next, listIds())}
        />
      </div>
    </Card>
  );
};
