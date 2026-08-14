import { type Component, For } from "solid-js";
import Star from "lucide-solid/icons/star";

// StarRating is the operator 1–5 control used on Library cards (sibling
// overlay — never nested inside MediaCardShell) and in the detail panel.
// Clicking the current rating again clears it (sends 0).
export const StarRating: Component<{
  rating: number;
  onChange?: (rating: number) => void;
  size?: "sm" | "md";
}> = (props) => {
  const interactive = () => !!props.onChange;
  const iconClass = () =>
    props.size === "md" ? "h-5 w-5" : "h-3.5 w-3.5";
  const set = (n: number) => {
    const next = n === props.rating ? 0 : n;
    props.onChange?.(next);
  };
  return (
    <div
      class="flex items-center gap-0.5"
      role={interactive() ? "group" : "img"}
      aria-label={
        props.rating > 0 ? `Rated ${props.rating} of 5` : "Unrated"
      }
    >
      <For each={[1, 2, 3, 4, 5]}>
        {(n) => {
          const filled = () => n <= (props.rating ?? 0);
          const star = (
            <Star
              class={`${iconClass()} ${filled() ? "fill-accent text-accent" : "text-muted"}`}
            />
          );
          if (!interactive()) {
            return star;
          }
          return (
            <button
              type="button"
              class="rounded p-0.5 hover:text-accent focus:outline-none focus:ring-2 focus:ring-accent"
              aria-label={n === 1 ? "Rate 1 star" : `Rate ${n} stars`}
              onClick={(e) => {
                e.stopPropagation();
                set(n);
              }}
            >
              {star}
            </button>
          );
        }}
      </For>
    </div>
  );
};
