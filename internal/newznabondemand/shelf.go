package newznabondemand

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/release"
)

// Claude 2026-10-23: Discover On Demand shelf is verified rows only.
// Reason: chip must not recrawl or return enclosure/apikey/details URLs.
// Troubleshooting: empty grid → crawler still fetching/STAT, kind mismatch,
//   or catalog.tmdb_id empty/0 (no IMDb→TMDB match).
// Review if: Play starts-while-downloading (keep nzbId as GetNZBID hash).

var (
	episodeToken = regexp.MustCompile(`(?i)^s\d{1,2}e\d{1,3}$`)
	seasonToken  = regexp.MustCompile(`(?i)^s\d{1,2}$`)
	yearToken    = regexp.MustCompile(`^(?:19|20)\d{2}$`)
)

// ShelfRelease is one verified NZB for a title+quality. NzbID is GetNZBID
// (hash), never a catalog details URL.
type ShelfRelease struct {
	Indexer string
	NzbID   string
	Label   string
}

// ShelfTitle is one Discover card: grouped verified NZBs, quality options.
type ShelfTitle struct {
	ID        string
	Kind      string
	Title     string
	Year      int
	ImdbID    string
	TmdbID    int
	TvdbID    string
	Qualities []ShelfRelease
}

type shelfRow struct {
	Indexer string
	GUID    string
	Kind    string
	Title   string
	ImdbID  string
	TvdbID  string
	TmdbID  string
	Year    int
}

func (s *Store) ListVerified(ctx context.Context, kind string) ([]ShelfTitle, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.indexer, o.guid, o.kind, o.title,
			COALESCE(c.imdb_id, ''), COALESCE(c.tvdb_id, ''), COALESCE(c.tmdb_id, ''), COALESCE(c.year, 0)
		FROM newznab_ondemand o
		LEFT JOIN newznab_catalog c ON c.indexer = o.indexer AND c.guid = o.guid
		WHERE o.status = $1 AND o.kind = $2
		ORDER BY o.indexer ASC, o.guid ASC`, StatusVerified, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var raw []shelfRow
	for rows.Next() {
		var r shelfRow
		if err := rows.Scan(&r.Indexer, &r.GUID, &r.Kind, &r.Title, &r.ImdbID, &r.TvdbID, &r.TmdbID, &r.Year); err != nil {
			return nil, err
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groupShelf(raw), nil
}

func groupShelf(rows []shelfRow) []ShelfTitle {
	type acc struct {
		title    ShelfTitle
		seenQual map[string]bool
	}
	grouped := map[string]*acc{}
	order := []string{}
	for _, r := range rows {
		nzbID := newznab.GetNZBID(r.GUID)
		if nzbID == "" || looksLikeURL(nzbID) || looksLikeURL(r.Indexer) {
			continue
		}
		key := groupKey(r)
		a := grouped[key]
		if a == nil {
			year := r.Year
			if year == 0 {
				year = newznabYear(r.Title)
			}
			tmdbID, _ := strconv.Atoi(newznab.DigitsOnly(r.TmdbID))
			if tmdbID <= 0 {
				continue
			}
			a = &acc{
				title: ShelfTitle{
					ID:     key,
					Kind:   r.Kind,
					Title:  DisplayTitle(r.Title),
					Year:   year,
					ImdbID: newznab.NormalizeIMDB(r.ImdbID),
					TmdbID: tmdbID,
					TvdbID: newznab.DigitsOnly(r.TvdbID),
				},
				seenQual: map[string]bool{},
			}
			grouped[key] = a
			order = append(order, key)
		}
		label := QualityLabel(r.Title)
		if label == "" {
			label = "Unknown"
		}
		if a.seenQual[label] {
			continue
		}
		a.seenQual[label] = true
		a.title.Qualities = append(a.title.Qualities, ShelfRelease{
			Indexer: r.Indexer,
			NzbID:   nzbID,
			Label:   label,
		})
	}
	out := make([]ShelfTitle, 0, len(order))
	for _, key := range order {
		t := grouped[key].title
		sort.Slice(t.Qualities, func(i, j int) bool {
			return qualityRank(t.Qualities[i].Label) < qualityRank(t.Qualities[j].Label)
		})
		if t.TmdbID <= 0 {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

func groupKey(r shelfRow) string {
	imdb := newznab.NormalizeIMDB(r.ImdbID)
	tmdb := newznab.DigitsOnly(r.TmdbID)
	tvdb := newznab.DigitsOnly(r.TvdbID)
	ep := episodeKey(r.Title)
	switch r.Kind {
	case "movies":
		if imdb != "" {
			return "imdb:" + imdb
		}
		if tmdb != "" {
			return "tmdb:" + tmdb
		}
	case "series":
		base := ""
		if tvdb != "" {
			base = "tvdb:" + tvdb
		} else if tmdb != "" {
			base = "tmdb:" + tmdb
		}
		if base != "" {
			if ep != "" {
				return base + ":" + ep
			}
			return base
		}
	}
	title := strings.ToLower(DisplayTitle(r.Title))
	if ep != "" && r.Kind == "series" {
		return "title:" + title + ":" + ep
	}
	return "title:" + title
}

func episodeKey(title string) string {
	season := ""
	for _, w := range titleWords(title) {
		lw := strings.ToLower(w)
		if episodeToken.MatchString(lw) {
			return lw
		}
		if season == "" && seasonToken.MatchString(lw) {
			season = lw
		}
	}
	return season
}

func QualityLabel(title string) string {
	info := release.Parse(title)
	switch {
	case info.Resolution >= 2160:
		return "2160p"
	case info.Resolution >= 1080:
		return "1080p"
	case info.Resolution >= 720:
		return "720p"
	case info.Resolution >= 480:
		return "480p"
	default:
		return ""
	}
}

func qualityRank(label string) int {
	switch label {
	case "2160p":
		return 0
	case "1080p":
		return 1
	case "720p":
		return 2
	case "480p":
		return 3
	default:
		return 9
	}
}

func DisplayTitle(raw string) string {
	words := titleWords(raw)
	keep := make([]string, 0, len(words))
	yearSeen := false
	for _, w := range words {
		lw := strings.ToLower(strings.Trim(w, "()[]"))
		if isCutToken(lw) {
			break
		}
		if yearToken.MatchString(lw) {
			if len(keep) == 0 {
				continue
			}
			keep = append(keep, w)
			yearSeen = true
			continue
		}
		epOrSeason := episodeToken.MatchString(lw) || seasonToken.MatchString(lw)
		if yearSeen && !epOrSeason {
			break
		}
		if epOrSeason {
			keep = append(keep, strings.ToUpper(lw))
			continue
		}
		keep = append(keep, w)
	}
	if len(keep) == 0 {
		return strings.Join(words, " ")
	}
	return strings.Join(keep, " ")
}

func titleWords(raw string) []string {
	s := strings.ReplaceAll(raw, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	return strings.Fields(s)
}

func isCutToken(lw string) bool {
	switch lw {
	case "2160p", "1080p", "1080i", "720p", "480p", "576p", "540p", "4k", "uhd",
		"x264", "x265", "h264", "h265", "h.264", "h.265", "hevc", "av1", "xvid",
		"web-dl", "webdl", "webrip", "bluray", "blu-ray", "bdrip", "brrip",
		"hdtv", "dvdrip", "remux", "proper", "repack":
		return true
	default:
		return false
	}
}

func newznabYear(title string) int {
	for _, w := range titleWords(title) {
		if yearToken.MatchString(w) {
			n, _ := strconv.Atoi(w)
			return n
		}
	}
	return 0
}
