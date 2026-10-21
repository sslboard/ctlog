package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMeSendsBearerKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != "Bearer sslb_live_k" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"Invalid or revoked API key"}`)
			return
		}
		io.WriteString(w, `{"email":"a@example.com","plan":{"tier":"pro","status":"active","current_period_end":"2026-11-20T00:00:00Z","cancel_at_period_end":true},"limits":{"hostnames_per_request":10000,"paging":true,"requests":"60 per minute per key"},"key":{"name":"ci","prefix":"sslb_live_k"},"usage":{"searches_today":1,"searches_30d":2,"hostnames_30d":3}}`)
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("Me without a key should fail")
	} else {
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Message != "Invalid or revoked API key" || apiErr.Code != "" {
			t.Errorf("err = %#v", err)
		}
	}

	c.APIKey = "sslb_live_k"
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.Email != "a@example.com" || me.Plan.Tier != "pro" || !me.Plan.CancelAtPeriodEnd ||
		me.Limits.HostnamesPerRequest != 10000 || me.Usage.Hostnames30d != 3 || len(me.Raw) == 0 {
		t.Errorf("me = %+v", me)
	}
}

func TestLookupSendsPagingParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("after") != "a.example.com" || q.Get("limit") != "500" {
			t.Errorf("query = %v", q)
		}
		io.WriteString(w, `{"hostnames":["b.example.com"],"tier":"pro","next":"b.example.com","upgrade_url":null}`)
	}))
	defer srv.Close()

	res, err := newClient(srv.URL).Lookup(context.Background(), "example.com", Options{After: "a.example.com", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != "pro" || res.Next != "b.example.com" || res.UpgradeURL != "" {
		t.Errorf("res = %+v", res)
	}
}

func TestDeviceLoginCalls(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s content-type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		switch r.URL.Path {
		case "/api/v1/device/code":
			if body["client_name"] != "box" {
				t.Errorf("client_name = %q", body["client_name"])
			}
			io.WriteString(w, `{"device_code":"d","user_code":"BCDF-GHJK","verification_uri":"https://x/device/","expires_in":600,"interval":5}`)
		case "/api/v1/device/token":
			polls++
			if polls == 1 {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"authorization_pending","error_description":"Waiting for you."}`)
				return
			}
			io.WriteString(w, `{"api_key":"sslb_live_x","email":"a@example.com","plan":{"tier":"free","status":null}}`)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL)

	code, err := c.StartDeviceLogin(context.Background(), "box")
	if err != nil || code.DeviceCode != "d" || code.UserCode != "BCDF-GHJK" || code.Interval != 5 {
		t.Fatalf("code = %+v, %v", code, err)
	}

	_, err = c.PollDeviceToken(context.Background(), "d")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != DeviceAuthorizationPending || apiErr.Message != "Waiting for you." {
		t.Fatalf("pending err = %#v", err)
	}

	token, err := c.PollDeviceToken(context.Background(), "d")
	if err != nil || token.APIKey != "sslb_live_x" || token.Plan.Tier != "free" {
		t.Errorf("token = %+v, %v", token, err)
	}
}

func TestAccountCallsRejectBadBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	if _, err := c.StartDeviceLogin(context.Background(), "box"); err == nil {
		t.Error("StartDeviceLogin accepted a body without a device code")
	}
	if _, err := c.PollDeviceToken(context.Background(), "d"); err == nil {
		t.Error("PollDeviceToken accepted a body without a key")
	}

	bad := &Client{BaseURL: "ftp://x", UserAgent: "t"}
	if _, err := bad.Me(context.Background()); err == nil {
		t.Error("Me accepted an invalid base URL")
	}
	if _, err := bad.StartDeviceLogin(context.Background(), ""); err == nil {
		t.Error("StartDeviceLogin accepted an invalid base URL")
	}
	if _, err := bad.PollDeviceToken(context.Background(), ""); err == nil {
		t.Error("PollDeviceToken accepted an invalid base URL")
	}
}
