package newznab

import (
	"context"
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

// CatalogPageSize is the catalog crawl page length. Smaller than grab
// search so each paced request stays cheap for the indexer.
func CatalogPageSize() int { return catalogPageSize }

// Claude 2026-10-23: category-only t=search pages for the local catalog.
// Reason: grab search requires q=; overlap indexing pages cat=2000/5000/6000
//
//	with offset. Do not scrape HTML. Do not fetch NZBs.
//
// Troubleshooting: empty search error → catalog flag not set on searchReq.
// Review if: an indexer requires q=* for latest-in-cat (retry in the crawler).
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
	items, err := c.search(ctx, idx, searchReq{
		cats:    []int{cat},
		offset:  offset,
		limit:   limit,
		catalog: true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			return nil, err
		}
		// Some hosts reject cat-only latest; q=* is still a catalog page,
		// not HTML and not an NZB fetch.
		items, err = c.search(ctx, idx, searchReq{
			q:       "*",
			cats:    []int{cat},
			offset:  offset,
			limit:   limit,
			catalog: true,
		})
		if err != nil {
			return nil, err
		}
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
