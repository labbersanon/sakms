// Package proposals persists the review queue every workflow (Rename first,
// Purge/Dedup/Tag later) stages into: a Scan populates rows here, a row is a
// decision waiting to be made, and Apply/Dismiss is what actually resolves
// one. Nothing in this package makes an outbound call to any *arr app —
// that's internal/rename (and its future siblings) — this package only
// knows how to store and retrieve the queue itself.
package proposals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/dbutil"
	"github.com/labbersanon/sakms/internal/mode"
)

// Workflow identifies which review workflow a proposal belongs to.
type Workflow string

const (
	Rename Workflow = "rename"
	Purge  Workflow = "purge"
	Dedup  Workflow = "dedup"
)

// Status is where a proposal currently sits in its review lifecycle.
type Status string

const (
	// Pending is a fully-identified proposal awaiting a human Apply/Dismiss.
	Pending Status = "pending"
	// Unmatched is a proposal Scan couldn't confidently resolve on its own
	// (no lookup match, a lookup error, or a suspected duplicate) — surfaced
	// in the queue for manual attention rather than silently dropped.
	Unmatched Status = "unmatched"
	Applied   Status = "applied"
	Dismissed Status = "dismissed"
)

// ErrNotFound is returned by Get when no proposal exists with the given ID.
var ErrNotFound = errors.New("proposals: no proposal with that id")

// Candidate is one file in a Dedup duplicate group. Unused by Rename/Purge
// proposals (an empty slice).
type Candidate struct {
	// Label identifies this candidate for display — "tracked" for the
	// currently-tracked copy, or the source unmapped-folder name otherwise.
	Label      string `json:"label"`
	Path       string `json:"path"`
	TrackedID  int    `json:"trackedId,omitempty"`
	Resolution int    `json:"resolution"`
	Codec      string `json:"codec"`
	BitRate    int64  `json:"bitRate"`
	// PHash is this candidate's SAK-computed perceptual hash (Movies Dedup
	// only), scheme-tagged (see internal/phash). Surfaced for display/audit —
	// the same-TMDB group was already refined by phash similarity at Scan time,
	// so nothing consumes this on Apply. Empty for non-Movies-library Dedup
	// candidates (Adult/Series/Servarr paths don't compute it). Zero migration:
	// Candidates persist as the candidates_json blob, so a new field just
	// serializes. Distinct from Proposal.PHash, which is Adult's Stash-READ
	// hash, never SAK-computed.
	PHash string `json:"phash,omitempty"`
	// Winner is precomputed at Scan time via place.QualityKey — Apply uses
	// it as the default "auto-resolve by quality" choice when the caller
	// doesn't explicitly pick a candidate to keep.
	Winner bool `json:"winner"`
}

// Proposal is one staged review-queue row. TVDBID/TMDBID/QualityProfileID
// and Title are only meaningful once Status is Pending or Applied; Reason
// explains why Status is Unmatched. Candidates is only populated for Dedup.
type Proposal struct {
	ID             int64     `json:"id"`
	Mode           mode.Mode `json:"mode"`
	Workflow       Workflow  `json:"workflow"`
	Status         Status    `json:"status"`
	SourceName     string    `json:"sourceName"`
	SourcePath     string    `json:"sourcePath"`
	RootFolderPath string    `json:"rootFolderPath"`
	Title          string    `json:"title,omitempty"`
	TVDBID         int       `json:"tvdbId,omitempty"`
	TMDBID         int       `json:"tmdbId,omitempty"`
	// SeasonNumber/EpisodeNumber are Series-only — a season-pack orphan
	// produces one proposal per episode file found inside it, never a
	// bulk proposal, so each needs to record which episode it is.
	//
	// CORRECTED 2026-08-07 — the superseded claim, quoted so a future session
	// can find it: "a Proposal's SeasonNumber always comes from a successful
	// library.ParseEpisodeFilename call, so 0 unambiguously means the filename
	// itself encoded season 0 (Specials)." There is now a SECOND source: the
	// operator, via Store.RepickEpisode (the manual slot-assignment path for a
	// file with no recoverable episode signal). So a Proposal's SeasonNumber
	// comes from EITHER a successful ParseEpisodeFilename call OR a direct
	// operator assignment.
	//
	// The CONCLUSION the superseded sentence drew is UNCHANGED and still holds:
	// 0 still unambiguously means Specials, and no companion "was this
	// specified" flag is needed (unlike grabs.Grab's fields of the same name).
	// That is not luck — internal/api's repickProposalHandler enforces it,
	// rejecting an operator-supplied episodeNumber < 1 and accepting
	// seasonNumber == 0 deliberately, and the DTO carries *int precisely so a
	// literal 0 cannot be dropped in transit. What changed is the PROVENANCE,
	// not the semantics.
	SeasonNumber  int `json:"seasonNumber,omitempty"`
	EpisodeNumber int `json:"episodeNumber,omitempty"`
	// ExtraEpisodeNumbers holds any ADDITIONAL episode numbers bundled into
	// the same file as EpisodeNumber (logical episode-splitting — see
	// library.ParseEpisodeNumbers) — e.g. a "S01E01-E02" file produces
	// EpisodeNumber=1, ExtraEpisodeNumbers=[2]. Empty/nil for the ordinary
	// single-episode case, the overwhelming majority. Apply relocates the
	// file exactly once, then upserts one Episode row per number (the
	// primary plus each of these) all pointing at that same resulting path.
	ExtraEpisodeNumbers []int `json:"extraEpisodeNumbers,omitempty"`
	// Year is the release year resolved from TMDB at Scan time (Rename
	// only) — carried through to Apply so RelocateMovie/RelocateEpisode can
	// include it in the Jellyfin/Emby-style destination name, and so the
	// library row it produces finally populates library.Item.Year/
	// library.Series.Year instead of leaving them at their Go zero value.
	Year             int         `json:"year,omitempty"`
	QualityProfileID int         `json:"qualityProfileId,omitempty"`
	Reason           string      `json:"reason,omitempty"`
	TrackedID        int         `json:"trackedId,omitempty"`
	ForeignID        string      `json:"foreignId,omitempty"`
	ItemType         string      `json:"itemType,omitempty"`
	Candidates       []Candidate `json:"candidates,omitempty"`
	// PHashSimilarity is the minimum pairwise phash similarity across the group
	// [0.0–1.0], populated only by phash-primary scans (ScanLibraryPHash /
	// ScanLibrarySeriesPHash). Zero means the proposal was produced by the
	// legacy TMDB-keyed path and no similarity score was computed.
	//
	// Claude 2026-08-04: backed by the phash_similarity column (migration
	// 0060) as of this comment. Reason: from 50dd970 (2026-07-18) until this
	// migration, this field had no column to persist to — ReplacePending's
	// INSERT silently dropped it, so List/Get always read back 0 regardless
	// of what Scan computed, defeating the Dedup card's similarity badge.
	// See .omc/plans/autopilot-impl-phash-grouping.md §2 for the traced break.
	PHashSimilarity float64 `json:"pHashSimilarity,omitempty"`
	// Studio and Date are captured from Adult identification alongside Title,
	// even on an Unmatched (web-identified-only) proposal — SubmitDraft needs
	// them to give the scene back to the community databases.
	Studio string `json:"studio,omitempty"`
	Date   string `json:"date,omitempty"`
	// DraftID and DraftSubmittedAt record a successful SubmitDraft call — set
	// once, never cleared, so a proposal is never submitted as a draft twice.
	DraftID          string `json:"draftId,omitempty"`
	DraftSubmittedAt string `json:"draftSubmittedAt,omitempty"`
	// PHash and DurationSeconds are read from the LOCAL Stash instance's own
	// already-computed metadata at Scan time (Adult only — never computed by
	// SAK itself, see mode.Session.Stash's doc comment), regardless of
	// whether the fingerprint cascade or the AI/text pipeline actually
	// resolved the proposal. Both empty/zero when no Stash connection was
	// configured at Scan time, or Stash simply had no phash yet for this file.
	PHash           string `json:"phash,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	// GiveBackBox/GiveBackSceneID identify which community stash-box (if any)
	// this proposal matched an EXISTING scene in — captured directly from the
	// identify.MatchResult's own Box/SceneID at Scan time, NOT derived from
	// ForeignID: WhisparrForeignID() returns the same raw UUID string for
	// both a stashdb and a fansdb match, so ForeignID alone can't
	// disambiguate which box a fingerprint should be given back to.
	GiveBackBox     string `json:"giveBackBox,omitempty"`
	GiveBackSceneID string `json:"giveBackSceneId,omitempty"`
	// FingerprintSubmittedAt records a successful fingerprint give-back
	// (either automatically at Apply time or via a later manual retry) — set
	// once, never cleared, same "never submit twice" guard as DraftID/
	// DraftSubmittedAt.
	FingerprintSubmittedAt string `json:"fingerprintSubmittedAt,omitempty"`
	// Genres and Cast are populated at Scan time from TMDB for Movies/Series
	// Rename proposals — enrichment only; empty for Dedup/Purge/Adult.
	// Soft-fail: a failed credits call leaves them nil without blocking.
	Genres    []string `json:"genres,omitempty"`
	Cast      []string `json:"cast,omitempty"`
	CreatedAt string   `json:"createdAt"`
	AppliedAt string   `json:"appliedAt,omitempty"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// ReplacePending replaces the live (Pending/Unmatched) proposal queue for
// (m, wf) with fresh. Applied and Dismissed rows are untouched — they are
// history, not part of the live queue, so a rescan never erases a decision
// already made.
//
// Apply-gate: if an apply is currently in flight for (m, wf), the replace is
// deferred — it returns nil, ErrReplaceDeferred without mutating the queue.
// The existing IDs remain valid for the apply loop. A catch-up rescan fires
// automatically via RegisterApplyIdle hooks when the apply completes.
//
// Upsert semantics: proposals with a non-empty source_path are matched to
// existing live rows by that path. Matching rows are updated in place —
// preserving their ID and created_at — so apply-batch IDs remain stable
// across a concurrent rescan. Proposals with an empty source_path (rare) are
// always inserted as new rows. Live rows whose source_path is not in the
// fresh set are deleted. Returns fresh with IDs and CreatedAt populated.
//
// Claude 2026-08-06: replaced delete-all+insert with apply-gate check + upsert.
// Reason: watchfolder scan during apply-batch caused "no proposal with that id"
//   (ErrNotFound) because ReplacePending's DELETE removed rows apply-batch was
//   still looking up. Upsert preserves IDs; gate defers replace while apply runs.
// Troubleshooting: if ids appear stale, confirm BeginApply/EndApply are paired
//   in every apply handler and that ErrReplaceDeferred is not silently swallowed.
// Review if: applies become fully idempotent or the gate is superseded.
func (s *Store) ReplacePending(ctx context.Context, m mode.Mode, wf Workflow, fresh []Proposal) ([]Proposal, error) {
	// Apply-gate: defer if an apply is running for this (mode, workflow).
	if DefaultGate.ApplyInFlight(m, wf) {
		DefaultGate.markDeferred(m, wf)
		return nil, ErrReplaceDeferred
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// Load existing live rows for upsert matching keyed by source_path.
	// Empty-path rows are excluded from the map — they always re-insert.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, source_path, created_at FROM proposals WHERE mode = ? AND workflow = ? AND status IN (?, ?)`,
		string(m), string(wf), string(Pending), string(Unmatched))
	if err != nil {
		return nil, fmt.Errorf("loading existing live rows: %w", err)
	}
	type existingRow struct {
		id        int64
		createdAt string
	}
	existing := make(map[string]existingRow)
	for rows.Next() {
		var id int64
		var sourcePath, createdAt string
		if err := rows.Scan(&id, &sourcePath, &createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning existing live row: %w", err)
		}
		if sourcePath != "" {
			existing[sourcePath] = existingRow{id: id, createdAt: createdAt}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading existing live rows: %w", err)
	}

	out := make([]Proposal, len(fresh))
	// updatedIDs collects every ID that survives this replace (updated or
	// freshly inserted). Used to delete orphaned live rows at the end.
	updatedIDs := make([]int64, 0, len(fresh))

	for i, p := range fresh {
		p.Mode, p.Workflow = m, wf
		candidatesJSON, err := json.Marshal(p.Candidates)
		if err != nil {
			return nil, fmt.Errorf("encoding candidates for %q: %w", p.SourceName, err)
		}
		extraEpisodesJSON, err := marshalExtraEpisodes(p.ExtraEpisodeNumbers)
		if err != nil {
			return nil, fmt.Errorf("encoding extra episode numbers for %q: %w", p.SourceName, err)
		}
		genresJSON, err := marshalStringSlice(p.Genres)
		if err != nil {
			return nil, fmt.Errorf("encoding genres for %q: %w", p.SourceName, err)
		}
		castJSON, err := marshalStringSlice(p.Cast)
		if err != nil {
			return nil, fmt.Errorf("encoding cast for %q: %w", p.SourceName, err)
		}

		if p.SourcePath != "" {
			if ex, ok := existing[p.SourcePath]; ok {
				// UPDATE existing row in place — preserve id and created_at so
				// any in-flight apply-batch lookup by that ID continues to work.
				if _, err := tx.ExecContext(ctx, `
					UPDATE proposals SET
						status=?, source_name=?, root_folder_path=?,
						title=?, tvdb_id=?, tmdb_id=?, season_number=?, episode_number=?,
						year=?, quality_profile_id=?, reason=?, tracked_id=?,
						foreign_id=?, item_type=?, candidates_json=?, studio=?,
						scene_date=?, phash=?, duration_seconds=?, give_back_box=?,
						give_back_scene_id=?, extra_episode_numbers=?, genres=?,
						"cast"=?, phash_similarity=?
					WHERE id=?
				`, string(p.Status), p.SourceName, p.RootFolderPath,
					p.Title, p.TVDBID, p.TMDBID, p.SeasonNumber, p.EpisodeNumber,
					p.Year, p.QualityProfileID, p.Reason, p.TrackedID,
					p.ForeignID, p.ItemType, string(candidatesJSON), p.Studio,
					p.Date, p.PHash, p.DurationSeconds, p.GiveBackBox,
					p.GiveBackSceneID, extraEpisodesJSON, genresJSON,
					castJSON, p.PHashSimilarity, ex.id); err != nil {
					return nil, fmt.Errorf("updating proposal for %q: %w", p.SourceName, err)
				}
				p.ID = ex.id
				p.CreatedAt = ex.createdAt
				updatedIDs = append(updatedIDs, ex.id)
				out[i] = p
				continue
			}
		}

		// INSERT: new source_path, empty source_path, or no live match.
		// Claude 2026-08-04: added phash_similarity (migration 0060). Reason: it
		// was computed by ScanLibraryPHash/ScanLibrarySeriesPHash but had no
		// column to land in, silently dying here before it ever reached List/Get
		// — see the plan's §2 trace. Appended at the end of the column list
		// rather than inserted alongside PHash/DurationSeconds so this diff
		// can't accidentally shift any of the 26 existing positional slots.
		row := tx.QueryRowContext(ctx, `
			INSERT INTO proposals (
				mode, workflow, status, source_name, source_path, root_folder_path,
				title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
				foreign_id, item_type, candidates_json, studio, scene_date,
				phash, duration_seconds, give_back_box, give_back_scene_id, extra_episode_numbers,
				genres, "cast", phash_similarity
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id, created_at
		`, string(p.Mode), string(p.Workflow), string(p.Status), p.SourceName, p.SourcePath, p.RootFolderPath,
			p.Title, p.TVDBID, p.TMDBID, p.SeasonNumber, p.EpisodeNumber, p.Year, p.QualityProfileID, p.Reason, p.TrackedID,
			p.ForeignID, p.ItemType, string(candidatesJSON), p.Studio, p.Date,
			p.PHash, p.DurationSeconds, p.GiveBackBox, p.GiveBackSceneID, extraEpisodesJSON,
			genresJSON, castJSON, p.PHashSimilarity)
		if err := row.Scan(&p.ID, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("inserting proposal for %q: %w", p.SourceName, err)
		}
		updatedIDs = append(updatedIDs, p.ID)
		out[i] = p
	}

	// Delete live rows not covered by this scan (orphans whose source
	// disappeared, and empty-path rows being replaced by fresh inserts above).
	if len(updatedIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(updatedIDs)), ",")
		args := make([]any, 0, 4+len(updatedIDs))
		args = append(args, string(m), string(wf), string(Pending), string(Unmatched))
		for _, id := range updatedIDs {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM proposals WHERE mode = ? AND workflow = ? AND status IN (?, ?) AND id NOT IN (%s)`, placeholders),
			args...); err != nil {
			return nil, fmt.Errorf("deleting orphaned proposals: %w", err)
		}
	} else {
		// No survivors — clear the entire live queue for (m, wf).
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM proposals WHERE mode = ? AND workflow = ? AND status IN (?, ?)`,
			string(m), string(wf), string(Pending), string(Unmatched)); err != nil {
			return nil, fmt.Errorf("clearing previous queue: %w", err)
		}
	}

	// Re-check the apply-gate before commit: an apply may have begun after our
	// initial check (TOCTOU). Abort so we don't delete rows an in-flight apply
	// still needs for MarkApplied after a mid-batch file move.
	if DefaultGate.ApplyInFlight(m, wf) {
		DefaultGate.markDeferred(m, wf)
		return nil, ErrReplaceDeferred // tx rolls back via defer
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing queue replacement: %w", err)
	}
	return out, nil
}

// List returns every proposal for (m, wf), most recently created first.
// Unbounded — used by internal scan/replace paths. HTTP list handlers use
// ListPage instead.
func (s *Store) List(ctx context.Context, m mode.Mode, wf Workflow) ([]Proposal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, workflow, status, source_name, source_path, root_folder_path,
		       title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
		       foreign_id, item_type, candidates_json, studio, scene_date,
		       draft_id, COALESCE(draft_submitted_at, ''),
		       phash, duration_seconds, give_back_box, give_back_scene_id, COALESCE(fingerprint_submitted_at, ''),
		       created_at, COALESCE(applied_at, ''), COALESCE(extra_episode_numbers, ''),
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'), phash_similarity
		FROM proposals WHERE mode = ? AND workflow = ? ORDER BY id DESC
	`, string(m), string(wf))
	if err != nil {
		return nil, fmt.Errorf("listing proposals: %w", err)
	}
	defer rows.Close()

	// []Proposal{}, not var out []Proposal — an empty queue should serialize
	// as [] over the API, not null, so a frontend never needs a special case
	// for "nothing yet" versus "some proposals" (see allowlist.Store.List's
	// identical convention).
	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Claude 2026-08-05: page-size bounds for Organize queue pagination.
// Reason: deep-interview-organize-pagination-log — Dedup/Purge apply is
//   page-scoped; max page size is also their apply-batch bound.
// Troubleshooting: clients sending limit>Max get clamped, not 400.
// Review if: page-size options in the UI change.
const (
	MaxProposalPageSize     = 200
	DefaultProposalPageSize = 50
)

// Claude 2026-08-05: ListView filters Organize queues (deep-interview-organize-hide-applied).
// Reason: live queue excludes applied/dismissed; history is swap-only of those.
// Troubleshooting: unknown view falls back to live — never return a mixed page by accident.
// Review if: a third "all" view is added for admin/debug.
type ListView string

const (
	ListViewLive    ListView = "live"
	ListViewHistory ListView = "history"
)

// ParseListView maps ?view= query values. Empty/unknown → live.
func ParseListView(raw string) ListView {
	if strings.EqualFold(strings.TrimSpace(raw), string(ListViewHistory)) {
		return ListViewHistory
	}
	return ListViewLive
}

func listViewStatuses(view ListView) (a, b Status) {
	if view == ListViewHistory {
		return Applied, Dismissed
	}
	return Pending, Unmatched
}

// ListPage returns one page of proposals for (m, wf) plus the total matching
// count, filtered by view (live = pending+unmatched; history = applied+dismissed).
// limit<=0 uses DefaultProposalPageSize; limit is clamped to MaxProposalPageSize.
// offset<0 is treated as 0. Empty pages still return items=[] (not null).
func (s *Store) ListPage(ctx context.Context, m mode.Mode, wf Workflow, limit, offset int, view ListView) ([]Proposal, int, error) {
	if limit <= 0 {
		limit = DefaultProposalPageSize
	}
	if limit > MaxProposalPageSize {
		limit = MaxProposalPageSize
	}
	if offset < 0 {
		offset = 0
	}
	stA, stB := listViewStatuses(view)

	var total int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM proposals
		WHERE mode = ? AND workflow = ? AND status IN (?, ?)
	`, string(m), string(wf), string(stA), string(stB)).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting proposals: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, workflow, status, source_name, source_path, root_folder_path,
		       title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
		       foreign_id, item_type, candidates_json, studio, scene_date,
		       draft_id, COALESCE(draft_submitted_at, ''),
		       phash, duration_seconds, give_back_box, give_back_scene_id, COALESCE(fingerprint_submitted_at, ''),
		       created_at, COALESCE(applied_at, ''), COALESCE(extra_episode_numbers, ''),
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'), phash_similarity
		FROM proposals
		WHERE mode = ? AND workflow = ? AND status IN (?, ?)
		ORDER BY id DESC
		LIMIT ? OFFSET ?
	`, string(m), string(wf), string(stA), string(stB), limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing proposals page: %w", err)
	}
	defer rows.Close()

	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// ListPendingIDs returns every Pending proposal id for (m, wf), newest first.
// Used by Rename "Select all matching" (cross-page selection).
func (s *Store) ListPendingIDs(ctx context.Context, m mode.Mode, wf Workflow) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM proposals WHERE mode = ? AND workflow = ? AND status = ?
		ORDER BY id DESC
	`, string(m), string(wf), string(Pending))
	if err != nil {
		return nil, fmt.Errorf("listing pending proposal ids: %w", err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Get returns a single proposal by ID.
func (s *Store) Get(ctx context.Context, id int64) (*Proposal, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, workflow, status, source_name, source_path, root_folder_path,
		       title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
		       foreign_id, item_type, candidates_json, studio, scene_date,
		       draft_id, COALESCE(draft_submitted_at, ''),
		       phash, duration_seconds, give_back_box, give_back_scene_id, COALESCE(fingerprint_submitted_at, ''),
		       created_at, COALESCE(applied_at, ''), COALESCE(extra_episode_numbers, ''),
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'), phash_similarity
		FROM proposals WHERE id = ?
	`, id)
	p, err := scanProposal(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading proposal %d: %w", id, err)
	}
	return &p, nil
}

// GetLiveBySourcePath returns the live (Pending/Unmatched) proposal for
// (m, wf, sourcePath), if any. Used by apply-batch's id-miss safety net to
// re-resolve after ReplacePending rotates ids.
//
// Claude 2026-08-06: path-based live lookup for apply safety net
// Reason: when Get(id) misses after a rescan, the same file still has a live
//   row under a new id keyed by source_path.
// Troubleshooting: apply-batch "no proposal with that id" despite file still pending.
// Review if: ApplyBatchItem always carries sourcePath and ids are fully stable.
func (s *Store) GetLiveBySourcePath(ctx context.Context, m mode.Mode, wf Workflow, sourcePath string) (*Proposal, error) {
	if sourcePath == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, workflow, status, source_name, source_path, root_folder_path,
		       title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
		       foreign_id, item_type, candidates_json, studio, scene_date,
		       draft_id, COALESCE(draft_submitted_at, ''),
		       phash, duration_seconds, give_back_box, give_back_scene_id, COALESCE(fingerprint_submitted_at, ''),
		       created_at, COALESCE(applied_at, ''), COALESCE(extra_episode_numbers, ''),
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'), phash_similarity
		FROM proposals
		WHERE mode = ? AND workflow = ? AND source_path = ? AND status IN (?, ?)
		LIMIT 1
	`, string(m), string(wf), sourcePath, string(Pending), string(Unmatched))
	p, err := scanProposal(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading live proposal for path %q: %w", sourcePath, err)
	}
	return &p, nil
}

// MarkApplied records that proposal id was successfully acted on, producing
// trackedID in the target *arr app.
func (s *Store) MarkApplied(ctx context.Context, id int64, trackedID int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET status = ?, tracked_id = ?, applied_at = sakms_now()
		WHERE id = ?
	`, string(Applied), trackedID, id)
	if err != nil {
		return fmt.Errorf("marking proposal %d applied: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// Dismiss marks proposal id as reviewed-and-rejected — it stays in history
// but drops out of the live queue, and won't reappear unless a future Scan
// re-discovers the same source item.
func (s *Store) Dismiss(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE proposals SET status = ? WHERE id = ?`, string(Dismissed), id)
	if err != nil {
		return fmt.Errorf("dismissing proposal %d: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// Repick overwrites proposal id's title/tmdbId/year with an operator-chosen
// alternative and promotes it to Pending — Rename's manual-override
// workflow (see internal/api's repickProposalHandler, which enforces the
// eligible-status precondition — Pending or Unmatched only — before ever
// calling this, so this method itself doesn't need a status guard). Clears
// Reason too: whatever explained the old (wrong, or too-weak-to-auto-accept)
// match no longer describes anything true about the row.
func (s *Store) Repick(ctx context.Context, id int64, title string, tmdbID, year int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET title = ?, tmdb_id = ?, year = ?, status = ?, reason = ''
		WHERE id = ?
	`, title, tmdbID, year, string(Pending), id)
	if err != nil {
		return fmt.Errorf("re-picking proposal %d: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// RepickEpisode is Repick's Series-only sibling: it does everything Repick does
// (overwrite title/tmdb_id/year, promote to Pending, clear reason) AND assigns
// season_number/episode_number directly — the manual-assignment path for a file
// with no recoverable episode signal at all (an opaque hash basename, a raw DVD
// VIDEO_TS authoring name, an ambiguous multi-slot title match).
//
// A separate method rather than optional parameters on Repick, per this repo's
// no-premature-abstraction convention: it keeps the Movies re-pick path
// PROVABLY untouched by this feature rather than merely reviewed as untouched.
// The cost is one duplicated UPDATE statement, priced and accepted.
//
// Callers pass a validated pair — internal/api's repickProposalHandler enforces
// both-or-neither, mode == Series, season >= 0 and episode >= 1 before this is
// reached, exactly as it already enforces the eligible-status precondition for
// Repick.
//
// extra_episode_numbers is deliberately CLEARED: a manual single-slot
// assignment supersedes whatever multi-episode bundle a prior parse claimed,
// and leaving a stale bundle would make Apply upsert episode rows the operator
// never chose (rename.ApplyLibrarySeries' extra-episode loop). The cleared
// value comes from marshalExtraEpisodes(nil) rather than a literal so it stays
// in lockstep with ReplacePending's own encoding — "" (not "[]", not NULL),
// which is what scanProposal's non-empty guard expects.
func (s *Store) RepickEpisode(ctx context.Context, id int64, title string, tmdbID, year, season, episode int) error {
	extra, err := marshalExtraEpisodes(nil)
	if err != nil {
		return fmt.Errorf("clearing extra episode numbers for proposal %d: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET title = ?, tmdb_id = ?, year = ?,
			season_number = ?, episode_number = ?, extra_episode_numbers = ?,
			status = ?, reason = ''
		WHERE id = ?
	`, title, tmdbID, year, season, episode, extra, string(Pending), id)
	if err != nil {
		return fmt.Errorf("re-picking episode for proposal %d: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// MarkDraftSubmitted records that a scene draft was successfully submitted to
// a community database for proposal id, stamping draftID and the current
// time. Does not change Status — the proposal stays Unmatched (or whatever it
// was) since submitting a draft doesn't identify the file; it only offers it
// up for others to identify.
func (s *Store) MarkDraftSubmitted(ctx context.Context, id int64, draftID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET draft_id = ?, draft_submitted_at = sakms_now()
		WHERE id = ?
	`, draftID, id)
	if err != nil {
		return fmt.Errorf("marking proposal %d draft-submitted: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

// MarkFingerprintSubmitted records a successful phash give-back for proposal
// id (either an automatic attempt at Apply time or a later manual retry).
// Does not change Status.
func (s *Store) MarkFingerprintSubmitted(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET fingerprint_submitted_at = sakms_now()
		WHERE id = ?
	`, id)
	if err != nil {
		return fmt.Errorf("marking proposal %d fingerprint-submitted: %w", id, err)
	}
	return dbutil.CheckAffected(res, id, ErrNotFound)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProposal(row rowScanner) (Proposal, error) {
	var p Proposal
	var m, wf, status, candidatesJSON, extraEpisodesJSON, genresJSON, castJSON string
	if err := row.Scan(&p.ID, &m, &wf, &status, &p.SourceName, &p.SourcePath, &p.RootFolderPath,
		&p.Title, &p.TVDBID, &p.TMDBID, &p.SeasonNumber, &p.EpisodeNumber, &p.Year, &p.QualityProfileID, &p.Reason, &p.TrackedID,
		&p.ForeignID, &p.ItemType, &candidatesJSON, &p.Studio, &p.Date,
		&p.DraftID, &p.DraftSubmittedAt,
		&p.PHash, &p.DurationSeconds, &p.GiveBackBox, &p.GiveBackSceneID, &p.FingerprintSubmittedAt,
		&p.CreatedAt, &p.AppliedAt, &extraEpisodesJSON,
		&genresJSON, &castJSON, &p.PHashSimilarity); err != nil {
		return Proposal{}, err
	}
	p.Mode, p.Workflow, p.Status = mode.Mode(m), Workflow(wf), Status(status)
	if err := json.Unmarshal([]byte(candidatesJSON), &p.Candidates); err != nil {
		return Proposal{}, fmt.Errorf("decoding candidates for proposal %d: %w", p.ID, err)
	}
	if extraEpisodesJSON != "" {
		if err := json.Unmarshal([]byte(extraEpisodesJSON), &p.ExtraEpisodeNumbers); err != nil {
			return Proposal{}, fmt.Errorf("decoding extra episode numbers for proposal %d: %w", p.ID, err)
		}
	}
	if err := json.Unmarshal([]byte(genresJSON), &p.Genres); err != nil {
		return Proposal{}, fmt.Errorf("decoding genres for proposal %d: %w", p.ID, err)
	}
	if err := json.Unmarshal([]byte(castJSON), &p.Cast); err != nil {
		return Proposal{}, fmt.Errorf("decoding cast for proposal %d: %w", p.ID, err)
	}
	return p, nil
}

// marshalExtraEpisodes encodes nums for the extra_episode_numbers column —
// "" (not "null"/"[]") for the empty/nil case, matching the column's own
// NOT NULL DEFAULT " "empty string means absent" convention (the same
// convention FilePath already uses), rather than always storing a JSON
// array even when there's nothing to say.
func marshalExtraEpisodes(nums []int) (string, error) {
	if len(nums) == 0 {
		return "", nil
	}
	b, err := json.Marshal(nums)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// marshalStringSlice encodes ss as a JSON array — always [] for the nil/empty
// case, matching the genres/cast columns' DEFAULT '[]'.
func marshalStringSlice(ss []string) (string, error) {
	if len(ss) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
