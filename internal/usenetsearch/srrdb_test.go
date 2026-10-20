package usenetsearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSRRDBClient_ByCRCUniqueHit(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("User-Agent") != srrdbUserAgent {
			t.Errorf("User-Agent=%q", r.Header.Get("User-Agent"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"release": "Harry.Potter.And.The.Chamber.Of.Secrets.2002.DVDRip.XViD-iNTERNAL-TDF",
				"size":    1470905475,
			}},
			"resultsCount": 1,
		})
	}))
	t.Cleanup(srv.Close)
	c := NewSRRDBClient(srv.URL + "/v1/search/")
	c.Interval = time.Nanosecond
	hit, ok := c.ByCRC(context.Background(), "0053ca13")
	if !ok || hit.Title == "" || hit.SizeBytes != 1470905475 {
		t.Fatalf("hit=%+v ok=%v", hit, ok)
	}
	if !strings.HasSuffix(gotPath, "/archive-crc:0053CA13") {
		t.Fatalf("path=%q", gotPath)
	}
}

func TestSRRDBClient_ByCRCAmbiguousAndStringCount(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results":      []map[string]any{{"release": "A"}, {"release": "B"}},
				"resultsCount": "2",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":      []map[string]any{{"release": "Only.One.2021.1080p.WEB.x264-GRP", "size": 100}},
			"resultsCount": "1",
		})
	}))
	t.Cleanup(srv.Close)
	c := NewSRRDBClient(srv.URL)
	c.Interval = time.Nanosecond
	if _, ok := c.ByCRC(context.Background(), "AABBCCDD"); ok {
		t.Fatal("ambiguous must miss")
	}
	hit, ok := c.ByCRC(context.Background(), "11223344")
	if !ok || hit.Title != "Only.One.2021.1080p.WEB.x264-GRP" {
		t.Fatalf("string count hit=%+v ok=%v", hit, ok)
	}
}

func TestSRRDBClient_RejectsBadCRC(t *testing.T) {
	c := NewSRRDBClient("http://127.0.0.1:1/")
	c.Interval = time.Nanosecond
	if _, ok := c.ByCRC(context.Background(), "00000000"); ok {
		t.Fatal("zero CRC")
	}
	if _, ok := c.ByCRC(context.Background(), "not-crc"); ok {
		t.Fatal("garbage CRC")
	}
}

func TestSFVEntries(t *testing.T) {
	data := []byte("; generated\r\nSome.Show.S01E02.720p.HDTV.x264-GRP.mkv AABBCCDD\n" +
		"folder\\proof.jpg 11111111\n")
	got := sfvEntries(data)
	if len(got) != 2 || got[0].Name != "Some.Show.S01E02.720p.HDTV.x264-GRP.mkv" || got[0].CRC != "AABBCCDD" {
		t.Fatalf("sfv = %+v", got)
	}
	pref := preferredSFVEntries(got)
	if pref[0].CRC != "AABBCCDD" {
		t.Fatalf("payload CRC first: %+v", pref)
	}
}
