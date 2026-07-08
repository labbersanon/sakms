package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doTestRequest(t *testing.T, status int, body string) *http.Request {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if body != "" {
			w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	return req
}

func TestDoJSON_Accepts2xxRange(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204} {
		body := `{"ok":true}`
		if status == 204 {
			body = "" // 204 No Content legitimately has no body
		}
		req := doTestRequest(t, status, body)
		var out map[string]any
		err := DoJSON(http.DefaultClient, req, MaxResponseBodySize, &out)
		if status == 204 {
			if err == nil {
				t.Errorf("status %d with empty body: expected a decode error from plain DoJSON (that's what DoJSONAllowEmpty is for)", status)
			}
			continue
		}
		if err != nil {
			t.Errorf("status %d: unexpected error: %v", status, err)
		}
	}
}

func TestDoJSON_RejectsNon2xx(t *testing.T) {
	for _, status := range []int{301, 400, 404, 500} {
		req := doTestRequest(t, status, `{}`)
		var out map[string]any
		if err := DoJSON(http.DefaultClient, req, MaxResponseBodySize, &out); err == nil {
			t.Errorf("status %d: expected an error", status)
		}
	}
}

func TestDoJSONAllowEmpty_ToleratesEmptyBodyOn2xx(t *testing.T) {
	for _, status := range []int{200, 204} {
		req := doTestRequest(t, status, "")
		var discard json.RawMessage
		if err := DoJSONAllowEmpty(http.DefaultClient, req, MaxResponseBodySize, &discard); err != nil {
			t.Errorf("status %d empty body: unexpected error: %v", status, err)
		}
	}
}

func TestDoJSONAllowEmpty_StillErrorsOnBadStatus(t *testing.T) {
	req := doTestRequest(t, 500, "")
	var discard json.RawMessage
	if err := DoJSONAllowEmpty(http.DefaultClient, req, MaxResponseBodySize, &discard); err == nil {
		t.Error("expected a genuine status-code error to still surface")
	}
}

func TestDoJSONAllowEmpty_StillErrorsOnMalformedNonEmptyBody(t *testing.T) {
	req := doTestRequest(t, 200, "not json but not empty either")
	var discard json.RawMessage
	if err := DoJSONAllowEmpty(http.DefaultClient, req, MaxResponseBodySize, &discard); err == nil {
		t.Error("expected a malformed (non-empty) body to still error")
	}
}
