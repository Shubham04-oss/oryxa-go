package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// mounted is a service the host has assembled, under the name it was
// selected by.
type mounted struct {
	name    string
	service Service
}

// routes builds the process's handler: /healthz for the process itself, and
// every path each service serves, behind that service's deadlines and
// instrumentation.
//
// A path belongs to exactly one service. Two services claiming paths that
// match the same request is a startup error rather than a silent precedence
// rule, because whichever lost would stop being reachable.
func routes(services []mounted) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	owners := map[string]string{"/healthz": "the host"}
	for _, entry := range services {
		if entry.service.Handler == nil {
			if len(entry.service.Routes) != 0 {
				return nil, fmt.Errorf("service %s lists routes but has no handler", entry.name)
			}
			continue
		}
		handler := withDeadlines(entry.service.Timeouts,
			observability.HTTPHandler("oryxa-"+entry.name, withCleanRequest(entry.service.Handler)))
		claimed := map[string]bool{}
		for _, route := range entry.service.Routes {
			path, err := routePath(route)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", entry.name, err)
			}
			// Every service answers its own /healthz; the process answers for
			// all of them.
			if path == "/healthz" || claimed[path] {
				continue
			}
			if err := register(mux, path, handler); err != nil {
				return nil, fmt.Errorf("service %s cannot serve %s: it is already served by %s", entry.name, path, owner(owners, path))
			}
			claimed[path] = true
			owners[path] = entry.name
		}
	}
	return mux, nil
}

// routePath is the path of a ServeMux pattern, without the method that
// narrowed it. A host-qualified pattern is refused: the process serves one
// origin.
func routePath(pattern string) (string, error) {
	path := strings.TrimSpace(pattern)
	if method, rest, found := strings.Cut(path, " "); found {
		if method == "" || strings.ToUpper(method) != method {
			return "", fmt.Errorf("route %q does not start with a method", pattern)
		}
		path = strings.TrimSpace(rest)
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("route %q must be a path, not bound to a host", pattern)
	}
	return path, nil
}

// register adds a pattern, turning ServeMux's panic over a conflict into an
// error.
func register(mux *http.ServeMux, pattern string, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	mux.Handle(pattern, handler)
	return nil
}

// owner names whichever service already serves a path that conflicts with
// path. It is only asked after a conflict, so testing each pairing is cheap
// enough.
func owner(owners map[string]string, path string) string {
	if name, ok := owners[path]; ok {
		return name
	}
	for existing, name := range owners {
		probe := http.NewServeMux()
		probe.Handle(existing, http.NotFoundHandler())
		if register(probe, path, http.NotFoundHandler()) != nil {
			return fmt.Sprintf("%s (as %s)", name, existing)
		}
	}
	return "another service"
}

// withCleanRequest refuses a request whose path or query holds a NUL byte or
// bytes that are not UTF-8, before a service sees it. No client of ours sends
// either, and PostgreSQL will not store them, so every service that put an id
// or a search term into a query would otherwise answer 500, one route at a
// time. It also covers the organization id in the path, which every service
// forwards to auth. It sits inside the instrumentation, so a refusal is still
// counted and logged as the service's own.
func withCleanRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !cleanURL(request.URL) {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("X-Content-Type-Options", "nosniff")
			writer.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(writer).Encode(map[string]string{"error": "the request path or query is invalid: " + cleantext.ErrUnclean.Error()})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// cleanURL reports whether the decoded path, and every decoded query name and
// value, is text the database will take.
func cleanURL(target *url.URL) bool {
	if !cleantext.Valid(target.Path) {
		return false
	}
	for name, values := range target.Query() {
		if !cleantext.Valid(name) {
			return false
		}
		for _, value := range values {
			if !cleantext.Valid(value) {
				return false
			}
		}
	}
	return true
}

// withDeadlines holds one request to the timeouts its service chose. The
// process's server sets none of its own, so a service that streams is not cut
// off by a neighbour's deadline, and every request sets both sides afresh
// rather than inheriting whatever the previous request on the connection left.
func withDeadlines(timeouts Timeouts, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		controller := http.NewResponseController(writer)
		now := time.Now()
		if err := controller.SetReadDeadline(deadline(now, timeouts.Read)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if err := controller.SetWriteDeadline(deadline(now, timeouts.Write)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func deadline(now time.Time, timeout time.Duration) time.Time {
	if timeout <= 0 {
		return time.Time{}
	}
	return now.Add(timeout)
}
