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
	// bulk proposal, so each needs to record which episode it is. Unlike
	// grabs.Grab's fields of the same name, no companion "was this
	// specified" flag is needed here: a Proposal's SeasonNumber always
	// comes from a successful library.ParseEpisodeFilename call, so 0
	// unambiguously means the filename itself encoded season 0 (Specials).
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

// ReplacePending atomically replaces every Pending/Unmatched proposal for
// (m, wf) with fresh — the effect of running Scan again. Applied and
// Dismissed rows are untouched: they're history, not part of the live queue,
// so a new Scan never erases a decision already made. Returns fresh with IDs
// and CreatedAt populated from what was actually inserted.
func (s *Store) ReplacePending(ctx context.Context, m mode.Mode, wf Workflow, fresh []Proposal) ([]Proposal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM proposals WHERE mode = ? AND workflow = ? AND status IN (?, ?)`,
		string(m), string(wf), string(Pending), string(Unmatched)); err != nil {
		return nil, fmt.Errorf("clearing previous queue: %w", err)
	}

	out := make([]Proposal, len(fresh))
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
		row := tx.QueryRowContext(ctx, `
			INSERT INTO proposals (
				mode, workflow, status, source_name, source_path, root_folder_path,
				title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
				foreign_id, item_type, candidates_json, studio, scene_date,
				phash, duration_seconds, give_back_box, give_back_scene_id, extra_episode_numbers,
				genres, "cast"
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id, created_at
		`, string(p.Mode), string(p.Workflow), string(p.Status), p.SourceName, p.SourcePath, p.RootFolderPath,
			p.Title, p.TVDBID, p.TMDBID, p.SeasonNumber, p.EpisodeNumber, p.Year, p.QualityProfileID, p.Reason, p.TrackedID,
			p.ForeignID, p.ItemType, string(candidatesJSON), p.Studio, p.Date,
			p.PHash, p.DurationSeconds, p.GiveBackBox, p.GiveBackSceneID, extraEpisodesJSON,
			genresJSON, castJSON)
		if err := row.Scan(&p.ID, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("inserting proposal for %q: %w", p.SourceName, err)
		}
		out[i] = p
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing queue replacement: %w", err)
	}
	return out, nil
}

// List returns every proposal for (m, wf), most recently created first.
func (s *Store) List(ctx context.Context, m mode.Mode, wf Workflow) ([]Proposal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, workflow, status, source_name, source_path, root_folder_path,
		       title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
		       foreign_id, item_type, candidates_json, studio, scene_date,
		       draft_id, COALESCE(draft_submitted_at, ''),
		       phash, duration_seconds, give_back_box, give_back_scene_id, COALESCE(fingerprint_submitted_at, ''),
		       created_at, COALESCE(applied_at, ''), COALESCE(extra_episode_numbers, ''),
		       COALESCE(genres, '[]'), COALESCE("cast", '[]')
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

// Get returns a single proposal by ID.
func (s *Store) Get(ctx context.Context, id int64) (*Proposal, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, workflow, status, source_name, source_path, root_folder_path,
		       title, tvdb_id, tmdb_id, season_number, episode_number, year, quality_profile_id, reason, tracked_id,
		       foreign_id, item_type, candidates_json, studio, scene_date,
		       draft_id, COALESCE(draft_submitted_at, ''),
		       phash, duration_seconds, give_back_box, give_back_scene_id, COALESCE(fingerprint_submitted_at, ''),
		       created_at, COALESCE(applied_at, ''), COALESCE(extra_episode_numbers, ''),
		       COALESCE(genres, '[]'), COALESCE("cast", '[]')
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

// MarkApplied records that proposal id was successfully acted on, producing
// trackedID in the target *arr app.
func (s *Store) MarkApplied(ctx context.Context, id int64, trackedID int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET status = ?, tracked_id = ?, applied_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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

// MarkDraftSubmitted records that a scene draft was successfully submitted to
// a community database for proposal id, stamping draftID and the current
// time. Does not change Status — the proposal stays Unmatched (or whatever it
// was) since submitting a draft doesn't identify the file; it only offers it
// up for others to identify.
func (s *Store) MarkDraftSubmitted(ctx context.Context, id int64, draftID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET draft_id = ?, draft_submitted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
		UPDATE proposals SET fingerprint_submitted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
		&genresJSON, &castJSON); err != nil {
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
