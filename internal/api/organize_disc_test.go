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

func TestOrganizeDiscUnpack_RejectsNonDisc(t *testing.T) {
	resetDiscJobForTest()
	tmp := t.TempDir()
	mkv := filepath.Join(tmp, "a.mkv")
	if err := os.WriteFile(mkv, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)

	body, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{Path: mkv})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/browse/unpack-disc", bytes.NewReader(body))
	organizeDiscUnpackStartHandler(nil)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
}

func TestOrganizeDiscUnpack_StartsAndCompletes(t *testing.T) {
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
	unpackDiscFn = func(ctx context.Context, path string, opts disc.Options) (*disc.Result, error) {
		if opts.OnProgress != nil {
			opts.OnProgress(1, 1)
		}
		if err := os.WriteFile(out, []byte("mkv"), 0o644); err != nil {
			return nil, err
		}
		_ = os.Remove(path)
		return &disc.Result{
			Map:           &disc.Map{Volume: "SHOW", Source: path},
			Outputs:       []string{out},
			DeletedSource: true,
		}, nil
	}

	body, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{Path: src})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/browse/unpack-disc", bytes.NewReader(body))
	organizeDiscUnpackStartHandler(nil)(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body %s", rr.Code, rr.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	var st apidto.OrganizeDiscUnpackStatus
	for time.Now().Before(deadline) {
		gr := httptest.NewRecorder()
		organizeDiscUnpackStatusHandler()(gr, httptest.NewRequest(http.MethodGet, "/api/organize/browse/unpack-disc?path="+src, nil))
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
	if !st.DeletedSource || len(st.Outputs) != 1 {
		t.Fatalf("result = %+v", st)
	}
}

func TestOrganizeDiscUnpack_ConflictWhenBusy(t *testing.T) {
	resetDiscJobForTest()
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.iso")
	b := filepath.Join(tmp, "b.iso")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)

	block := make(chan struct{})
	orig := unpackDiscFn
	t.Cleanup(func() {
		close(block)
		unpackDiscFn = orig
		resetDiscJobForTest()
	})
	unpackDiscFn = func(ctx context.Context, path string, opts disc.Options) (*disc.Result, error) {
		<-block
		return &disc.Result{Map: &disc.Map{Volume: "A"}}, nil
	}

	body, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{Path: a})
	rr := httptest.NewRecorder()
	organizeDiscUnpackStartHandler(nil)(rr, httptest.NewRequest(http.MethodPost, "/api/organize/browse/unpack-disc", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first start = %d", rr.Code)
	}

	body2, _ := json.Marshal(apidto.OrganizeDiscUnpackRequest{Path: b})
	rr2 := httptest.NewRecorder()
	organizeDiscUnpackStartHandler(nil)(rr2, httptest.NewRequest(http.MethodPost, "/api/organize/browse/unpack-disc", bytes.NewReader(body2)))
	if rr2.Code != http.StatusConflict {
		t.Fatalf("second start = %d body %s", rr2.Code, rr2.Body.String())
	}
}
