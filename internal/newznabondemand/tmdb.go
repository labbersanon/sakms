package newznabondemand

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/labbersanon/sakms/internal/newznab"
)

// Claude 2026-10-23: IMDb→TMDB at verify, not on Discover render.
// Reason: Newznab RSS has imdb, not tmdb. Poster path needs tmdbId.
//   Unmatched titles stay off the On Demand shelf.
// Troubleshooting: empty shelf → finder nil/unset, or TMDB has no imdb row.
// Review if: Play starts-while-downloading.

// ErrTMDBUnset means Settings has no TMDB key. Do not stamp tmdb_id=0.
var ErrTMDBUnset = errors.New("tmdb unset")

var tmdbUnsetOnce sync.Once

// IMDBFinder maps indexer ids onto TMDB. Tests stub this.
type IMDBFinder interface {
	MovieByIMDB(ctx context.Context, imdbID string) (int, error)
	TVByIMDB(ctx context.Context, imdbID string) (int, error)
	TVByTVDB(ctx context.Context, tvdbID int) (int, error)
}

func resolveCatalogTMDB(ctx context.Context, store *Store, finder IMDBFinder, row Row) error {
	if store == nil || row.Indexer == "" || row.GUID == "" {
		return nil
	}
	imdbID, tmdbID, tvdbID, err := store.CatalogIDs(ctx, row.Indexer, row.GUID)
	if err != nil {
		return err
	}
	if n, _ := strconv.Atoi(newznab.DigitsOnly(tmdbID)); n > 0 {
		return nil
	}
	id, tried, err := lookupTMDB(ctx, finder, row.Kind, imdbID, tvdbID)
	if err != nil {
		if errors.Is(err, ErrTMDBUnset) {
			tmdbUnsetOnce.Do(func() {
				log.Printf("newznab ondemand: tmdb unset, unmatched titles stay hidden")
			})
		}
		return err
	}
	val := "0"
	if id > 0 {
		val = strconv.Itoa(id)
	}
	if tried && newznab.NormalizeIMDB(imdbID) != "" {
		if err := store.SetCatalogTMDBForIMDB(ctx, imdbID, val); err != nil {
			return err
		}
	}
	return store.SetCatalogTMDB(ctx, row.Indexer, row.GUID, val)
}

func lookupTMDB(ctx context.Context, finder IMDBFinder, kind, imdbID, tvdbID string) (id int, tried bool, err error) {
	if finder == nil {
		return 0, false, ErrTMDBUnset
	}
	findIMDB := finder.MovieByIMDB
	if kind == "series" {
		findIMDB = finder.TVByIMDB
	}
	for _, cand := range imdbCandidates(imdbID) {
		tried = true
		id, err = findIMDB(ctx, cand)
		if err != nil {
			return 0, true, err
		}
		if id > 0 {
			return id, true, nil
		}
	}
	if kind == "series" {
		n, _ := strconv.Atoi(newznab.DigitsOnly(tvdbID))
		if n > 0 {
			tried = true
			id, err = finder.TVByTVDB(ctx, n)
			return id, true, err
		}
	}
	return 0, tried, nil
}

func imdbCandidates(raw string) []string {
	id := newznab.NormalizeIMDB(raw)
	if id == "" || id == "tt0000000" {
		return nil
	}
	out := []string{id}
	digits := strings.TrimPrefix(id, "tt")
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return nil
	}
	for len(trimmed) < 7 {
		trimmed = "0" + trimmed
	}
	alt := "tt" + trimmed
	if alt != id {
		out = append(out, alt)
	}
	return out
}
