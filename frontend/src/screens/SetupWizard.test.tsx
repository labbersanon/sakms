// Regression test for the "copy button not working" report: copyKey() used to
// be fire-and-forget (navigator.clipboard.writeText(...).catch(() => {})),
// giving zero visible feedback on success OR failure — a real clipboard
// failure (missing API, rejected write) looked identical to a working copy.
// These tests assert the button now shows an explicit outcome either way.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { SetupWizard } from "./SetupWizard";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

afterEach(() => {
  vi.unstubAllGlobals();
  Object.defineProperty(window.navigator, "clipboard", {
    value: undefined,
    configurable: true,
  });
});

const revealBreakGlassKey = async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse({ apiKey: "test-break-glass-key" })),
  );

  render(() => <SetupWizard onSetupComplete={() => {}} />);

  fireEvent.change(screen.getByLabelText("Authentication mode"), {
    target: { value: "oidc" },
  });
  fireEvent.input(screen.getByLabelText("Issuer URL"), {
    target: { value: "https://sso.example.com/application/o/sakms/" },
  });
  fireEvent.input(screen.getByLabelText("Client ID"), {
    target: { value: "client-id" },
  });
  fireEvent.input(screen.getByLabelText("Client secret"), {
    target: { value: "client-secret" },
  });
  fireEvent.input(screen.getByLabelText("Redirect URL"), {
    target: {
      value: "https://media-admin.example.com/api/auth/oidc/callback",
    },
  });
  fireEvent.click(screen.getByText("Save and continue to SSO"));

  expect(await screen.findByText("Copy")).toBeInTheDocument();
};

describe("SetupWizard — break-glass API key copy button", () => {
  it("shows explicit success feedback when the clipboard write succeeds", async () => {
    await revealBreakGlassKey();
    Object.defineProperty(window.navigator, "clipboard", {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });

    fireEvent.click(screen.getByText("Copy"));

    expect(await screen.findByText("Copied!")).toBeInTheDocument();
  });

  it("shows explicit failure feedback instead of silently doing nothing when the clipboard API is unavailable", async () => {
    await revealBreakGlassKey();
    // navigator.clipboard stays undefined (afterEach's default / no stub here).

    fireEvent.click(screen.getByText("Copy"));

    expect(
      await screen.findByText("Couldn't copy — select the field instead"),
    ).toBeInTheDocument();
  });

  it("shows explicit failure feedback when the clipboard write itself rejects", async () => {
    await revealBreakGlassKey();
    Object.defineProperty(window.navigator, "clipboard", {
      value: {
        writeText: vi.fn().mockRejectedValue(new Error("denied")),
      },
      configurable: true,
    });

    fireEvent.click(screen.getByText("Copy"));

    expect(
      await screen.findByText("Couldn't copy — select the field instead"),
    ).toBeInTheDocument();
  });
});

describe("SetupWizard — break-glass API key download button", () => {
  it("downloads the key as a .txt file via a Blob object URL", async () => {
    const createObjectURL = vi.fn((_blob: Blob) => "blob:mock-url");
    const revokeObjectURL = vi.fn();
    (URL as unknown as { createObjectURL: typeof createObjectURL }).createObjectURL =
      createObjectURL;
    (URL as unknown as { revokeObjectURL: typeof revokeObjectURL }).revokeObjectURL =
      revokeObjectURL;
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});

    await revealBreakGlassKey();
    fireEvent.click(screen.getByText("Download as text file"));

    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const blob = createObjectURL.mock.calls[0]![0] as Blob;
    expect(await blob.text()).toBe("test-break-glass-key\n");
    expect(clickSpy).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:mock-url");

    clickSpy.mockRestore();
  });
});
