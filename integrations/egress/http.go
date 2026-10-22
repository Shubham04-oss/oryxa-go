package egress

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

// HostHeader names the app host a connector's request is for; the path and
// query are the request's own (the broker-proxy contract the SDK and the
// gate's broker speak).
const HostHeader = "X-Oryxa-Egress-Host"

// ServeHTTP is the proxy as connectors reach it: the handle as a bearer
// token, the app's host in HostHeader, and the app's path as the request's.
// The answer is the app's, or a status the connector can act on: 401 for a
// bad or expired handle, 403 for a host the manifest doesn't allow, 413 for
// a body too large, 502 when the app could not be reached.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handle, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	host := strings.ToLower(strings.TrimSpace(r.Header.Get(HostHeader)))
	if !ok || handle == "" || host == "" || strings.ContainsAny(host, "/@?#") {
		http.Error(w, "egress: a handle and an app host are required", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "egress: read the request", http.StatusBadRequest)
		return
	}
	header := r.Header.Clone()
	header.Del(HostHeader)
	response, err := p.Do(r.Context(), handle, Request{
		Method: r.Method, URL: "https://" + host + r.URL.RequestURI(), Header: header, Body: body,
	})
	if err != nil {
		// The errors name the host or URL at most, never the handle or a token.
		http.Error(w, err.Error(), statusOf(err))
		return
	}
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrHandle), errors.Is(err, ErrExpired):
		return http.StatusUnauthorized
	case errors.Is(err, ErrHost):
		return http.StatusForbidden
	case errors.Is(err, ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadGateway
	}
}
