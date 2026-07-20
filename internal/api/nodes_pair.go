package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/nodes"
)

const sseKeepaliveInterval = 30 * time.Second

// PairStreamHandler handles GET /api/nodes/pair (no auth required).
// It opens a pre-auth SSE stream: emits "pending" immediately (pairing code +
// device name), then blocks until the operator approves (→ emits "config" and
// closes), the TTL expires, the operator rejects, or the client disconnects.
func PairStreamHandler(pairingReg *nodes.PairingRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		name := r.URL.Query().Get("name")
		if name == "" {
			name = "unknown"
		}

		id, code, configCh, done, registered := pairingReg.Register(name)
		if !registered {
			http.Error(w, "too many pending pairings", http.StatusServiceUnavailable)
			return
		}
		defer pairingReg.Disconnect(id)

		log.Printf("nodes/pair: %s registered (code=%s)", name, code)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		pendingData, err := json.Marshal(struct {
			PairingCode string `json:"pairingCode"`
			DeviceName  string `json:"deviceName"`
		}{PairingCode: code, DeviceName: name})
		if err != nil {
			log.Printf("nodes/pair: marshal pending: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", nodes.EventPending, pendingData)
		flusher.Flush()

		ticker := time.NewTicker(sseKeepaliveInterval)
		defer ticker.Stop()
		ctx := r.Context()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				// TTL expired or operator rejected — close stream without config.
				return
			case cfg := <-configCh:
				cfgData, err := json.Marshal(cfg)
				if err != nil {
					log.Printf("nodes/pair: marshal config: %v", err)
					return
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", nodes.EventConfig, cfgData)
				flusher.Flush()
				log.Printf("nodes/pair: %s approved (id=%s)", name, id)
				return
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}
