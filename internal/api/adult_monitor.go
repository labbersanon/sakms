// This file is the Adult monitored-entity API — the GET/PUT/interval routes
// that back the operator's monitor toggle on performer/studio drill-down pages.
//
// Routes:
//   GET  /api/modes/adult/discover/monitor?kind=&name= → AdultMonitorState
//   PUT  /api/modes/adult/discover/monitor              ← SetAdultMonitorRequest
//   GET  /api/modes/adult/newest-rows/monitored/resolve?page= → []AdultNewestReleaseItem
//   GET  /api/settings/adult-monitor-interval          → adultMonitorIntervalResponse
//   PUT  /api/settings/adult-monitor-interval          ← adultMonitorIntervalRequest
//
// The GET handler makes ZERO Prowlarr calls — it reads only the monitored store
// (and, if absent there, the pool table for entity_source). The PUT handler
// re-resolves the entity server-side (one box call, via identify.ResolveEntityID)
// and 409s when the entity cannot be resolved and monitored=true is requested.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// adultMonitorIntervalKey is the settings key for the monitor poll cadence.
// Mirrors adultnewest.MonitorIntervalSettingKey's import-avoidance convention.
const adultMonitorIntervalKey = "adult_monitor_interval_seconds"

// adultMonitorDefaultSeconds mirrors adultnewest.defaultMonitorIntervalSeconds.
const adultMonitorDefaultSeconds = 24 * 60 * 60

type adultMonitorIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type adultMonitorIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getAdultMonitorIntervalHandler returns the configured monitor interval in
// seconds — adultMonitorDefaultSeconds when the key was never explicitly saved,
// 0 when an operator explicitly saved "0" (turning the pass off), and whatever
// positive value was last saved otherwise.
func getAdultMonitorIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, adultMonitorIntervalKey, adultMonitorDefaultSeconds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, adultMonitorIntervalResponse{IntervalSeconds: secs})
	}
}

// putAdultMonitorIntervalHandler stores the monitor interval. 0 disables the
// pass; negative values are rejected.
func putAdultMonitorIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adultMonitorIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, adultMonitorIntervalKey, req.IntervalSeconds, 0)
		if err != nil {
			status := http.StatusInternalServerError
			if badRequest {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getAdultMonitorStateHandler is GET /api/modes/adult/discover/monitor?kind=&name=
// It returns the current monitor state for a performer/studio. ZERO Prowlarr
// calls are made — the response is built entirely from the monitored store and
// the pool table.
//
// kind must be "performer" or "studio"; name must be non-empty. Any other
// combination returns 400.
func getAdultMonitorStateHandler(monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		name := r.URL.Query().Get("name")
		if (kind != "performer" && kind != "studio") || name == "" {
			http.Error(w, "kind must be 'performer' or 'studio' and name must be non-empty", http.StatusBadRequest)
			return
		}
		ctx := r.Context()

		// Check the monitored store first — it already has the resolved entity.
		entity, err := monitoredStore.GetByKindName(ctx, kind, name)
		if err == nil {
			writeJSON(w, apidto.AdultMonitorState{
				Resolved:   entity.EntityID != "",
				Source:     entity.EntitySource,
				EntityID:   entity.EntityID,
				EntityName: entity.EntityName,
				Monitored:  entity.Monitored,
			})
			return
		}
		if !errors.Is(err, adultnewest.ErrMonitoredNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Not in monitored table — check pool for entity_source.
		rowType := adultnewest.RowPerformer
		if kind == "studio" {
			rowType = adultnewest.RowStudio
		}
		box, poolErr := releaseStore.EntityBox(ctx, rowType, name)
		if poolErr != nil {
			// Not in pool either — not resolvable without a Prowlarr call.
			writeJSON(w, apidto.AdultMonitorState{
				Resolved: false,
				Reason:   "entity not in pool — open the drill-down first to resolve its catalog identity",
			})
			return
		}

		writeJSON(w, apidto.AdultMonitorState{
			Resolved: box != "",
			Source:   box,
			Reason:   "entity found in pool but not yet in monitored list",
		})
	}
}

// putAdultMonitorHandler is PUT /api/modes/adult/discover/monitor
// It enables or disables monitoring for a performer/studio.
//
//   - monitored=true:  re-resolve entity id server-side; 409 if unresolvable.
//   - monitored=false: clear monitored flag and cancel never-dispatched monitor retries.
func putAdultMonitorHandler(monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore, grabsStore *grabs.Store, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.SetAdultMonitorRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Kind != "performer" && req.Kind != "studio" {
			http.Error(w, "kind must be 'performer' or 'studio'", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name must be non-empty", http.StatusBadRequest)
			return
		}

		ctx := r.Context()

		if !req.Monitored {
			// Un-monitor: clear flag (if row exists) and cancel never-dispatched retries.
			entity, err := monitoredStore.GetByKindName(ctx, req.Kind, req.Name)
			if err != nil && !errors.Is(err, adultnewest.ErrMonitoredNotFound) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err == nil {
				if setErr := monitoredStore.SetMonitored(ctx, req.Kind, entity.EntitySource, entity.EntityID, false); setErr != nil {
					http.Error(w, setErr.Error(), http.StatusInternalServerError)
					return
				}
				key := adultnewest.FormatMonitorKey(req.Kind, entity.EntitySource, entity.EntityID)
				cancelMonitorRetries(ctx, grabsStore, key)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Enable: resolve entity id first. Try pool EntityBox, else build a session.
		rowType := adultnewest.RowPerformer
		if req.Kind == "studio" {
			rowType = adultnewest.RowStudio
		}

		// Prefer the pool's known box — it already disambiguated the entity.
		preferredBox, _ := releaseStore.EntityBox(ctx, rowType, req.Name)

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
		if err != nil {
			log.Printf("adult monitor PUT: building session for resolution: %T", err)
			http.Error(w, "could not build an identify session to resolve the entity", http.StatusInternalServerError)
			return
		}
		if sess.Identify == nil {
			http.Error(w, "identify is not configured — add a box connection first", http.StatusConflict)
			return
		}

		resolvedBox, entityID := sess.Identify.ResolveEntityID(ctx, req.Kind, req.Name, preferredBox)
		if entityID == "" {
			http.Error(w, "entity could not be resolved to a catalog id — it may not exist in any configured box", http.StatusConflict)
			return
		}

		// Fetch entity image from pool if available.
		image := ""
		existing, err := monitoredStore.GetByKindSourceID(ctx, req.Kind, resolvedBox, entityID)
		if err == nil {
			image = existing.EntityImage
		}

		since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		upserted, err := monitoredStore.UpsertOnMonitor(ctx, req.Kind, resolvedBox, entityID, req.Name, image, since)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, apidto.AdultMonitorState{
			Resolved:   true,
			Source:     upserted.EntitySource,
			EntityID:   upserted.EntityID,
			EntityName: upserted.EntityName,
			Monitored:  upserted.Monitored,
		})
	}
}

// getMonitoredEntitiesRowHandler is
// GET /api/modes/adult/newest-rows/monitored/resolve?page=
// It returns scenes from the pool that are linked to any currently-monitored
// entity, ordered by first_seen_at DESC. Uses the same AdultNewestReleaseItem
// shape as resolveAdultNewestRowHandler.
func getMonitoredEntitiesRowHandler(monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page := 1
		if p, err := adultMonitorParseInt(r.URL.Query().Get("page")); err == nil && p > 0 {
			page = p
		}

		monitored, err := monitoredStore.ListMonitored(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now()
		items := []apidto.AdultNewestReleaseItem{}
		seen := make(map[string]bool) // deduplicate by entity ID

		for _, entity := range monitored {
			rowType := adultnewest.RowPerformer
			if entity.Kind == "studio" {
				rowType = adultnewest.RowStudio
			}
			scenes, err := releaseStore.ScenesLinkedToEntity(ctx, rowType, entity.EntityName)
			if err != nil {
				log.Printf("adult monitor row: listing scenes for %s %q: %v", entity.Kind, entity.EntityName, err)
				continue
			}
			// Simple pagination: skip first (page-1)*defaultResolvePerPage scenes across all entities.
			// For simplicity we collect all and let the caller page client-side or accept
			// the full list — the monitored list is bounded (operator-curated).
			for _, m := range scenes {
				key := m.EntitySource + "/" + m.EntityID
				if seen[key] {
					continue
				}
				seen[key] = true
				if !feedHealth.Available(m.BrowseConfirmed, m.DownloadProtocol, m.FeedID, time.Unix(m.LastConfirmedSeen, 0), now) {
					continue
				}
				items = append(items, toDTOReleaseItem(m, feedHealth, now))
			}
		}

		// Apply page offset (each page = 25 items).
		const pageSize = 25
		start := (page - 1) * pageSize
		if start >= len(items) {
			writeJSON(w, []apidto.AdultNewestReleaseItem{})
			return
		}
		end := start + pageSize
		if end > len(items) {
			end = len(items)
		}
		writeJSON(w, items[start:end])
	}
}

// adultMonitorParseInt parses an integer string for query params.
func adultMonitorParseInt(s string) (int, error) {
	return strconv.Atoi(s)
}
