package api

import (
	"context"
	"errors"
	"strconv"

	"github.com/labbersanon/sakms/internal/settings"
)

// This file holds the shared parsing/persistence logic behind every
// settings-backed "interval in whole seconds" endpoint (recheck.go's
// recheck-interval, adult_newest_scan.go's adult-newest-scan-interval,
// entity_sync.go's entity-sync-interval — three call sites now, past the
// "parallel sibling functions until a second real caller proves the
// abstraction is worth it" threshold this project's conventions call for).
//
// Each call site keeps its OWN named request/response struct types and its
// own settings-key constant — recheckIntervalResponse/Request in particular
// are drift-tested against internal/apidto's generated DTOs (see
// dto_drift_test.go), so they can't be collapsed into one shared type
// without breaking that check. Only the repeated tolerant-parse/degrade-to-
// off/negative-rejected LOGIC is shared here, not the wire types.

// loadIntervalSeconds reads settingsKey and returns the interval in whole
// seconds: defaultSeconds for a genuinely-unset key (0 for an off-by-default
// job like recheck/entity-sync, a positive value for a default-active one
// like adult-newest-scan's 24h), 0 for any stored-but-unparseable or
// non-positive value, or the stored positive value otherwise. A real store
// error (anything but settings.ErrNotFound) is returned as-is for the caller
// to surface as a 500.
func loadIntervalSeconds(ctx context.Context, settingsStore *settings.Store, settingsKey string, defaultSeconds int) (int, error) {
	v, err := settingsStore.Get(ctx, settingsKey)
	switch {
	case errors.Is(err, settings.ErrNotFound):
		return defaultSeconds, nil
	case err != nil:
		return 0, err
	default:
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			return n, nil
		}
		return 0, nil
	}
}

// storeIntervalSeconds validates and persists an interval-in-seconds value
// under settingsKey. 0 is accepted (it's how an operator turns a job off);
// a negative value is rejected — badRequest reports which case an error is,
// so the caller can pick the right HTTP status.
func storeIntervalSeconds(ctx context.Context, settingsStore *settings.Store, settingsKey string, intervalSeconds int) (badRequest bool, err error) {
	if intervalSeconds < 0 {
		return true, errors.New("intervalSeconds must be zero (off) or a positive number of seconds")
	}
	return false, settingsStore.Set(ctx, settingsKey, strconv.Itoa(intervalSeconds))
}
