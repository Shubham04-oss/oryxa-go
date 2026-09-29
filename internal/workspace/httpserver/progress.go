package httpserver

import (
	"net/http"
)

// Progress follows successful service writes, including a write that succeeds
// before a later response or operation fails. Bodies and tokens are excluded.
type progressTransport struct {
	base      http.RoundTripper
	completed func(*http.Request) error
}

func (transport progressTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	switch request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		if err := transport.completed(request); err != nil {
			response.Body.Close()
			return nil, err
		}
	}
	return response, nil
}
