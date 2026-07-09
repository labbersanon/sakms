package nzbget

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppend_DownloadsContentAndSubmitsBase64(t *testing.T) {
	nzbContent := []byte("<nzb>fake nzb content</nzb>")

	var gotAuth string
	var gotParams []any
	mux := http.NewServeMux()
	mux.HandleFunc("/download.nzb", func(w http.ResponseWriter, r *http.Request) {
		w.Write(nzbContent)
	})
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		gotAuth = user + ":" + pass
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotParams = req.Params
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result": 42, "id": 1}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, Username: "wade", Password: "hunter2"}, srv.Client())

	id, err := c.Append(context.Background(), srv.URL+"/download.nzb", "some-release.nzb", "movies")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Errorf("expected NZBID 42, got %d", id)
	}
	if gotAuth != "wade:hunter2" {
		t.Errorf("expected basic auth to be forwarded, got %q", gotAuth)
	}
	if len(gotParams) < 3 {
		t.Fatalf("expected at least 3 params, got %d: %+v", len(gotParams), gotParams)
	}
	if gotParams[0] != "some-release.nzb" {
		t.Errorf("expected filename param, got %v", gotParams[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(gotParams[1].(string))
	if err != nil || string(decoded) != string(nzbContent) {
		t.Errorf("expected base64-encoded nzb content round trip, got %v (err %v)", gotParams[1], err)
	}
	if gotParams[2] != "movies" {
		t.Errorf("expected category param, got %v", gotParams[2])
	}
}

func TestAppend_BooleanFailureResultIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/download.nzb", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result": false, "id": 1}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL}, srv.Client())

	if _, err := c.Append(context.Background(), srv.URL+"/download.nzb", "x.nzb", ""); err == nil {
		t.Fatal("expected an error for a false append result")
	}
}

func TestAppend_RPCErrorPropagates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/download.nzb", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result": null, "error": {"message": "Invalid category"}, "id": 1}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL}, srv.Client())

	_, err := c.Append(context.Background(), srv.URL+"/download.nzb", "x.nzb", "bogus")
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestStatus_FindsActiveGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "listgroups":
			w.Write([]byte(`{"result": [{"NZBID": 42, "NZBName": "Some.Movie", "Status": "DOWNLOADING", "FileSizeMB": 100, "RemainingSizeMB": 25}], "id": 1}`))
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL}, srv.Client())

	status, err := c.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "DOWNLOADING" || status.Progress != 0.75 {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestStatus_FallsBackToHistory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "listgroups":
			w.Write([]byte(`{"result": [], "id": 1}`))
		case "history":
			w.Write([]byte(`{"result": [{"NZBID": 42, "Name": "Some.Movie", "Status": "SUCCESS/ALL"}], "id": 1}`))
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL}, srv.Client())

	status, err := c.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "SUCCESS/ALL" || status.Progress != 1 {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestStatus_NotFoundAnywhereIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result": [], "id": 1}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL}, srv.Client())

	if _, err := c.Status(context.Background(), 999); err == nil {
		t.Fatal("expected an error when the id isn't found anywhere")
	}
}
