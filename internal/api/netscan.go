package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/netscan"
)

// LAN service probing for the setup wizard — an authenticated-operator
// convenience that offers to pre-fill a connection's URL (and, only on a
// separate explicit action, a Servarr-family service's API key) instead of
// typing it by hand. Every route here lives on NewMux, so it inherits the
// same session/API-key protection as every other connections-management
// route; nothing here is on the public setup/login mux. See internal/netscan's
// package doc for the security posture — every Finding is a hint to verify,
// never a trusted fact, and the general probe endpoints never return a
// credential.

// netscanKnownHandler tries the fixed list of conventional container
// hostnames (prowlarr/qbittorrent/nzbget/jellyfin) at their default ports and
// returns whatever confirmed. Never returns a credential (Prowlarr's key is
// fetched only via netscanProwlarrKeyHandler).
func netscanKnownHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		findings := netscan.ProbeKnownHosts(r.Context(), httpClient)
		if findings == nil {
			findings = []netscan.Finding{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(findings)
	}
}

// netscanHostHandler probes one operator-supplied host across the four known
// services' default ports. ProbeHost refuses any host that doesn't resolve to
// a private address, so a bad request (e.g. a public IP) surfaces as a 400.
func netscanHostHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Host string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		findings, err := netscan.ProbeHost(r.Context(), httpClient, req.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if findings == nil {
			findings = []netscan.Finding{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(findings)
	}
}

// netscanProwlarrKeyHandler re-fetches a Prowlarr instance's /initialize.json
// fresh from the given URL to retrieve its live API key — the one dedicated,
// explicit action that reads the key, never bundled into the probe responses
// above. Prowlarr's unauthenticated /initialize.json is the only known
// service endpoint (confirmed by direct check against a live instance) to
// expose a key this way.
func netscanProwlarrKeyHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		key, err := netscan.FetchProwlarrAPIKey(r.Context(), httpClient, req.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			APIKey string `json:"apiKey"`
		}{APIKey: key})
	}
}
