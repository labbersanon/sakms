package api

import (
	"sync"

	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-09-17: process-wide native search service pointer.
// Reason: NewMux has dozens of call sites; adding another required param would
//   churn every test. main wires this once after constructing the Service.
// Troubleshooting: settings PUT applies but crawl never runs → SetNNTPNativeService
//   was not called from main.
// Review if: NewMux grows a deps struct that includes this.

var (
	nntpNativeMu  sync.RWMutex
	nntpNativeSvc *usenetsearch.Service
)

// SetNNTPNativeService registers the native NNTP discovery service for HTTP
// handlers and AutoGrabDeps helpers. Pass nil to clear (tests).
func SetNNTPNativeService(s *usenetsearch.Service) {
	nntpNativeMu.Lock()
	nntpNativeSvc = s
	nntpNativeMu.Unlock()
}

func getNNTPNativeService() *usenetsearch.Service {
	nntpNativeMu.RLock()
	defer nntpNativeMu.RUnlock()
	return nntpNativeSvc
}

// WithUsenetSearch returns deps with UsenetSearch filled from the process-wide
// service when the field is nil.
func WithUsenetSearch(deps AutoGrabDeps) AutoGrabDeps {
	if deps.UsenetSearch == nil {
		deps.UsenetSearch = getNNTPNativeService()
	}
	return deps
}
