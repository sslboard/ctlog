package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Certificates fetches a page from the recent certificate index.
func (c *Client) Certificates(ctx context.Context, domain, after string, exact bool) (map[string]json.RawMessage, error) {
	endpoint, err := c.url("/api/v1/certificates")
	if err != nil {
		return nil, err
	}
	q := url.Values{"q": {domain}, "include_subdomains": {strconv.FormatBool(!exact)}}
	if after != "" {
		q.Set("after", after)
	}
	body, err := c.send(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	var certs []json.RawMessage
	if err := json.Unmarshal(result["certificates"], &certs); err != nil || certs == nil {
		return nil, fmt.Errorf("invalid certificate response")
	}
	return result, nil
}
