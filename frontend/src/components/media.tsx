import { type Component, type JSX, Index, Show } from "solid-js";

type MediaGridSkeletonProps = {
  count?: number;
};

export const MediaGridSkeleton: Component<MediaGridSkeletonProps> = (props) => {
  const count = () => props.count ?? 12;
  return (
    <div
      class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6"
      role="status"
      aria-live="polite"
      aria-busy="true"
      aria-label="Loading media"
    >
      <Index each={Array.from({ length: count() })}>
        {() => (
          <div
            class="overflow-hidden rounded-lg border-2 border-transparent bg-surface"
            aria-hidden="true"
          >
            <div
              class="w-full animate-pulse bg-surface-2"
              style="aspect-ratio: 2 / 3"
            />
            <div class="space-y-1.5 p-2">
              <div class="h-2.5 w-3/4 animate-pulse rounded bg-surface-2" />
              <div class="h-2 w-1/3 animate-pulse rounded bg-surface-2" />
            </div>
          </div>
        )}
      </Index>
    </div>
  );
};

export const MediaCardShell: Component<{
  label: string;
  selected?: boolean;
  disabled?: boolean;
  class?: string;
  onClick: () => void;
  children: JSX.Element;
}> = (props) => (
  <button
    type="button"
    class={`flex w-full flex-col overflow-hidden rounded-lg border-2 bg-surface text-left transition focus:outline-none focus:ring-2 focus:ring-accent disabled:cursor-default ${props.class ?? ""}`}
    classList={{
      "border-accent": !!props.selected,
      "border-transparent": !props.selected,
    }}
    aria-pressed={props.selected}
    aria-label={props.label}
    disabled={props.disabled}
    onClick={props.onClick}
  >
    {props.children}
  </button>
);

export const MediaFallbackTile: Component<{
  title: string;
  loading?: boolean;
  error?: boolean;
}> = (props) => (
  <div
    class="flex h-full w-full items-center justify-center bg-surface-2 text-2xl font-bold text-muted"
    classList={{
      "animate-pulse": !!props.loading,
      "bg-danger/10 text-danger/60": !!props.error,
    }}
    aria-hidden="true"
  >
    {props.title.charAt(0).toUpperCase()}
  </div>
);

export const MediaBadge: Component<{
  children: JSX.Element;
  tone?: "default" | "accent" | "success" | "warning";
  class?: string;
}> = (props) => {
  const tone = () => props.tone ?? "default";
  return (
    <span
      class={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${props.class ?? ""}`}
      classList={{
        "bg-surface-2 text-muted": tone() === "default",
        "bg-accent text-accent-fg": tone() === "accent",
        "bg-ok/20 text-ok": tone() === "success",
        "bg-warn/20 text-warn": tone() === "warning",
      }}
    >
      {props.children}
    </span>
  );
};

export const MediaDetailShell: Component<{
  title: string;
  poster?: JSX.Element;
  hero?: JSX.Element;
  actions?: JSX.Element;
  children: JSX.Element;
}> = (props) => (
  <div class="overflow-hidden rounded-2xl border border-border bg-surface shadow-2xl">
    <Show when={props.hero}>
      <div class="relative min-h-48 bg-bg">{props.hero}</div>
    </Show>
    <div class="p-4 sm:p-6">
      <div class="flex flex-col gap-4 sm:flex-row">
        <Show when={props.poster}>
          <div class="mx-auto w-32 shrink-0 overflow-hidden rounded-lg border border-border sm:mx-0">
            {props.poster}
          </div>
        </Show>
        <div class="min-w-0 flex-1">
          <h2 class="text-xl font-semibold text-fg">{props.title}</h2>
          <Show when={props.actions}>
            <div class="mt-3 flex flex-wrap gap-2">{props.actions}</div>
          </Show>
          <div class="mt-4">{props.children}</div>
        </div>
      </div>
    </div>
  </div>
);
