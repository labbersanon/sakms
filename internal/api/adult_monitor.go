// This file is the Adult monitored-entity API — the GET/PUT/interval routes
// that back the operator's monitor toggle on performer/studio drill-down pages.
//
// Routes:
//
//	GET  /api/modes/adult/discover/monitor?kind=&name= → AdultMonitorState
//	PUT  /api/modes/adult/discover/monitor              ← SetAdultMonitorRequest
//	GET  /api/modes/adult/newest-rows/monitored/resolve?page= → []AdultNewestReleaseItem
//	GET  /api/settings/adult-monitor-interval          → adultMonitorIntervalResponse
//	PUT  /api/settings/adult-monitor-interval          ← adultMonitorIntervalRequest
//
// Neither handler ever searches Prowlarr. Both resolve the entity's catalog
// identity with at most one box call via identify.ResolveEntityID, preferring
// the pool's known entity_source; GET reports the outcome as Resolved, PUT 409s
// when monitored=true is requested for an entity it cannot resolve.
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
// It returns the current monitor state for a performer/studio. Rows already in
// the monitored store answer from there; anything else costs at most one
// exact-name catalog resolve (same budget shape as adultDescriptionHandler) so
// Resolved reflects a real opaque id.
//
// kind must be "performer" or "studio"; name must be non-empty. Any other
// combination returns 400. Unresolvable / unconfigured installs still return
// 200 with Resolved=false (no ErrorBoundary in the SPA).
func getAdultMonitorStateHandler(monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
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

		// Claude 2026-08-24: resolve opaque catalog id on drill-open.
		// Reason: pool entity_id is the display name; Resolved must mean a real id.
		// Troubleshooting: toggle enabled then PUT 409 — GET was treating pool box as resolved.
		// Review if: monitored store always pre-seeds identity before the toggle renders.
		preferredBox, _ := releaseStore.EntityBox(ctx, adultRowType(kind), name)

		sess, buildErr := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
		if buildErr != nil || sess.Identify == nil {
			writeJSON(w, apidto.AdultMonitorState{
				Resolved:   false,
				EntityName: name,
				Reason:     "Can't monitor — no catalog box is configured to resolve this entry.",
			})
			return
		}
		resolvedBox, entityID := sess.Identify.ResolveEntityID(ctx, kind, name, preferredBox)
		if entityID == "" {
			writeJSON(w, apidto.AdultMonitorState{
				Resolved:   false,
				Source:     preferredBox,
				EntityName: name,
				Reason:     "Can't monitor — this performer/studio isn't matched to a catalog entry yet.",
			})
			return
		}
		writeJSON(w, apidto.AdultMonitorState{
			Resolved:   true,
			Source:     resolvedBox,
			EntityID:   entityID,
			EntityName: name,
			Monitored:  false,
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

		// Enable: resolve the entity id first, preferring the pool's known box
		// because it already disambiguated the entity.
		preferredBox, _ := releaseStore.EntityBox(ctx, adultRowType(req.Kind), req.Name)

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

		// Carry the image forward from a previous monitor of the same entity.
		image := ""
		if existing, err := monitoredStore.GetByKindSourceID(ctx, req.Kind, resolvedBox, entityID); err == nil {
			image = existing.EntityImage
		}

		since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := monitoredStore.UpsertOnMonitor(ctx, req.Kind, resolvedBox, entityID, req.Name, image, since); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
		if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
			page = p
		}

		monitored, err := monitoredStore.ListMonitored(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now()
		items := []apidto.AdultNewestReleaseItem{}
		seen := make(map[string]bool)

		// Every monitored entity's scenes are collected before paging: the
		// monitored list is operator-curated and therefore bounded, and a scene
		// can be linked to more than one entity, so dedup has to see all of them
		// before an offset means anything.
		for _, entity := range monitored {
			scenes, err := releaseStore.ScenesLinkedToEntity(ctx, adultRowType(entity.Kind), entity.EntityName)
			if err != nil {
				log.Printf("adult monitor row: listing scenes for %s %q: %v", entity.Kind, entity.EntityName, err)
				continue
			}
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

		const pageSize = 25
		start := (page - 1) * pageSize
		if start >= len(items) {
			writeJSON(w, []apidto.AdultNewestReleaseItem{})
			return
		}
		writeJSON(w, items[start:min(start+pageSize, len(items))])
	}
}

// adultRowType maps a monitor "kind" ("performer"/"studio") to the pool row type
// used to look the entity up. Anything other than "studio" is a performer, which
// is safe because every caller validates kind first.
func adultRowType(kind string) adultnewest.RowType {
	if kind == "studio" {
		return adultnewest.RowStudio
	}
	return adultnewest.RowPerformer
}
