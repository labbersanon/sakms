// Package trakt is a client + persistence layer for Trakt.tv, used to back
// Discover's Watchlist row: titles the operator marked "want to watch" on
// Trakt but doesn't own yet (unlike Jellyfin, Trakt actually models this
// concept; unlike Plex, sakms doesn't run Plex at all — see the frontend
// redesign plan for why Trakt was chosen over both).
//
// Trakt requires a registered "application" (client_id + client_secret,
// operator-created at https://trakt.tv/oauth/applications, never hardcoded
// here) plus a per-account OAuth link obtained via the device-code flow
// (RequestDeviceCode + PollDeviceToken) — the right flow for a headless
// server with no browser redirect callback, the same reasoning that makes
// device flow the standard choice for TVs/CLIs. Config (client_id/secret)
// and Tokens (access/refresh token + expiry) are stored separately by
// Store: config is operator-entered via Settings, tokens are only ever
// produced by a successful device-flow exchange or a refresh, never typed
// in directly — see store.go's Credentials/Tokens split.
//
// Session ties Store and Client together to own one invariant end to end:
// nothing calls Watchlist with a stale access token. A loose Client.Watchlist
// primitive is exposed for testability, but the only entry point that
// matters for callers is Session.Watchlist, which loads the stored tokens,
// refreshes (and persists the new tokens) if the access token is at or past
// its expiry, and only then calls the API — that sequencing lives here, not
// left to whichever caller wires up the HTTP route.
//
// Honesty about unverified assumptions (this project's convention): every
// request/response shape here (device/code, device/token, oauth/token
// refresh, sync/watchlist) is modeled from Trakt's public developer
// documentation (docs.trakt.tv) and corroborated by independent third-party
// client implementations (e.g. github.com/BrenekH/go-traktdeviceauth's
// CodeResponse field names match byte-for-byte), NOT confirmed against a
// live Trakt account — no real Trakt application credentials exist yet as
// of this package's writing. Tests exercise this package's own logic against
// httptest fixtures built from that documented/corroborated shape; they
// prove internal correctness, not fidelity to Trakt's actual live behavior.
// See this package's final report for exactly what remains unverified.
package trakt
