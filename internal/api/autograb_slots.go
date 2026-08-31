package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/settings"
)

// Auto-grab slot budgets, edited in Settings → Download → Usenet / Torrent.
// They replace the hard-coded 20-per-cycle / 5-per-series air-date caps, which
// let a classic backlog starve new shows.
//
// Torrent per-cycle 0 (the default) means torrent auto-grabs share the Usenet
// cycle budget — the historical behaviour. A positive value adds dedicated
// torrent slots on top of it.

const (
	autoGrabSlotsProtocolUsenet  = "usenet"
	autoGrabSlotsProtocolTorrent = "torrent"

	usenetAutoGrabSlotsPerCycleKey   = "usenet_autograb_slots_per_cycle"
	usenetAutoGrabSlotsPerSeriesKey  = "usenet_autograb_slots_per_series"
	torrentAutoGrabSlotsPerCycleKey  = "torrent_autograb_slots_per_cycle"
	torrentAutoGrabSlotsPerSeriesKey = "torrent_autograb_slots_per_series"

	defaultAutoGrabSlotsPerCycle  = 20
	defaultAutoGrabSlotsPerSeries = 5
	autoGrabSlotsMax              = 100
)

var errUnknownAutoGrabSlotsProtocol = errors.New("autograb slots: protocol must be usenet or torrent")

type autoGrabSlots struct {
	PerCycle  int `json:"perCycle"`
	PerSeries int `json:"perSeries"`
}

// autoGrabSlotsSpec is one protocol's storage keys, its unset per-cycle
// default, and the smallest per-cycle value a PUT accepts.
type autoGrabSlotsSpec struct {
	perCycleKey  string
	perSeriesKey string
	defaultCycle int
	minCycle     int
}

var autoGrabSlotsSpecs = map[string]autoGrabSlotsSpec{
	autoGrabSlotsProtocolUsenet: {
		perCycleKey:  usenetAutoGrabSlotsPerCycleKey,
		perSeriesKey: usenetAutoGrabSlotsPerSeriesKey,
		defaultCycle: defaultAutoGrabSlotsPerCycle,
		minCycle:     1,
	},
	autoGrabSlotsProtocolTorrent: {
		perCycleKey:  torrentAutoGrabSlotsPerCycleKey,
		perSeriesKey: torrentAutoGrabSlotsPerSeriesKey,
		defaultCycle: 0,
		minCycle:     0,
	},
}

func loadAutoGrabSlots(ctx context.Context, store *settings.Store, protocol string) (autoGrabSlots, error) {
	spec, ok := autoGrabSlotsSpecs[protocol]
	if !ok {
		return autoGrabSlots{}, errUnknownAutoGrabSlotsProtocol
	}
	cycle, err := loadAutoGrabSlotValue(ctx, store, spec.perCycleKey, spec.defaultCycle)
	if err != nil {
		return autoGrabSlots{}, err
	}
	series, err := loadAutoGrabSlotValue(ctx, store, spec.perSeriesKey, defaultAutoGrabSlotsPerSeries)
	if err != nil {
		return autoGrabSlots{}, err
	}
	return autoGrabSlots{PerCycle: cycle, PerSeries: series}, nil
}

// loadAutoGrabSlotValue reads one slot count, treating an unparseable or
// out-of-range stored value as unset so a hand-edited row cannot wedge dispatch.
func loadAutoGrabSlotValue(ctx context.Context, store *settings.Store, key string, def int) (int, error) {
	v, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, settings.ErrNotFound) {
			return def, nil
		}
		return 0, err
	}
	n, convErr := strconv.Atoi(v)
	if convErr != nil || n < 0 || n > autoGrabSlotsMax {
		return def, nil
	}
	return n, nil
}

// autoGrabSlotsOrDefaults is the dispatch-path read: a store failure logs and
// falls back to the protocol's defaults rather than stalling the cycle.
func autoGrabSlotsOrDefaults(ctx context.Context, store *settings.Store, protocol string) autoGrabSlots {
	slots, err := loadAutoGrabSlots(ctx, store, protocol)
	if err == nil {
		return slots
	}
	log.Printf("auto-grab slots: reading %s caps: %v — using defaults", protocol, err)
	return autoGrabSlots{
		PerCycle:  autoGrabSlotsSpecs[protocol].defaultCycle,
		PerSeries: defaultAutoGrabSlotsPerSeries,
	}
}

// airDateDispatchCaps is the air-date cycle / series-backfill budget: Usenet's
// slots plus any dedicated Torrent slots. Per-series is Usenet's cap, plus
// Torrent's only when Torrent has a cycle budget of its own.
func airDateDispatchCaps(ctx context.Context, store *settings.Store) (limit, perSeries int) {
	u := autoGrabSlotsOrDefaults(ctx, store, autoGrabSlotsProtocolUsenet)
	t := autoGrabSlotsOrDefaults(ctx, store, autoGrabSlotsProtocolTorrent)
	perSeries = u.PerSeries
	if t.PerCycle > 0 {
		perSeries += t.PerSeries
	}
	return u.PerCycle + t.PerCycle, perSeries
}

// loadUsenetCycleSlots is the per-cycle budget for the passes with no torrent
// side of their own (pre-release, adult monitor).
func loadUsenetCycleSlots(ctx context.Context, store *settings.Store) int {
	return autoGrabSlotsOrDefaults(ctx, store, autoGrabSlotsProtocolUsenet).PerCycle
}

func getAutoGrabSlotsHandler(settingsStore *settings.Store, protocol string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slots, err := loadAutoGrabSlots(r.Context(), settingsStore, protocol)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, slots)
	}
}

// putAutoGrabSlotsHandler validates against the protocol's own minimum: Usenet
// needs at least 1 slot per cycle, Torrent accepts 0 (share Usenet's budget).
func putAutoGrabSlotsHandler(settingsStore *settings.Store, protocol string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		spec, ok := autoGrabSlotsSpecs[protocol]
		if !ok {
			http.Error(w, errUnknownAutoGrabSlotsProtocol.Error(), http.StatusBadRequest)
			return
		}
		var req autoGrabSlots
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.PerCycle < spec.minCycle || req.PerCycle > autoGrabSlotsMax {
			http.Error(w, "perCycle is out of range", http.StatusBadRequest)
			return
		}
		if req.PerSeries < 0 || req.PerSeries > autoGrabSlotsMax {
			http.Error(w, "perSeries is out of range", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.Set(ctx, spec.perCycleKey, strconv.Itoa(req.PerCycle)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, spec.perSeriesKey, strconv.Itoa(req.PerSeries)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
