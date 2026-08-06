package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Claude 2026-08-06: TMDB user session for mainstream give-back
// Reason: deep-interview-rename-apply-all-giveback-settings — authenticated contribute.
// Troubleshooting: contribute soft-fails without session; TMDB has no create-title API.
// Review if: TMDB publishes a real contribution write endpoint.

type tmdbSessionStatusResponse struct {
	Configured bool `json:"configured"`
}

type tmdbSessionLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func getTMDBSessionHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := settingsStore.Get(r.Context(), mode.TMDBSessionIDKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tmdbSessionStatusResponse{Configured: strings.TrimSpace(v) != ""})
	}
}

func putTMDBSessionHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req tmdbSessionLoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Username) == "" || req.Password == "" {
			http.Error(w, "username and password required", http.StatusBadRequest)
			return
		}
		conn, err := connStore.Get(r.Context(), "tmdb")
		if err != nil {
			http.Error(w, "tmdb connection required: "+err.Error(), http.StatusBadRequest)
			return
		}
		client := tmdb.New(tmdb.Config{BaseURL: tmdb.DefaultBaseURL, APIKey: conn.APIKey}, httpClient)
		sessionID, err := client.CreateSessionWithLogin(r.Context(), req.Username, req.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := settingsStore.Set(r.Context(), mode.TMDBSessionIDKey, sessionID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteTMDBSessionHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := settingsStore.Set(r.Context(), mode.TMDBSessionIDKey, ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// maybeAutoGiveBack runs after Rename scan persist: Adult SubmitDraft and/or
// mainstream TMDB contribute when the matching toggle is on and the proposal
// was web-identified. Soft-fail only — never changes HTTP success of Scan.
func maybeAutoGiveBack(
	ctx context.Context,
	m mode.Mode,
	sess *mode.Session,
	settingsStore *settings.Store,
	propStore *proposals.Store,
	saved []proposals.Proposal,
) {
	switch m {
	case mode.Adult:
		on, err := settingsStore.GetBool(ctx, mode.RenameGiveBackAdultKey, false)
		if err != nil || !on {
			return
		}
		for _, p := range saved {
			if p.Status != proposals.Unmatched || p.Title == "" || p.DraftID != "" {
				continue
			}
			if !strings.Contains(strings.ToLower(p.Reason), "web-identified") {
				continue
			}
			draftID, err := rename.SubmitDraft(ctx, sess, p)
			if err != nil {
				log.Printf("rename give-back (adult): proposal %d soft-fail: %v", p.ID, err)
				continue
			}
			if err := propStore.MarkDraftSubmitted(ctx, p.ID, draftID); err != nil {
				log.Printf("rename give-back (adult): mark draft %d soft-fail: %v", p.ID, err)
			}
		}
	case mode.Movies, mode.Series:
		on, err := settingsStore.GetBool(ctx, mode.RenameGiveBackMainstreamKey, false)
		if err != nil || !on {
			return
		}
		sessionID, _ := settingsStore.Get(ctx, mode.TMDBSessionIDKey)
		for _, p := range saved {
			if !strings.HasPrefix(strings.ToLower(p.Reason), "web match:") {
				continue
			}
			if err := rename.ContributeTMDBTitle(ctx, sess, sessionID, p); err != nil {
				log.Printf("rename give-back (mainstream): proposal %d soft-fail: %v", p.ID, err)
			}
		}
	}
}
