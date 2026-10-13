import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import {
  DualRangeSlider,
  resolutionStopsFromWire,
  resolutionStopsToWire,
} from "./ui";

const TIERS = ["low", "medium", "high", "lossless"] as const;
const TIER_LABELS: Record<(typeof TIERS)[number], string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  lossless: "Lossless",
};

describe("DualRangeSlider", () => {
  it("moves the ceiling thumb without changing the floor", () => {
    const [low, setLow] = createSignal<(typeof TIERS)[number]>("medium");
    const [high, setHigh] = createSignal<(typeof TIERS)[number]>("lossless");
    render(() => (
      <DualRangeSlider
        label="Quality"
        options={[...TIERS]}
        optionLabels={TIER_LABELS}
        low={low()}
        high={high()}
        lowAriaLabel="Quality floor"
        highAriaLabel="Quality ceiling"
        onChange={(a, b) => {
          setLow(a);
          setHigh(b);
        }}
      />
    ));
    expect(screen.getByText("Medium – Lossless")).toBeInTheDocument();
    fireEvent.input(screen.getByLabelText("Quality ceiling"), {
      target: { value: "2" },
    });
    expect(screen.getByText("Medium – High")).toBeInTheDocument();
    expect(screen.getByLabelText("Quality floor")).toHaveValue("1");
  });

  it("clamps a floor thumb that would cross the ceiling", () => {
    const [low, setLow] = createSignal<(typeof TIERS)[number]>("low");
    const [high, setHigh] = createSignal<(typeof TIERS)[number]>("medium");
    render(() => (
      <DualRangeSlider
        label="Quality"
        options={[...TIERS]}
        optionLabels={TIER_LABELS}
        low={low()}
        high={high()}
        lowAriaLabel="Quality floor"
        highAriaLabel="Quality ceiling"
        onChange={(a, b) => {
          setLow(a);
          setHigh(b);
        }}
      />
    ));
    fireEvent.input(screen.getByLabelText("Quality floor"), {
      target: { value: "3" },
    });
    expect(screen.getByText("Medium – Medium")).toBeInTheDocument();
  });
});

describe("resolutionStopsFromWire / ToWire", () => {
  it("maps an open band onto the full 480–2160 track", () => {
    expect(resolutionStopsFromWire(0, 0)).toEqual({ low: "480", high: "2160" });
    expect(resolutionStopsToWire("480", "2160")).toEqual({ min: 0, max: 0 });
  });

  it("keeps an inward 720–1080 band as hard min and max", () => {
    expect(resolutionStopsFromWire(720, 1080)).toEqual({
      low: "720",
      high: "1080",
    });
    expect(resolutionStopsToWire("720", "1080")).toEqual({
      min: 720,
      max: 1080,
    });
  });
});
