// Package mode builds the live client(s) for one of Tidyarr's three
// isolated modes — Movies, Series, or Adult — from whatever connection is
// currently configured in Settings. A Session is cheap to build (an HTTP
// client wrapper, nothing cached), so it's constructed fresh per request:
// a connection edited in Settings takes effect on the very next Scan/Apply,
// no restart required.
package mode

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
)

// Mode is one of Tidyarr's three isolated library contexts. Never blended —
// see the design spec's "Mode replaces checkboxes" section for why.
type Mode string

const (
	Movies Mode = "movies"
	Series Mode = "series"
	Adult  Mode = "adult"
)

// service reports which connections.Store key and servarr.App back this
// mode's primary client.
func (m Mode) service() (service string, app servarr.App, err error) {
	switch m {
	case Movies:
		return "radarr", servarr.Radarr, nil
	case Series:
		return "sonarr", servarr.Sonarr, nil
	case Adult:
		// Whisparr's Add() is fully implemented (see internal/servarr), but
		// Adult mode's identification pipeline (StashDB/FansDB/TPDB/Ollama,
		// internal/identify) isn't wired into a Session yet — that's a
		// separate, larger piece of work, not something to half-build here.
		return "", 0, fmt.Errorf("mode %q: Adult mode isn't wired up yet", m)
	default:
		return "", 0, fmt.Errorf("mode %q: unknown mode", m)
	}
}

// Session holds the live client(s) for one mode.
type Session struct {
	Mode    Mode
	Servarr *servarr.Client
}

// Build constructs a Session for m using the connection currently configured
// in store. Returns an error if m isn't supported yet, or if its service has
// no connection configured (Settings hasn't been filled in for it yet).
func Build(ctx context.Context, store *connections.Store, httpClient *http.Client, m Mode) (*Session, error) {
	service, app, err := m.service()
	if err != nil {
		return nil, err
	}
	conn, err := store.Get(ctx, service)
	if err != nil {
		if errors.Is(err, connections.ErrNotFound) {
			return nil, fmt.Errorf("mode %q: %s isn't configured yet — add it in Settings first", m, service)
		}
		return nil, fmt.Errorf("mode %q: loading %s connection: %w", m, service, err)
	}
	client := servarr.New(servarr.Config{BaseURL: conn.URL, APIKey: conn.APIKey, App: app}, httpClient)
	return &Session{Mode: m, Servarr: client}, nil
}
