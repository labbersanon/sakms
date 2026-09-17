package grabs

// Claude 2026-09-17: alternate-release exclusion store surface.
// Reason: when a Usenet release fails due to bad content (PAR2/unpack/no-video),
//   a different NZB should be tried. tried_release_keys persists the URL+title
//   fingerprints of already-failed attempts so the next search can exclude them.
// Troubleshooting: journal "usenet content: grab N parked for alternate release".
// Review if: MaxAlternateReleaseAttempts needs a settings knob (rejected by plan §9).
// Related files: internal/api/usenetcontent.go (routing + park call),
//   internal/api/autograbdrain.go (drainAlternateReleaseRetries),
//   internal/api/autograb_shared.go (ExcludeReleaseKeys filter).

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/dbutil"
)

// MaxAlternateReleaseAttempts is the maximum number of content-unusable parks
// allowed for one grab's current alternate-release episode (= number of "u:"
// entries in tried_release_keys). When reached, parkUsenetContentFailure falls
// through to the days ladder, which clears the keys and ends the episode.
const MaxAlternateReleaseAttempts = 3

// ReleaseKeys returns the two fingerprint keys for a release: one derived from
// the download URL and one from the normalised title. Empty inputs are skipped.
// Format: "u:<16 hex>" (URL) and/or "t:<16 hex>" (title).
//
// Keys are sha256 truncated to 8 bytes (16 hex chars). 64-bit collisions are
// negligible for a ≤6-entry list.
//
// Title normalisation: lowercase, trimmed, inner whitespace collapsed. This is
// deliberately NOT searchterm.FromName — quality/group tokens distinguish real
// releases, and stripping them would collapse distinct NZBs into one key.
func ReleaseKeys(downloadURL, title string) []string {
	var keys []string
	if downloadURL != "" {
		h := sha256.Sum256([]byte(downloadURL))
		keys = append(keys, fmt.Sprintf("u:%x", h[:8]))
	}
	if title != "" {
		norm := normaliseTitle(title)
		if norm != "" {
			h := sha256.Sum256([]byte(norm))
			keys = append(keys, fmt.Sprintf("t:%x", h[:8]))
		}
	}
	return keys
}

// normaliseTitle lowercases, trims, and collapses inner whitespace.
func normaliseTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// ParseTriedReleaseKeys splits the newline-separated tried_release_keys value.
func ParseTriedReleaseKeys(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// FormatTriedReleaseKeys joins keys back to the storage format.
func FormatTriedReleaseKeys(keys []string) string {
	return strings.Join(keys, "\n")
}

// AlternateAttempts counts the number of "u:" entries in keys, which equals
// the number of content-unusable parks in the current episode (one URL key per
// failed download).
func AlternateAttempts(keys []string) int {
	n := 0
	for _, k := range keys {
		if strings.HasPrefix(k, "u:") {
			n++
		}
	}
	return n
}

// appendKeys returns keys with newKeys appended, de-duped. Order is preserved;
// duplicates are dropped on the right side.
func appendKeys(existing, newKeys []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, k := range existing {
		seen[k] = true
	}
	out := make([]string, len(existing))
	copy(out, existing)
	for _, k := range newKeys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// ParkForAlternateRelease atomically parks a content-failed grab for a
// different-release retry. It is the exclusive writer of tried_release_keys.
//
// One atomic UPDATE:
//   - status                = 'pending_retry'
//   - retry_after           = after   (due-now: pass time.Now())
//   - retry_reason          = reason
//   - download_gid          = ''      (DueForRetry needs download_gid='')
//   - transport_retry_count = 0       (transport episode, if any, is over)
//   - tried_release_keys    = appended, de-duped
//   - retry_count           UNCHANGED (days-ladder driver, not this episode)
//   - next_search_scope     UNTOUCHED (stays '')
//
// Claude 2026-09-17: download_gid cleared — required by DueForRetry's
//   'download_gid = ' filter; leaving it would make the row invisible to the
//   very cycle it was parked for. Also prevents DueForResume treating it as
//   a transport-resume row.
// Review if: a resume_gid column separates the two concerns.
func (s *Store) ParkForAlternateRelease(ctx context.Context, id int64, after time.Time, reason string, newKeys []string) error {
	existing, err := s.triedReleaseKeys(ctx, id)
	if err != nil {
		return fmt.Errorf("reading tried_release_keys for grab %d: %w", id, err)
	}
	merged := FormatTriedReleaseKeys(appendKeys(existing, newKeys))
	res, err := s.db.ExecContext(ctx, `
		UPDATE grabs SET
			status                = 'pending_retry',
			retry_after           = ?,
			retry_reason          = ?,
			download_gid          = '',
			transport_retry_count = 0,
			tried_release_keys    = ?,
			updated_at            = sakms_now()
		WHERE id = ?
	`, FormatTime(after), reason, merged, id)
	if err != nil {
		return fmt.Errorf("parking grab %d for alternate release: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// triedReleaseKeys reads the current tried_release_keys for id.
func (s *Store) triedReleaseKeys(ctx context.Context, id int64) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT tried_release_keys FROM grabs WHERE id = ?`, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return ParseTriedReleaseKeys(raw), nil
}
