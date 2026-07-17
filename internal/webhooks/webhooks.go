// Package webhooks persists operator-configured outbound webhook subscriptions
// and dispatches event notifications to them after key SAK actions (Apply,
// grab). Each subscription carries a URL, an optional HMAC-SHA256 signing
// secret, the set of event names it subscribes to, and an enabled flag.
//
// Dispatch is fire-and-forget: each delivery runs in a background goroutine
// with a 10-second timeout. Delivery failures are logged and silently ignored
// so they never block the triggering action. The secret (when present) is
// encrypted at rest via the same internal/secrets encryptor used by
// internal/connections and internal/trakt.
//
// Supported event names (pass as the event argument to Dispatch):
//
//	EventRenameApplied  — a Rename proposal was applied
//	EventPurgeApplied   — a Purge proposal was applied
//	EventDedupApplied   — a Dedup proposal was applied
//	EventGrabCompleted  — a grab was sent to a download client
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Supported event name constants.
const (
	EventRenameApplied = "rename.applied"
	EventPurgeApplied  = "purge.applied"
	EventDedupApplied  = "dedup.applied"
	EventGrabCompleted = "grab.completed"
)

// AllEvents is the canonical list of supported event names.
var AllEvents = []string{
	EventRenameApplied,
	EventPurgeApplied,
	EventDedupApplied,
	EventGrabCompleted,
}

// ErrNotFound is returned when an id has no stored webhook.
var ErrNotFound = errors.New("webhooks: no webhook with that id")

// ErrURLRequired is returned when URL is blank.
var ErrURLRequired = errors.New("webhooks: url is required")

// ErrInvalidEvent is returned when an unknown event name is given.
var ErrInvalidEvent = errors.New("webhooks: unknown event name")

var validEvents = func() map[string]bool {
	m := make(map[string]bool, len(AllEvents))
	for _, e := range AllEvents {
		m[e] = true
	}
	return m
}()

type encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Webhook is one stored subscription, with secret already decrypted.
type Webhook struct {
	ID        int64
	URL       string
	Secret    string // "" means no signing
	Events    []string
	Enabled   bool
	CreatedAt string
	UpdatedAt string
}

// Summary is what's safe to expose over the API — secret is never included.
type Summary struct {
	ID        int64    `json:"id"`
	URL       string   `json:"url"`
	SecretSet bool     `json:"secretSet"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// Store is the persistence layer for webhook subscriptions.
type Store struct {
	db      *sql.DB
	secrets encryptor
}

// New returns a Store using the given db and encryptor.
func New(db *sql.DB, secretStore encryptor) *Store {
	return &Store{db: db, secrets: secretStore}
}

func (s *Store) toSummary(w Webhook) Summary {
	return Summary{
		ID: w.ID, URL: w.URL, SecretSet: w.Secret != "",
		Events: w.Events, Enabled: w.Enabled,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func eventsToJSON(events []string) string {
	b, _ := json.Marshal(events)
	return string(b)
}

func eventsFromJSON(raw string) []string {
	var events []string
	_ = json.Unmarshal([]byte(raw), &events)
	if events == nil {
		return []string{}
	}
	return events
}

// List returns all stored webhooks as summaries.
func (s *Store) List(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, secret_enc, events, enabled, created_at, updated_at
		FROM webhooks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var id int64
		var url, secretEnc, eventsRaw, createdAt, updatedAt string
		var enabledInt int
		if err := rows.Scan(&id, &url, &secretEnc, &eventsRaw, &enabledInt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		out = append(out, Summary{
			ID: id, URL: url, SecretSet: secretEnc != "",
			Events:    eventsFromJSON(eventsRaw),
			Enabled:   enabledInt == 1,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		})
	}
	if out == nil {
		out = []Summary{}
	}
	return out, rows.Err()
}

// Create stores a new subscription. secret may be "" (no signing).
// Returns the new row's id.
func (s *Store) Create(ctx context.Context, url, secret string, events []string, enabled bool) (int64, error) {
	if url == "" {
		return 0, ErrURLRequired
	}
	for _, e := range events {
		if !validEvents[e] {
			return 0, fmt.Errorf("%w: %q", ErrInvalidEvent, e)
		}
	}
	encSecret := ""
	if secret != "" {
		var err error
		encSecret, err = s.secrets.Encrypt(secret)
		if err != nil {
			return 0, fmt.Errorf("encrypting secret: %w", err)
		}
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO webhooks (url, secret_enc, events, enabled)
		VALUES (?, ?, ?, ?)`,
		url, encSecret, eventsToJSON(events), enabledInt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update modifies an existing subscription. secret follows three-state
// semantics: nil = preserve, "" = clear, non-empty = update.
func (s *Store) Update(ctx context.Context, id int64, url string, secret *string, events []string, enabled bool) error {
	if url == "" {
		return ErrURLRequired
	}
	for _, e := range events {
		if !validEvents[e] {
			return fmt.Errorf("%w: %q", ErrInvalidEvent, e)
		}
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	if secret == nil {
		// Preserve existing secret — don't touch secret_enc.
		res, err := s.db.ExecContext(ctx, `
			UPDATE webhooks SET url=?, events=?, enabled=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
			WHERE id=?`,
			url, eventsToJSON(events), enabledInt, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	}

	encSecret := ""
	if *secret != "" {
		var err error
		encSecret, err = s.secrets.Encrypt(*secret)
		if err != nil {
			return fmt.Errorf("encrypting secret: %w", err)
		}
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET url=?, secret_enc=?, events=?, enabled=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE id=?`,
		url, encSecret, eventsToJSON(events), enabledInt, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

// Delete removes a webhook subscription.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id=?`, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func checkRowsAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// listEnabled returns all enabled webhooks that subscribe to the given event,
// with secrets decrypted — for internal use by Dispatch only.
func (s *Store) listEnabled(ctx context.Context, event string) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, secret_enc, events, enabled, created_at, updated_at
		FROM webhooks WHERE enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		var eventsRaw string
		var enabledInt int
		if err := rows.Scan(&w.ID, &w.URL, &w.Secret, &eventsRaw, &enabledInt, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		w.Enabled = enabledInt == 1
		w.Events = eventsFromJSON(eventsRaw)

		subscribed := false
		for _, e := range w.Events {
			if e == event {
				subscribed = true
				break
			}
		}
		if !subscribed {
			continue
		}

		// Decrypt secret for signing — "" means no signing.
		if w.Secret != "" {
			decrypted, err := s.secrets.Decrypt(w.Secret)
			if err != nil {
				log.Printf("webhooks: failed to decrypt secret for id %d: %v", w.ID, err)
				w.Secret = ""
			} else {
				w.Secret = decrypted
			}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Event is the payload fired to webhook subscribers.
type Event struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

// Dispatch fires event to all enabled subscribed webhooks in background
// goroutines. It returns immediately; delivery failures are logged silently.
// A nil Store is a no-op — safe when called from handlers wired with nil in tests.
func (s *Store) Dispatch(event string, data any) {
	if s == nil {
		return
	}
	// Snapshot enabled subscribers synchronously to avoid a stale-context race
	// (the request context may cancel before deliveries complete). Use a
	// background context so the dispatch outlives the HTTP request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	hooks, err := s.listEnabled(ctx, event)
	cancel()
	if err != nil {
		log.Printf("webhooks: listing subscribers for %q: %v", event, err)
		return
	}
	if len(hooks) == 0 {
		return
	}

	payload, err := json.Marshal(Event{
		Event:     event,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      data,
	})
	if err != nil {
		log.Printf("webhooks: marshalling %q payload: %v", event, err)
		return
	}

	for _, h := range hooks {
		go deliver(h.URL, h.Secret, payload)
	}
}

func deliver(url, secret string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("webhooks: building request to %s: %v", url, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		sig := hmacSHA256(secret, payload)
		req.Header.Set("X-SAK-Signature", "sha256="+sig)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("webhooks: delivering to %s: %v", url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("webhooks: %s responded %d", url, resp.StatusCode)
	}
}

func hmacSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateEvents returns an error if any name in events is unknown.
func ValidateEvents(events []string) error {
	var bad []string
	for _, e := range events {
		if !validEvents[e] {
			bad = append(bad, e)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidEvent, strings.Join(bad, ", "))
	}
	return nil
}
