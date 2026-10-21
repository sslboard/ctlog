package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// Plan is the hostname API plan of an account.
type Plan struct {
	// Tier is "free" or "pro".
	Tier string `json:"tier"`
	// Status is the Stripe subscription status, empty if there never was one.
	Status            string `json:"status"`
	CurrentPeriodEnd  string `json:"current_period_end"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
}

// Me is the account behind an API key (GET /api/v1/me).
type Me struct {
	Email  string `json:"email"`
	Plan   Plan   `json:"plan"`
	Limits struct {
		HostnamesPerRequest int    `json:"hostnames_per_request"`
		Paging              bool   `json:"paging"`
		Requests            string `json:"requests"`
	} `json:"limits"`
	Key struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Prefix     string `json:"prefix"`
		CreatedAt  string `json:"created_at"`
		LastUsedAt string `json:"last_used_at"`
	} `json:"key"`
	Usage struct {
		SearchesToday int64 `json:"searches_today"`
		Searches30d   int64 `json:"searches_30d"`
		Hostnames30d  int64 `json:"hostnames_30d"`
	} `json:"usage"`
	AccountURL string `json:"account_url"`
	UpgradeURL string `json:"upgrade_url"`

	// Raw is the response body exactly as received.
	Raw []byte `json:"-"`
}

// Me returns the account for c.APIKey.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	endpoint, err := c.url("/api/v1/me")
	if err != nil {
		return nil, err
	}
	body, err := c.send(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var me Me
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, errors.New("unexpected response from API (not JSON)")
	}
	me.Raw = body
	return &me, nil
}

// DeviceCode starts a device login (RFC 8628 style).
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceToken is the result of an approved device login.
type DeviceToken struct {
	APIKey string `json:"api_key"`
	Email  string `json:"email"`
	Plan   Plan   `json:"plan"`
}

// Device login error codes, in Error.Code.
const (
	DeviceAuthorizationPending = "authorization_pending"
	DeviceSlowDown             = "slow_down"
	DeviceAccessDenied         = "access_denied"
	DeviceExpiredToken         = "expired_token"
)

// StartDeviceLogin asks for a device and user code pair. clientName labels
// the key on the account page (the machine's hostname).
func (c *Client) StartDeviceLogin(ctx context.Context, clientName string) (*DeviceCode, error) {
	endpoint, err := c.url("/api/v1/device/code")
	if err != nil {
		return nil, err
	}
	body, err := c.send(ctx, http.MethodPost, endpoint, map[string]string{"client_name": clientName})
	if err != nil {
		return nil, err
	}
	var code DeviceCode
	if err := json.Unmarshal(body, &code); err != nil || code.DeviceCode == "" {
		return nil, errors.New("unexpected response from API (no device code)")
	}
	return &code, nil
}

// PollDeviceToken asks whether the login was approved. Pending, slow-down,
// denied and expired answers are *Error with Code set.
func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string) (*DeviceToken, error) {
	endpoint, err := c.url("/api/v1/device/token")
	if err != nil {
		return nil, err
	}
	body, err := c.send(ctx, http.MethodPost, endpoint, map[string]string{"device_code": deviceCode})
	if err != nil {
		return nil, err
	}
	var token DeviceToken
	if err := json.Unmarshal(body, &token); err != nil || token.APIKey == "" {
		return nil, errors.New("unexpected response from API (no API key)")
	}
	return &token, nil
}
