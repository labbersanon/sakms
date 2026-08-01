// buildServiceConnectionBody tests — the registry's three-state secret gate,
// the direct twin of buildConnectionUpsertBody's own dedicated test in
// Settings.test.tsx. This is the #1 incident class in this project: an update
// that doesn't touch the secret field must OMIT `secret` entirely, because the
// backend reads a present-but-empty secret as "clear the stored one" and
// silently wipes a working password/API key.
//
// Plus sameModes, the guard deciding whether a player save fires the extra
// /modes request, and the two request wrappers whose URL/method/body shape the
// mode-assignment path depends on.

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  buildServiceConnectionBody,
  sameModes,
  setServiceConnectionModes,
  updateServiceConnection,
} from "./serviceConnections";

const base = {
  kind: "usenet",
  provider: "nntp",
  label: "Eweka",
  enabled: true,
  host: "news.example.com",
  port: 563,
  tls: true,
  maxConns: 8,
  username: "wade",
};

describe("buildServiceConnectionBody", () => {
  it("OMITS secret entirely when it wasn't touched and one is already stored", () => {
    const body = buildServiceConnectionBody({
      base,
      secretTouched: false,
      secretValue: "",
      hasExistingSecret: true,
    });
    expect("secret" in body).toBe(false);
    // The property must be genuinely absent on the wire, not undefined-but-present.
    expect(JSON.parse(JSON.stringify(body))).not.toHaveProperty("secret");
    // Everything else still round-trips, so an unrelated edit still saves.
    expect(body.host).toBe("news.example.com");
    expect(body.port).toBe(563);
  });

  it("includes a typed secret so a real change replaces the stored one", () => {
    const body = buildServiceConnectionBody({
      base,
      secretTouched: true,
      secretValue: "hunter2",
      hasExistingSecret: true,
    });
    expect(body.secret).toBe("hunter2");
  });

  it("includes an explicitly-emptied secret, the deliberate clear signal", () => {
    const body = buildServiceConnectionBody({
      base,
      secretTouched: true,
      secretValue: "",
      hasExistingSecret: true,
    });
    expect("secret" in body).toBe(true);
    expect(body.secret).toBe("");
  });

  it("includes a blank secret when none is stored yet, so a no-auth row can save", () => {
    const body = buildServiceConnectionBody({
      base,
      secretTouched: false,
      secretValue: "",
      hasExistingSecret: false,
    });
    expect("secret" in body).toBe(true);
    expect(body.secret).toBe("");
  });
});

describe("sameModes", () => {
  it("treats a reordered assignment as unchanged (no redundant /modes request)", () => {
    expect(sameModes(["series", "movies"], ["movies", "series"])).toBe(true);
  });

  it("detects an added mode", () => {
    expect(sameModes(["movies", "adult"], ["movies"])).toBe(false);
  });

  it("detects a removed mode", () => {
    expect(sameModes([], ["movies"])).toBe(false);
  });

  it("detects a swapped mode of the same length", () => {
    expect(sameModes(["adult"], ["movies"])).toBe(false);
  });
});

describe("registry request wrappers", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("updateServiceConnection PUTs the built body to the id route", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ id: 3 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await updateServiceConnection(
      3,
      buildServiceConnectionBody({
        base,
        secretTouched: false,
        secretValue: "",
        hasExistingSecret: true,
      }),
    );

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("/api/service-connections/3");
    expect(init!.method).toBe("PUT");
    // The wire body is the real assertion: no `secret` key reaches the server.
    expect(JSON.parse(init!.body as string)).not.toHaveProperty("secret");
  });

  it("setServiceConnectionModes PUTs the FULL assignment to the modes route", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ id: 3 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await setServiceConnectionModes(3, ["movies", "adult"]);

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("/api/service-connections/3/modes");
    expect(init!.method).toBe("PUT");
    expect(JSON.parse(init!.body as string)).toEqual({
      modes: ["movies", "adult"],
    });
  });
});
