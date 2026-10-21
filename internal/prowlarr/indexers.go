package prowlarr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Claude 2026-10-21: list enabled Usenet Newznab endpoints from Prowlarr.
// Reason: namefix needs t=search by obfuscated archive name on the same
//   keyed indexers Prowlarr already has. Keys stay in Prowlarr; sakms
//   fetches them at runtime and never writes them to its own files.
// Troubleshooting: empty list → protocol!=usenet, enable=false, or missing
//   baseUrl/apiKey fields. Do not scrape indexer HTML.
// Review if: Prowlarr IndexerResource field names change.
// Related files: internal/newznab/client.go, internal/api/nzbpassword.go

// NewznabIndexer is one enabled Usenet Newznab site from GET /api/v1/indexer.
// APIKey must not be logged.
type NewznabIndexer struct {
	ID       int
	Name     string
	Endpoint string // scheme+host+api path, no apikey
	APIKey   string
}

type indexerResource struct {
	ID             int            `json:"id"`
	Name           string         `json:"name"`
	Enable         bool           `json:"enable"`
	Protocol       string         `json:"protocol"`
	Implementation string         `json:"implementation"`
	Fields         []indexerField `json:"fields"`
}

type indexerField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func fieldString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func joinNewznabAPI(base, apiPath string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	apiPath = strings.TrimSpace(apiPath)
	if apiPath == "" {
		apiPath = "/api"
	}
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	if strings.HasSuffix(strings.ToLower(base), strings.ToLower(apiPath)) {
		return base
	}
	return base + apiPath
}

func (r indexerResource) newznab() (NewznabIndexer, bool) {
	if !r.Enable || !strings.EqualFold(strings.TrimSpace(r.Protocol), "usenet") {
		return NewznabIndexer{}, false
	}
	var base, apiPath, apiKey string
	for _, f := range r.Fields {
		switch strings.ToLower(strings.TrimSpace(f.Name)) {
		case "baseurl", "url":
			if base == "" {
				base = fieldString(f.Value)
			}
		case "apipath":
			apiPath = fieldString(f.Value)
		case "apikey", "api_key":
			apiKey = fieldString(f.Value)
		}
	}
	if base == "" || apiKey == "" {
		return NewznabIndexer{}, false
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = strings.TrimSpace(r.Implementation)
	}
	if name == "" {
		name = strconv.Itoa(r.ID)
	}
	return NewznabIndexer{
		ID:       r.ID,
		Name:     name,
		Endpoint: joinNewznabAPI(base, apiPath),
		APIKey:   apiKey,
	}, true
}

// UsenetNewznab returns enabled Usenet indexers that expose a Newznab base
// URL and API key. Misses are empty, not an error.
func (c *Client) UsenetNewznab(ctx context.Context) ([]NewznabIndexer, error) {
	if c == nil {
		return nil, nil
	}
	var raw []indexerResource
	if err := c.do(ctx, "/api/v1/indexer", &raw); err != nil {
		return nil, err
	}
	out := make([]NewznabIndexer, 0, len(raw))
	for _, r := range raw {
		idx, ok := r.newznab()
		if !ok {
			continue
		}
		out = append(out, idx)
	}
	return out, nil
}
