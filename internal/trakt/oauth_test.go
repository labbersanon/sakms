package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, ClientID: "test-client-id", ClientSecret: "test-client-secret"}, srv.Client())
}

func TestRequestDeviceCode_SendsClientIDAndDecodesResponse(t *testing.T) {
	var gotBody map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device/code" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"device_code":"dc123","user_code":"ABCD1234","verification_url":"https://trakt.tv/activate","expires_in":600,"interval":5}`))
	})

	dc, err := c.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["client_id"] != "test-client-id" {
		t.Errorf("expected client_id in request body, got %+v", gotBody)
	}
	if dc.DeviceCode != "dc123" || dc.UserCode != "ABCD1234" || dc.VerificationURL != "https://trakt.tv/activate" || dc.ExpiresIn != 600 || dc.Interval != 5 {
		t.Errorf("unexpected device code: %+v", dc)
	}
}

func TestPollDeviceToken_Success(t *testing.T) {
	var gotBody map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device/token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":7776000,"created_at":1700000000,"token_type":"bearer","scope":"public"}`))
	})

	tok, err := c.PollDeviceToken(context.Background(), "dc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["code"] != "dc123" || gotBody["client_id"] != "test-client-id" || gotBody["client_secret"] != "test-client-secret" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Errorf("unexpected token: %+v", tok)
	}
	wantExpiry := time.Unix(1700000000, 0).Add(7776000 * time.Second).UTC()
	if !tok.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("expected ExpiresAt %v, got %v", wantExpiry, tok.ExpiresAt)
	}
}

func TestPollDeviceToken_StatusCodesMapToSentinelErrors(t *testing.T) {
	cases := []struct {
		status  int
		wantErr error
	}{
		{http.StatusBadRequest, ErrAuthorizationPending},
		{http.StatusNotFound, ErrDeviceCodeNotFound},
		{http.StatusConflict, ErrDeviceCodeUsed},
		{http.StatusGone, ErrDeviceCodeExpired},
		{418, ErrDeviceCodeDenied},
		{http.StatusTooManyRequests, ErrSlowDown},
	}
	for _, tc := range cases {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		})
		_, err := c.PollDeviceToken(context.Background(), "dc123")
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("status %d: expected %v, got %v", tc.status, tc.wantErr, err)
		}
	}
}

func TestPollUntilToken_PendingThenSuccess(t *testing.T) {
	attempts := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/device/token" {
			attempts++
			if attempts < 3 {
				w.WriteHeader(http.StatusBadRequest) // pending
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":100,"created_at":1700000000}`))
		}
	})

	dc := &DeviceCode{DeviceCode: "dc123", Interval: 0, ExpiresIn: 60} // Interval 0 -> clamped to 1s minimum
	tok, err := c.PollUntilToken(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 poll attempts, got %d", attempts)
	}
	if tok.AccessToken != "at" {
		t.Errorf("unexpected token: %+v", tok)
	}
}

func TestPollUntilToken_DeniedIsTerminal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
	})

	dc := &DeviceCode{DeviceCode: "dc123", Interval: 0, ExpiresIn: 60}
	_, err := c.PollUntilToken(context.Background(), dc)
	if !errors.Is(err, ErrDeviceCodeDenied) {
		t.Fatalf("expected ErrDeviceCodeDenied, got %v", err)
	}
}

func TestPollUntilToken_ContextCancelled(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // always pending
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dc := &DeviceCode{DeviceCode: "dc123", Interval: 0, ExpiresIn: 60}
	_, err := c.PollUntilToken(ctx, dc)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRefreshToken_SendsGrantTypeAndDecodesResponse(t *testing.T) {
	var gotBody map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-at","refresh_token":"new-rt","expires_in":7776000,"created_at":1700000000}`))
	})

	tok, err := c.RefreshToken(context.Background(), "old-rt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["grant_type"] != "refresh_token" || gotBody["refresh_token"] != "old-rt" || gotBody["redirect_uri"] != oobRedirectURI {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
	if tok.AccessToken != "new-at" || tok.RefreshToken != "new-rt" {
		t.Errorf("unexpected token: %+v", tok)
	}
}

func TestRefreshToken_NonOKStatusIsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.RefreshToken(context.Background(), "old-rt")
	if err == nil {
		t.Fatal("expected an error for a non-200 refresh response")
	}
}
