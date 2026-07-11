// Package oidcauth wraps SAK's OpenID Connect Relying Party flow — the
// Authorization Code flow with PKCE — where SAK is the RP and any OIDC
// provider (Authentik, Authelia, Keycloak, ...) is the IdP. It replaces the
// two earlier browser-facing-ish modes (a reverse-proxy shared-secret
// "forward" mode and an RFC 7662 bearer-introspection "authentik" mode),
// both of which were architecturally wrong for a real browser login: the
// former forced a live shared secret into the proxy's config (against this
// deployment's secrets policy, and not even how Authentik/Authelia's own
// model works — they rely on header-stripping + network isolation, no shared
// secret), and the latter was built for API/machine callers that already
// hold a token, never a real redirect/callback login.
//
// This flow is provider-agnostic and cryptographically verified: the ID
// token's signature is checked against the IdP's published JWKS (not a
// trusted proxy header), the issuer/audience/expiry are validated by
// go-oidc, and the nonce is checked here against the value SAK planted in the
// authorization request. Successfully completing it IS the one operator
// authenticating — consistent with SAK's single-operator model (CLAUDE.md):
// there is no subject-allowlist step, because restricting WHO may complete
// the IdP's login screen is the IdP's job (its own Application/Provider
// policy bindings), not SAK's. On success the handler issues the exact same
// signed session cookie password mode uses (see internal/auth's IssueToken/
// SetSessionCookie), so every subsequent per-request check is unchanged.
//
// Construction does live discovery (oidc.NewProvider fetches the provider's
// /.well-known/openid-configuration), so — unlike SAK's pure house HTTP
// clients — a Client is built per-request from stored config rather than once
// at boot, mirroring internal/auth's old per-request authentikClient shape.
// This is idiomatic for go-oidc, which needs the discovery document to build
// its Endpoint and verifier up front.
package oidcauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config is the operator-supplied OIDC Relying Party configuration.
//
// RedirectURL is required as an explicit, operator-supplied value (e.g.
// https://media-admin.zaena.us/api/auth/oidc/callback) and is NEVER derived
// from the request Host header — a Host header is client-spoofable, so
// deriving the redirect (and therefore where the IdP sends the code) from it
// would let an attacker steer the callback. It gets the same
// treated-as-config discipline as the issuer and client id/secret.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Client bundles the discovered provider, the oauth2 code-exchange config,
// and the ID-token verifier for one OIDC configuration. It carries the
// bounded *http.Client so every outbound leg (discovery already happened in
// New; token exchange and JWKS fetch happen later) shares SAK's single
// outboundTimeout, the same convention as every other external client.
type Client struct {
	provider   *oidc.Provider
	oauth      *oauth2.Config
	verifier   *oidc.IDTokenVerifier
	httpClient *http.Client
}

// New performs discovery against cfg.IssuerURL and returns a ready Client.
// The provided httpClient is threaded through both discovery here and every
// later outbound call (via ctxWithClient) so all three legs — discovery,
// token exchange, JWKS fetch — share the same bounded transport.
func New(ctx context.Context, cfg Config, httpClient *http.Client) (*Client, error) {
	provider, err := oidc.NewProvider(ctxWithClient(ctx, httpClient), cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return &Client{provider: provider, oauth: oauthCfg, verifier: verifier, httpClient: httpClient}, nil
}

// AuthCodeURL builds the IdP authorization URL the browser is redirected to,
// carrying the CSRF state, the OIDC nonce (oidc.Nonce), and the PKCE S256
// challenge derived from pkceVerifier. The caller must persist state, nonce,
// and pkceVerifier (server-side, in the short-lived flow cookie) to validate
// the callback.
func (c *Client) AuthCodeURL(state, nonce, pkceVerifier string) string {
	return c.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(pkceVerifier),
	)
}

// Exchange completes the callback: it swaps code for tokens (presenting the
// PKCE verifier), pulls the id_token out of the token response, verifies it
// (issuer, audience==clientID, signature via the IdP's JWKS, expiry) and then
// checks the nonce claim matches expectedNonce — go-oidc deliberately does
// NOT verify nonce itself (it's the RP's responsibility), so that final
// compare is done here and is mandatory. Any failure at any step returns a
// non-nil error and a nil token; the caller must fail closed (issue no
// session cookie).
func (c *Client) Exchange(ctx context.Context, code, pkceVerifier, expectedNonce string) (*oidc.IDToken, error) {
	ctx = ctxWithClient(ctx, c.httpClient)

	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return nil, fmt.Errorf("oidc code exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("oidc: token response missing id_token")
	}
	idToken, err := c.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc id token verify: %w", err)
	}
	if idToken.Nonce != expectedNonce {
		return nil, errors.New("oidc: id token nonce mismatch")
	}
	return idToken, nil
}

// ctxWithClient threads SAK's bounded HTTP client into both the go-oidc
// (oidc.ClientContext, used for discovery and JWKS fetch) and oauth2
// (oauth2.HTTPClient, used for the token exchange) lookup paths, so no leg of
// the flow escapes the shared outboundTimeout.
func ctxWithClient(ctx context.Context, httpClient *http.Client) context.Context {
	if httpClient == nil {
		return ctx
	}
	ctx = oidc.ClientContext(ctx, httpClient)
	return context.WithValue(ctx, oauth2.HTTPClient, httpClient)
}
