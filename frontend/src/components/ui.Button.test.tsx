// Button variant must stay reactive: Solid runs the component once, so a
// one-shot class string freezes the first paint (Import Series stayed unselected).

import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { Button } from "./ui";

describe("Button variant", () => {
  it("updates primary classes when variant changes after click", () => {
    const [variant, setVariant] = createSignal<"primary" | "secondary">(
      "secondary",
    );
    render(() => (
      <Button variant={variant()} onClick={() => setVariant("primary")}>
        Chip
      </Button>
    ));
    const btn = screen.getByRole("button", { name: "Chip" });
    expect(btn).not.toHaveClass("bg-accent");
    fireEvent.click(btn);
    expect(btn).toHaveClass("bg-accent");
  });
});
