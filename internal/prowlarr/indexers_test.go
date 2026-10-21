package prowlarr

import (
	"context"
	"net/http"
	"testing"
)

func TestUsenetNewznab_FiltersEnabledUsenetWithKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			t.Errorf("path = %s, want /api/v1/indexer", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key")
		}
		w.Write([]byte(`[
			{"id":1,"name":"NZBGeek","enable":true,"protocol":"usenet","implementation":"Newznab","fields":[
				{"name":"baseUrl","value":"https://api.nzbgeek.info"},
				{"name":"apiPath","value":"/api"},
				{"name":"apiKey","value":"idx-secret"}
			]},
			{"id":2,"name":"TorrentSite","enable":true,"protocol":"torrent","fields":[
				{"name":"baseUrl","value":"https://t.example"},
				{"name":"apiKey","value":"t-secret"}
			]},
			{"id":3,"name":"Off","enable":false,"protocol":"usenet","fields":[
				{"name":"baseUrl","value":"https://off.example"},
				{"name":"apiKey","value":"off-secret"}
			]},
			{"id":4,"name":"NoKey","enable":true,"protocol":"usenet","fields":[
				{"name":"baseUrl","value":"https://nokey.example"}
			]}
		]`))
	})
	got, err := c.UsenetNewznab(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "NZBGeek" || got[0].Endpoint != "https://api.nzbgeek.info/api" || got[0].APIKey != "idx-secret" {
		t.Fatalf("got %+v", got)
	}
}

func TestJoinNewznabAPI_DoesNotDoublePath(t *testing.T) {
	if got := joinNewznabAPI("https://idx.example/api", "/api"); got != "https://idx.example/api" {
		t.Fatalf("got %s", got)
	}
	if got := joinNewznabAPI("https://idx.example", ""); got != "https://idx.example/api" {
		t.Fatalf("got %s", got)
	}
}

func TestUsenetNewznab_EmptyOnMiss(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	got, err := c.UsenetNewznab(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
