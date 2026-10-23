package newznab

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// CatalogHit is one t=search row kept for overlap indexing.
// Download URLs are omitted — enclosure links often embed apikeys.
type CatalogHit struct {
	GUID     string
	Title    string
	Indexer  string
	Size     int64
	Category int
	ImdbID   string
	TvdbID   string
	TmdbID   string
	Year     int
}

// CatalogPageSize is the catalog crawl page length. NZB.life allows 1–100
// (default 100); 100 halves request count vs 50 at the same 5s pace.
func CatalogPageSize() int { return catalogPageSize }

// Claude 2026-10-23: catalog t=search is cat-only; q=* only on missing-q.
// Reason: NZB.life pages cat=2000 without q (helpapi q=Required is wrong-domain 200). q=* is a literal asterisk search and returned 0 series/adult hits, which marked those lanes complete.
// Troubleshooting: Missing parameter retry q=* did not fire. Empty host-2 series on first page means q=* came back as the first request.
// Review if: NZB.life documents a real latest-in-cat query.
func (c *Client) SearchCatalog(ctx context.Context, idx Indexer, cat, offset, limit int) ([]CatalogHit, error) {
	if cat <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > catalogPageSize {
		limit = catalogPageSize
	}
	if offset < 0 {
		offset = 0
	}
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	req := searchReq{cats: []int{cat}, offset: offset, limit: limit, catalog: true}
	items, err := c.search(ctx, idx, req)
	var ae APIError
	if errors.As(err, &ae) && ae.NoItems() {
		return nil, nil
	}
	if errors.As(err, &ae) && ae.MissingParam() {
		req.q = "*"
		items, err = c.search(ctx, idx, req)
	}
	if err != nil {
		if errors.As(err, &ae) && ae.NoItems() {
			return nil, nil
		}
		return nil, err
	}
	out := make([]CatalogHit, 0, len(items))
	for _, it := range items {
		hit, ok := catalogHitFromItem(idx, it)
		if !ok {
			continue
		}
		out = append(out, hit)
	}
	return out, nil
}

func catalogHitFromItem(idx Indexer, it rssItem) (CatalogHit, bool) {
	title := strings.TrimSpace(it.Title)
	guid := strings.TrimSpace(it.GUID)
	if guid == "" {
		guid = strings.TrimSpace(it.attr("guid"))
	}
	if title == "" || guid == "" {
		return CatalogHit{}, false
	}
	size, _ := strconv.ParseInt(it.attr("size"), 10, 64)
	if size <= 0 {
		size, _ = strconv.ParseInt(it.Enclosure.Length, 10, 64)
	}
	cat := 0
	if raw := it.attr("category"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err == nil && n > 0 {
				cat = n
				break
			}
		}
	}
	year := 0
	if raw := it.attr("year"); raw != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			year = n
		}
	}
	if year == 0 {
		year = yearFromTitle(title)
	}
	return CatalogHit{
		GUID:     guid,
		Title:    title,
		Indexer:  idx.Name,
		Size:     size,
		Category: cat,
		ImdbID:   firstAttr(it, "imdbid", "imdb"),
		TvdbID:   firstAttr(it, "tvdbid", "rageid"),
		TmdbID:   firstAttr(it, "tmdbid", "tmdb"),
		Year:     year,
	}, true
}

func firstAttr(it rssItem, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(it.attr(name)); v != "" {
			return v
		}
	}
	return ""
}
