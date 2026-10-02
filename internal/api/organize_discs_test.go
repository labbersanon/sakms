package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/disc"
)

func TestOrganizeDiscIdentify_RejectsNonDisc(t *testing.T) {
	tmp := t.TempDir()
	mkv := filepath.Join(tmp, "a.mkv")
	if err := os.WriteFile(mkv, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)
	body, _ := json.Marshal(apidto.OrganizeDiscIdentifyRequest{Path: mkv})
	rr := httptest.NewRecorder()
	organizeDiscIdentifyHandler(nil, nil, nil, nil, nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/identify", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
}

func TestOrganizeDiscIdentify_ReturnsWorks(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)
	orig := inspectDiscFn
	t.Cleanup(func() { inspectDiscFn = orig })
	inspectDiscFn = func(ctx context.Context, path string) (*disc.Map, error) {
		return &disc.Map{
			Volume: "LOONEY_TUNES_GOLDEN_V5_D1",
			Source: path,
			Titles: []disc.Title{
				{N: 1, DurationS: 6386, Chapters: 15},
				{N: 2, DurationS: 433},
				{N: 3, DurationS: 400},
			},
		}, nil
	}
	body, _ := json.Marshal(apidto.OrganizeDiscIdentifyRequest{Path: src})
	rr := httptest.NewRecorder()
	organizeDiscIdentifyHandler(nil, nil, nil, nil, nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/identify", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp apidto.OrganizeDiscIdentifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Volume != "LOONEY_TUNES_GOLDEN_V5_D1" {
		t.Fatalf("volume = %q", resp.Volume)
	}
	if len(resp.Works) != 2 {
		t.Fatalf("works = %+v, want 2 shorts", resp.Works)
	}
	if len(resp.Queries) == 0 {
		t.Fatal("expected search queries")
	}
}

func TestDiscOnlyNames(t *testing.T) {
	got := discOnlyNames([]apidto.OrganizeDiscUnpackItem{
		{Name: "t02", Conflict: "replace"},
		{Name: ""},
		{Name: "t03"},
	})
	if len(got) != 2 || got[0] != "t02" || got[1] != "t03" {
		t.Fatalf("names = %v", got)
	}
}

func TestOrganizeDiscUnpack_PassesOnlyNames(t *testing.T) {
	resetDiscJobForTest()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "show.iso")
	if err := os.WriteFile(src, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "show - t02.mkv")
	withBrowsableRoot(t, tmp)

	orig := unpackDiscFn
	t.Cleanup(func() { unpackDiscFn = orig })
	var gotNames []string
	unpackDiscFn = func(ctx context.Context, path string, opts disc.Options) (*disc.Result, error) {
		gotNames = append([]string(nil), opts.OnlyNames...)
		if opts.OnProgress != nil {
			opts.OnProgress(1, 1)
		}
		if err := os.WriteFile(out, []byte("mkv"), 0o644); err != nil {
			return nil, err
		}
		return &disc.Result{
			Map:     &disc.Map{Volume: "SHOW", Source: path},
			Outputs: []string{out},
		}, nil
	}

	body, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{
		Path: src,
		Items: []apidto.OrganizeDiscUnpackItem{
			{Name: "t02", Conflict: "keep_both"},
		},
	})
	rr := httptest.NewRecorder()
	organizeDiscUnpackStartHandler(nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/discs/extract", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	var st apidto.OrganizeDiscUnpackStatus
	for time.Now().Before(deadline) {
		gr := httptest.NewRecorder()
		organizeDiscUnpackStatusHandler()(gr, httptest.NewRequest(http.MethodGet, "/api/organize/discs/extract?path="+src, nil))
		if err := json.Unmarshal(gr.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Status == "done" || st.Status == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Status != "done" {
		t.Fatalf("status = %+v", st)
	}
	if len(gotNames) != 1 || gotNames[0] != "t02" {
		t.Fatalf("OnlyNames = %v", gotNames)
	}
}
