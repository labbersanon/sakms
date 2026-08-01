package serviceconn

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test here.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return NewStore(sqlDB, secretStore)
}

func player(label, url string, modes ...string) Connection {
	return Connection{Kind: KindPlayer, Provider: ProviderJellyfin, Label: label, URL: url,
		Enabled: true, Secret: "player-key-3f2a", Modes: modes}
}

func subscription(label, host string) Connection {
	return Connection{Kind: KindUsenet, Provider: ProviderNNTP, Label: label, Host: host,
		Port: 563, TLS: true, MaxConns: 8, Username: "u", Enabled: true, Secret: "nntp-pass"}
}

func TestCreateAndGet_RoundTripsSecretAndModes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, player("Living room", "http://jf:8096", "movies", "series"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected a nonzero id")
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Secret != "player-key-3f2a" {
		t.Errorf("secret should round-trip decrypted, got %q", got.Secret)
	}
	if len(got.Modes) != 2 || got.Modes[0] != "movies" || got.Modes[1] != "series" {
		t.Errorf("want modes [movies series], got %v", got.Modes)
	}

	// The plaintext secret must never be in the DB.
	summaries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("want 1 summary, got %d", len(summaries))
	}
	if !summaries[0].HasSecret || summaries[0].SecretSuffix != "3f2a" {
		t.Errorf("summary should expose only has-secret + last 4, got %+v", summaries[0])
	}
}

// TestUpdate_ThreeStateSecret is the rule an untouched Settings save depends
// on: nil preserves, "" clears, non-empty replaces.
func TestUpdate_ThreeStateSecret(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, player("Living room", "http://jf:8096", "movies"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edit := *created
	edit.Label = "Renamed"

	// nil: preserve.
	if _, err := s.Update(ctx, created.ID, edit, nil); err != nil {
		t.Fatalf("update (preserve): %v", err)
	}
	got, _ := s.Get(ctx, created.ID)
	if got.Secret != "player-key-3f2a" {
		t.Errorf("nil secret must preserve the stored one, got %q", got.Secret)
	}
	if got.Label != "Renamed" {
		t.Errorf("other fields should still update, got label %q", got.Label)
	}
	if len(got.Modes) != 1 {
		t.Errorf("Update must not touch mode assignment, got %v", got.Modes)
	}

	// non-empty: replace.
	replacement := "brand-new"
	if _, err := s.Update(ctx, created.ID, edit, &replacement); err != nil {
		t.Fatalf("update (replace): %v", err)
	}
	if got, _ = s.Get(ctx, created.ID); got.Secret != "brand-new" {
		t.Errorf("a non-empty secret must replace, got %q", got.Secret)
	}

	// "": clear.
	empty := ""
	if _, err := s.Update(ctx, created.ID, edit, &empty); err != nil {
		t.Fatalf("update (clear): %v", err)
	}
	if got, _ = s.Get(ctx, created.ID); got.Secret != "" {
		t.Errorf(`"" must clear the secret, got %q`, got.Secret)
	}
	sums, _ := s.List(ctx)
	if sums[0].HasSecret {
		t.Error("a cleared secret must report HasSecret false")
	}
}

// TestPlayersForMode_OnlyEnabledAndAssigned proves the query mode.Build will
// depend on: many-to-many assignment with no exclusivity, disabled rows
// excluded, unassigned rows excluded.
func TestPlayersForMode_OnlyEnabledAndAssigned(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	both, err := s.Create(ctx, player("Both", "http://a:8096", "movies", "adult"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Create(ctx, player("Movies only", "http://b:8096", "movies")); err != nil {
		t.Fatalf("create: %v", err)
	}
	disabled := player("Disabled", "http://c:8096", "movies")
	disabled.Enabled = false
	if _, err := s.Create(ctx, disabled); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Create(ctx, player("Unassigned", "http://d:8096")); err != nil {
		t.Fatalf("create: %v", err)
	}

	movies, err := s.PlayersForMode(ctx, "movies")
	if err != nil {
		t.Fatalf("players for movies: %v", err)
	}
	if len(movies) != 2 {
		t.Fatalf("want 2 enabled+assigned movies players, got %d", len(movies))
	}
	if movies[0].Secret != "player-key-3f2a" {
		t.Error("PlayersForMode must decrypt secrets — the client builder needs them")
	}

	adult, err := s.PlayersForMode(ctx, "adult")
	if err != nil {
		t.Fatalf("players for adult: %v", err)
	}
	if len(adult) != 1 || adult[0].ID != both.ID {
		t.Errorf("assignment is many-to-many with no exclusivity, got %+v", adult)
	}
	if series, err := s.PlayersForMode(ctx, "series"); err != nil || len(series) != 0 {
		t.Errorf("no player is assigned to series, got %v (err %v)", series, err)
	}
	if _, err := s.PlayersForMode(ctx, "nonsense"); !errors.Is(err, ErrInvalidMode) {
		t.Errorf("an unknown mode should be rejected, got %v", err)
	}
}

func TestSetModes_ReplacesWholesale(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, player("P", "http://a:8096", "movies", "series"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetModes(ctx, created.ID, []string{"adult"}); err != nil {
		t.Fatalf("set modes: %v", err)
	}
	got, _ := s.Get(ctx, created.ID)
	if len(got.Modes) != 1 || got.Modes[0] != "adult" {
		t.Errorf("SetModes replaces wholesale, got %v", got.Modes)
	}
	if err := s.SetModes(ctx, created.ID, []string{"bogus"}); !errors.Is(err, ErrInvalidMode) {
		t.Errorf("an unknown mode should be rejected, got %v", err)
	}

	sub, err := s.Create(ctx, subscription("Usenet", "news.example.com"))
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := s.SetModes(ctx, sub.ID, []string{"movies"}); !errors.Is(err, ErrModesNotPlayer) {
		t.Errorf("a usenet row cannot be mode-assigned, got %v", err)
	}
	if err := s.SetModes(ctx, 9999, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound for a missing id, got %v", err)
	}
}

// TestDelete_RemovesModeRowsWithoutCascade is the PRAGMA foreign_keys guard:
// internal/db's Open never enables FK enforcement, so the schema's ON DELETE
// CASCADE is documentation only and Delete must clear the join rows itself.
func TestDelete_RemovesModeRowsWithoutCascade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, player("P", "http://a:8096", "movies", "series"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}

	var orphans int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM service_connection_modes WHERE connection_id = ?`, created.ID).Scan(&orphans); err != nil {
		t.Fatalf("counting orphan mode rows: %v", err)
	}
	if orphans != 0 {
		t.Errorf("mode rows must be deleted explicitly, %d orphan(s) left behind", orphans)
	}
	if err := s.Delete(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound deleting a missing row, got %v", err)
	}
}

func TestValidate_ShapeAndEnumInvariants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		c    Connection
		want error
	}{
		{"unknown kind", Connection{Kind: "nas", Provider: ProviderPlex, URL: "http://x"}, ErrInvalidKind},
		{"unknown provider", Connection{Kind: KindPlayer, Provider: "kodi", URL: "http://x"}, ErrInvalidProvider},
		{"provider/kind mismatch", Connection{Kind: KindUsenet, Provider: ProviderPlex, Host: "h", Port: 119}, ErrProviderKind},
		{"player without url", Connection{Kind: KindPlayer, Provider: ProviderEmby}, ErrURLRequired},
		{"usenet without host", Connection{Kind: KindUsenet, Provider: ProviderNNTP, Port: 563}, ErrHostRequired},
		{"usenet without port", Connection{Kind: KindUsenet, Provider: ProviderNNTP, Host: "h"}, ErrHostRequired},
		{"usenet with modes", Connection{Kind: KindUsenet, Provider: ProviderNNTP, Host: "h", Port: 563, Modes: []string{"movies"}}, ErrModesNotPlayer},
		{"unknown mode", Connection{Kind: KindPlayer, Provider: ProviderPlex, URL: "http://x", Modes: []string{"anime"}}, ErrInvalidMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Create(ctx, tc.c); !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestListByKind_SplitsUsenetFromPlayers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, subscription("Primary", "news-a.example.com")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Create(ctx, subscription("Block account", "news-b.example.com")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Create(ctx, player("P", "http://a:8096", "movies")); err != nil {
		t.Fatalf("create: %v", err)
	}

	subs, err := s.ListByKind(ctx, KindUsenet)
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("want 2 usenet subscriptions, got %d", len(subs))
	}
	// Multiple rows per provider is the whole point of this table.
	if subs[0].SortOrder != 0 || subs[1].SortOrder != 1 {
		t.Errorf("rows should be appended in order, got %d and %d", subs[0].SortOrder, subs[1].SortOrder)
	}
	if subs[0].Secret != "nntp-pass" || subs[0].MaxConns != 8 {
		t.Errorf("usenet fields should round-trip, got %+v", subs[0])
	}
	players, err := s.ListByKind(ctx, KindPlayer)
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if len(players) != 1 {
		t.Errorf("want 1 player, got %d", len(players))
	}
}

func TestGet_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := s.Update(context.Background(), 404, player("P", "http://a"), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
