// The authed app shell. Past auth it renders the client-side router; the
// landing view is the read-only Discover browse (Stage 1 Wave 3). Later waves
// add the remaining views (Settings, workflows) and Discover's auto-grab. The
// router must never claim an /api/* path (see APP_ROUTES).

import { type Component, type JSX, createSignal, Show } from "solid-js";
import { A, Route, Router } from "@solidjs/router";
import { Button, ErrorText, Muted } from "../components/ui";
import { Discover } from "./Discover";
import { Grabs } from "./Grabs";
import { Rename } from "./Rename";
import { Purge } from "./Purge";
import { Dedup } from "./Dedup";
import { Tag } from "./Tag";
import { Settings } from "./Settings";

// APP_ROUTES is the exhaustive list of client-side route patterns the router
// serves. Guardrail #2 / requirement #7: the router must NEVER claim any
// /api/* path (the OIDC callback /api/auth/oidc/callback is a real server
// route). A unit test asserts none of these start with "/api".
export const APP_ROUTES = ["/", "/discover", "/grabs", "/rename", "/purge", "/dedup", "/tag", "/settings"] as const;

// ShellLayout is the Router root — a tab nav (Discover / Grabs) above whatever
// route is active. Being inside <Router> is what gives <A> its active-link
// context.
const ShellLayout: Component<{ children?: JSX.Element }> = (props) => (
  <>
    <nav class="mb-4 flex gap-3 border-b border-border pb-2">
      <A
        href="/discover"
        class="text-sm font-medium text-muted hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Discover
      </A>
      <A
        href="/grabs"
        class="text-sm font-medium hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Grabs
      </A>
      <A
        href="/rename"
        class="text-sm font-medium hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Rename
      </A>
      <A
        href="/purge"
        class="text-sm font-medium hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Purge
      </A>
      <A
        href="/dedup"
        class="text-sm font-medium hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Dedup
      </A>
      <A
        href="/tag"
        class="text-sm font-medium hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Tag
      </A>
      <A
        href="/settings"
        class="text-sm font-medium hover:text-fg"
        activeClass="text-fg"
        inactiveClass="text-muted"
      >
        Settings
      </A>
    </nav>
    {props.children}
  </>
);

const NotFound: Component = () => (
  <div class="rounded-xl border border-border bg-surface p-6">
    <h1 class="text-xl font-semibold text-fg">Not found</h1>
    <Muted class="mt-2">No such view. This is the SPA catch-all fallback.</Muted>
  </div>
);

export const AppShell: Component<{
  noneMode: boolean;
  connectionsSetupPending: boolean;
  onLoggedOut: () => void;
}> = (props) => {
  const [logoutError, setLogoutError] = createSignal("");

  const logout = async () => {
    setLogoutError("");
    try {
      await fetch("/api/auth/logout", { method: "POST" });
      props.onLoggedOut();
    } catch (err) {
      setLogoutError((err as Error).message);
    }
  };

  return (
    <div class="min-h-screen">
      <header class="flex items-center gap-4 border-b border-border bg-surface px-6 py-3">
        <span class="font-semibold text-fg">SAK Media Server</span>
        <div class="ml-auto">
          <Button onClick={logout}>Log out</Button>
        </div>
      </header>

      <Show when={props.noneMode}>
        <div class="border-b border-border bg-surface-2 px-6 py-2">
          <span class="text-sm text-danger">
            Authentication is disabled for this instance — it and every connected
            service is reachable by anyone who can reach it. Switch to a different
            mode in Settings to fix this.
          </span>
        </div>
      </Show>

      <Show when={props.connectionsSetupPending}>
        <div class="border-b border-border bg-surface-2 px-6 py-2">
          <span class="text-sm text-muted">
            First-run connections setup hasn't been dismissed yet — the setup
            wizard lands in a later wave.
          </span>
        </div>
      </Show>

      <main class="p-6">
        {logoutError() && <ErrorText>{logoutError()}</ErrorText>}
        <Router root={ShellLayout}>
          <Route path="/" component={Discover} />
          <Route path="/discover" component={Discover} />
          <Route path="/grabs" component={Grabs} />
          <Route path="/rename" component={Rename} />
          <Route path="/purge" component={Purge} />
          <Route path="/dedup" component={Dedup} />
          <Route path="/tag" component={Tag} />
          <Route
            path="/settings"
            component={() => <Settings onReboot={props.onLoggedOut} />}
          />
          <Route path="*" component={NotFound} />
        </Router>
      </main>
    </div>
  );
};
