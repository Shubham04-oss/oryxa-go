package pipedream

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// restrictedProxyHeaders are the headers Pipedream's proxy refuses with a 400
// (https://pipedream.com/docs/connect/api-proxy#restricted-headers), so they
// are dropped rather than forwarded. So is Authorization: Pipedream inserts
// the account's own credential, and nothing a caller holds may reach the app.
var restrictedProxyHeaders = map[string]bool{
	"Authorization": true, "Accept-Encoding": true, "Access-Control-Request-Headers": true,
	"Access-Control-Request-Method": true, "Connection": true, "Content-Length": true, "Cookie": true,
	"Date": true, "Dnt": true, "Expect": true, "Host": true, "Keep-Alive": true, "Origin": true,
	"Permissions-Policy": true, "Referer": true, "Te": true, "Trailer": true, "Transfer-Encoding": true,
	"Upgrade": true, "Via": true,
}

// Proxy sends request to an app's API as externalUserID's account accountID,
// through Pipedream's Connect proxy, which inserts the account's credential
// (https://pipedream.com/docs/connect/api-proxy). request.URL is either the
// full URL, on one of the app's allowed domains, or a path, which an app with
// an account-specific domain requires. Its headers are forwarded as
// x-pd-proxy-*, the only ones the proxy passes on.
//
// The answer is the proxy's, with its body read into memory (up to the usual
// cap) so the call's timeout cannot cut it off. ponytail: the docs say the
// proxy returns the app's response but do not say how to tell the app's
// status from the proxy's own (a 429 or 401 could be either), so any status
// is handed back as it came; only a proxy that did not answer is an error.
func (client *Client) Proxy(ctx context.Context, externalUserID, accountID string, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("%w: no request to proxy", ErrInvalid)
	}
	token, err := client.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var body io.Reader
	if request.Body != nil {
		defer request.Body.Close()
		body = request.Body
	}
	endpoint := client.baseURL + client.project("/proxy/"+base64.RawURLEncoding.EncodeToString([]byte(request.URL.String()))) +
		"?" + url.Values{"external_user_id": {externalUserID}, "account_id": {accountID}}.Encode()
	outbound, err := http.NewRequestWithContext(ctx, request.Method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("pipedream: build the proxy request: %w", err)
	}
	outbound.ContentLength = request.ContentLength
	for name, values := range request.Header {
		name = http.CanonicalHeaderKey(name)
		if restrictedProxyHeaders[name] || strings.HasPrefix(name, "Proxy-") || strings.HasPrefix(name, "Sec-") ||
			strings.HasPrefix(name, "X-Pd-") {
			continue
		}
		for _, value := range values {
			outbound.Header.Add("x-pd-proxy-"+name, value)
		}
	}
	// ponytail: the docs do not say whether the body's Content-Type is read
	// from the proxy request itself or from x-pd-proxy-content-type, so it
	// is sent as both.
	if contentType := request.Header.Get("Content-Type"); contentType != "" {
		outbound.Header.Set("Content-Type", contentType)
	}
	outbound.Header.Set("Authorization", "Bearer "+token)
	outbound.Header.Set("x-pd-environment", client.environment)

	response, err := client.http.Do(outbound)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: the proxy did not answer", ErrUnavailable)
	}
	defer response.Body.Close()
	payload, err := readCapped(response.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: the proxy: %v", ErrUnavailable, err)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	return response, nil
}
