// Package httpserver serves realtime's one route: the web app's Worker saying
// what happened in the conversations it keeps, POST /v1/realtime/events.
//
// Every call is signed with the secret the purge is signed with, but over a
// string of its own, so that a signature made for one direction never
// verifies in the other:
//
//	POST /v1/realtime/events
//	Content-Type: application/json
//	X-Oryxa-Timestamp: <Unix seconds>
//	X-Oryxa-Signature: v1=<lowercase hex HMAC-SHA256(secret, "events." + timestamp + "." + body)>
//
//	{"events":[{"id":"…","type":"realtime.message.posted.v1","detail":{…}},…]}
//
// A purge's string starts with its timestamp, a digit, and this one with a
// letter, so neither is ever the other's. The size and the signature's
// headers are checked before the body is read, and the signature over the
// body before anything reads it. The events are then checked, all of them,
// and recorded in realtime's outbox in one transaction, and the answer is 204.
package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/realtime/internal/postgres"
)

const (
	// Path is where the Worker posts its events.
	Path = "/v1/realtime/events"
	// TimestampHeader carries when the call was signed, in Unix seconds, as
	// the Worker wrote it: it is signed exactly as it arrives.
	TimestampHeader = "X-Oryxa-Timestamp"
	// SignatureHeader carries Sign's answer.
	SignatureHeader = "X-Oryxa-Signature"
	// MaxBodyBytes is the largest body taken. A larger one is answered 413.
	MaxBodyBytes = 256 << 10
	// MaxEvents is the most events one call may carry. More is answered 413.
	MaxEvents = 100
	// Window is how far a call's timestamp may be from this server's clock,
	// either way, as for the purge.
	Window = 300 * time.Second
	// signaturePrefix names the direction a signature is for. The purge's
	// has none, and starts with its timestamp.
	signaturePrefix  = "events."
	signatureVersion = "v1="
)

// Sign is the X-Oryxa-Signature value for body sent at timestamp: v1= and the
// lowercase hex HMAC-SHA256, keyed with the secret's characters as written,
// of "events.", the timestamp in decimal, a dot, and the body exactly as
// sent.
func Sign(secret string, timestamp int64, body []byte) string {
	return signatureVersion + hex.EncodeToString(mac([]byte(secret), strconv.FormatInt(timestamp, 10), body))
}

func mac(secret []byte, timestamp string, body []byte) []byte {
	hash := hmac.New(sha256.New, secret)
	hash.Write([]byte(signaturePrefix))
	hash.Write([]byte(timestamp))
	hash.Write([]byte{'.'})
	hash.Write(body)
	return hash.Sum(nil)
}

// Store records what the Worker sent. *postgres.Store is one.
type Store interface {
	Accept(context.Context, []postgres.Received) (int, error)
}

// Config is what the route needs.
type Config struct {
	// Secret is REALTIME_PURGE_SECRET, trimmed, which the Worker signs its
	// calls with.
	Secret string
	// Now is the clock a call's timestamp and events' times are judged by.
	// Nil is time.Now.
	Now func() time.Time
}

// Server is realtime's HTTP API: the Worker's route alone.
type Server struct {
	secret   []byte
	store    Store
	now      func() time.Time
	logger   *slog.Logger
	mux      *http.ServeMux
	patterns []string
}

// New returns the server, recording what it accepts in store.
func New(config Config, store Store, logger *slog.Logger) (*Server, error) {
	if strings.TrimSpace(config.Secret) == "" {
		return nil, errors.New("realtime's route needs the secret the Worker signs with")
	}
	if store == nil {
		return nil, errors.New("realtime's route needs somewhere to record what the Worker sends")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{secret: []byte(strings.TrimSpace(config.Secret)), store: store, now: config.Now, logger: logger, mux: http.NewServeMux()}
	if server.now == nil {
		server.now = time.Now
	}
	server.handle("POST "+Path, server.receive)
	return server, nil
}

func (server *Server) handle(pattern string, handler http.HandlerFunc) {
	server.mux.Handle(pattern, handler)
	server.patterns = append(server.patterns, pattern)
}

// Patterns lists every route the server registered.
func (server *Server) Patterns() []string { return slices.Clone(server.patterns) }

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "no-store")
	server.mux.ServeHTTP(writer, request)
}

// receive takes a batch of the Worker's events. It answers 204 once every
// event is recorded, or was already, and otherwise says why not: 413 for too
// much, 401 for a call the Worker did not sign now, 400 for any event realtime
// cannot publish, with nothing recorded, and 5xx or 408 for what may work if
// sent again.
func (server *Server) receive(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	if request.ContentLength > MaxBodyBytes {
		server.refuse(ctx, writer, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is over %d KiB", MaxBodyBytes>>10))
		return
	}
	timestamp, signature, err := server.signature(request.Header)
	if err != nil {
		server.refuse(ctx, writer, http.StatusUnauthorized, err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		server.refuse(ctx, writer, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is over %d KiB", MaxBodyBytes>>10))
		return
	case err != nil:
		// Not the Worker's batch being wrong, so worth sending again.
		server.refuse(ctx, writer, http.StatusRequestTimeout, "the body did not arrive whole")
		return
	}
	if !hmac.Equal(signature, mac(server.secret, timestamp, body)) {
		server.refuse(ctx, writer, http.StatusUnauthorized, "the signature does not match the body: the secret is not realtime's, "+
			"or the call was not signed over \"events.\", its timestamp, a dot and the body as sent")
		return
	}
	events, err := decode(body, server.now())
	if errors.Is(err, errTooMany) {
		server.refuse(ctx, writer, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		server.refuse(ctx, writer, http.StatusBadRequest, err.Error())
		return
	}
	recorded, err := server.store.Accept(ctx, events)
	if err != nil {
		server.logger.ErrorContext(ctx, "the Worker's events could not be recorded; it will send them again", "events", len(events), "error", err)
		writeError(writer, http.StatusInternalServerError, "internal", "the events could not be recorded")
		return
	}
	server.logger.InfoContext(ctx, "realtime events accepted", "events", len(events), "recorded", recorded)
	writer.WriteHeader(http.StatusNoContent)
}

// signature reads a call's timestamp, as written, and its signature,
// refusing a call without exactly one of each, or signed outside the window.
func (server *Server) signature(header http.Header) (string, []byte, error) {
	timestamps, signatures := header.Values(TimestampHeader), header.Values(SignatureHeader)
	if len(timestamps) != 1 || len(signatures) != 1 {
		return "", nil, errors.New("the call is not signed: it needs one " + TimestampHeader + " and one " + SignatureHeader)
	}
	timestamp := timestamps[0]
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strings.Trim(timestamp, "0123456789") != "" {
		return "", nil, errors.New(TimestampHeader + " must be the time the call was signed, in Unix seconds")
	}
	if age := server.now().Sub(time.Unix(seconds, 0)); age > Window || age < -Window {
		return "", nil, fmt.Errorf("%s is more than %d seconds from this server's clock", TimestampHeader, int(Window.Seconds()))
	}
	hexed, ok := strings.CutPrefix(signatures[0], signatureVersion)
	signature, err := hex.DecodeString(hexed)
	if !ok || err != nil || len(signature) != sha256.Size {
		return "", nil, errors.New(SignatureHeader + " must be v1= and the 64 hex characters of an HMAC-SHA256")
	}
	return timestamp, signature, nil
}

// refuse answers a call realtime will not take, and says why in the log: the
// host's own line for the request has its status but not its reason.
func (server *Server) refuse(ctx context.Context, writer http.ResponseWriter, status int, reason string) {
	server.logger.WarnContext(ctx, "the Worker's events refused", "status", status, "reason", reason)
	code := "invalid_request"
	switch status {
	case http.StatusUnauthorized:
		code = "unauthorized"
	case http.StatusRequestTimeout:
		code = "request_timeout"
	}
	writeError(writer, status, code, reason)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]map[string]string{"error": {"code": code, "message": message}})
}
