package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// AnswerGuardPreferences keeps what each member chose about how strictly the
// answer guard reads what their agent says, and tells what applies once their
// organization's minimum is counted in. *guardpolicy.Service implements it.
type AnswerGuardPreferences interface {
	Preferences(ctx context.Context, organizationID, userID string) (guardpolicy.View, error)
	// SetPreferences changes what a member chose: a nil level leaves that
	// origin as it is, and a level that is "" clears it.
	SetPreferences(ctx context.Context, organizationID, userID string, owner, peer *guardpolicy.Level) (guardpolicy.View, error)
}

// keptGuardPreferences is settings, or nil when it is a nil pointer, map, slice
// or func in the interface, which would read as a service and panic when
// asked: the way one that was never built gets past a nil check.
func keptGuardPreferences(settings AnswerGuardPreferences) AnswerGuardPreferences {
	if settings == nil {
		return nil
	}
	switch value := reflect.ValueOf(settings); value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		if value.IsNil() {
			return nil
		}
	}
	return settings
}

// guardPreferenceHandlers serve a member's own answer guard settings:
//
//	GET /v1/organizations/{organizationID}/answer-guard/preferences
//	PUT /v1/organizations/{organizationID}/answer-guard/preferences
//	    {"owner": "secrets" | "strict" | null, "peer": "secrets" | "strict" | null}
//
// owner is for an answer the member started themselves, and peer for one
// another member reads. On a PUT a key that is left out leaves that setting as
// it is, and null clears it, back to its default; a key that is not one of
// those two, or a value that is not one of those three, is a 400, and nothing
// is changed.
//
// Both answer with what the member is told about their settings:
//
//	{"owner": {"chosen": "strict" | null, "default": "secrets", "floor": "secrets", "effective": "strict"},
//	 "peer":  {"chosen": null, "default": "strict", "floor": "strict", "effective": "strict"},
//	 "relevance": "hold" | "shadow" | "off", "regulated": false}
//
// chosen is what the member chose (null when nothing), floor is their
// organization's minimum, and effective is what applies: the stricter of floor
// and the choice, or of floor and default when there is none. A choice below
// the floor is accepted and kept, and the floor wins in effective, so the
// three differ. relevance is how the relevance check treats an answer, and
// regulated whether the organization runs the regulated preset.
//
// They are the member's own, as a preference is: who is calling is decided by
// member, which is Server.member, so a cookie needs an allowed origin to
// change anything, an agent's token is refused, and the caller must belong to
// the organization in the path. Only the organization and the user that member
// resolves are ever used; nothing in the body says whose settings they are.
type guardPreferenceHandlers struct {
	settings AnswerGuardPreferences
	member   func(http.ResponseWriter, *http.Request) (threads.Member, bool)
	logger   *slog.Logger
}

// newGuardPreferenceHandlers returns the handlers over settings, which may be
// nil when they are not kept here, and answer that they are not. member says
// who is calling; logger, which may be nil, is told of failures.
func newGuardPreferenceHandlers(settings AnswerGuardPreferences, member func(http.ResponseWriter, *http.Request) (threads.Member, bool), logger *slog.Logger) *guardPreferenceHandlers {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &guardPreferenceHandlers{settings: settings, member: member, logger: logger}
}

// maxGuardPreferencesBytes bounds a request to change the settings, which are
// two short words at most.
const maxGuardPreferencesBytes = 4 << 10

// guardOriginView is one origin's level as a member is told of it. Chosen is
// null when the member has not chosen.
type guardOriginView struct {
	Chosen    *guardpolicy.Level `json:"chosen"`
	Default   guardpolicy.Level  `json:"default"`
	Floor     guardpolicy.Level  `json:"floor"`
	Effective guardpolicy.Level  `json:"effective"`
}

// guardPreferencesView is what a member is told about their settings.
type guardPreferencesView struct {
	Owner     guardOriginView           `json:"owner"`
	Peer      guardOriginView           `json:"peer"`
	Relevance guardpolicy.RelevanceMode `json:"relevance"`
	Regulated bool                      `json:"regulated"`
}

func guardViewOf(view guardpolicy.View) guardPreferencesView {
	origin := func(from guardpolicy.OriginView) guardOriginView {
		shown := guardOriginView{Default: from.Default, Floor: from.Floor, Effective: from.Effective}
		if from.Chosen != "" {
			chosen := from.Chosen
			shown.Chosen = &chosen
		}
		return shown
	}
	return guardPreferencesView{Owner: origin(view.Owner), Peer: origin(view.Peer), Relevance: view.Relevance, Regulated: view.Regulated}
}

// get reads the calling member's own settings.
func (handlers *guardPreferenceHandlers) get(writer http.ResponseWriter, request *http.Request) {
	member, ok := handlers.member(writer, request)
	if !ok {
		return
	}
	if handlers.settings == nil {
		handlers.unavailable(writer)
		return
	}
	view, err := handlers.settings.Preferences(request.Context(), member.OrganizationID, member.UserID)
	if err != nil {
		handlers.fail(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, guardViewOf(view))
}

// put changes the calling member's own settings.
func (handlers *guardPreferenceHandlers) put(writer http.ResponseWriter, request *http.Request) {
	member, ok := handlers.member(writer, request)
	if !ok {
		return
	}
	if handlers.settings == nil {
		handlers.unavailable(writer)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxGuardPreferencesBytes+1))
	if err != nil || len(body) > maxGuardPreferencesBytes {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must be one small JSON object")
		return
	}
	owner, peer, problem := parseGuardChanges(body)
	if problem != "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", problem)
		return
	}
	view, err := handlers.settings.SetPreferences(request.Context(), member.OrganizationID, member.UserID, owner, peer)
	if err != nil {
		handlers.fail(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, guardViewOf(view))
}

// parseGuardChanges reads a request to change the settings: a JSON object
// holding "owner", "peer", or both. A key that is left out is nil, leaving
// that setting as it is; null is a pointer to "", clearing it; a level is a
// pointer to it. It says what is wrong, when something is.
func parseGuardChanges(body []byte) (owner, peer *guardpolicy.Level, problem string) {
	const notAnObject = "request body must be one JSON object"
	decoder := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	// A JSON null decodes to a nil map without an error, which is no object.
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, nil, notAnObject
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, notAnObject
	}
	for name := range fields {
		if name != "owner" && name != "peer" {
			return nil, nil, `request body may hold only "owner" and "peer"`
		}
	}
	if owner, problem = guardChange("owner", fields); problem != "" {
		return nil, nil, problem
	}
	if peer, problem = guardChange("peer", fields); problem != "" {
		return nil, nil, problem
	}
	return owner, peer, ""
}

func guardChange(name string, fields map[string]json.RawMessage) (*guardpolicy.Level, string) {
	raw, named := fields[name]
	if !named {
		return nil, ""
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		cleared := guardpolicy.Level("")
		return &cleared, ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if level, err := guardpolicy.ParseLevel(text); err == nil {
			return &level, ""
		}
	}
	return nil, name + ` must be "secrets", "strict" or null`
}

func (handlers *guardPreferenceHandlers) unavailable(writer http.ResponseWriter) {
	writeError(writer, http.StatusServiceUnavailable, "answer_guard_unavailable", "The answer guard's settings are not kept on this server.")
}

// fail answers a failed call: a setting the service refused is the caller's
// mistake, and anything else is ours, told to the caller as nothing more than
// that and to the log as what it was. Errors never carry tokens or
// conversation content, so they can be logged.
func (handlers *guardPreferenceHandlers) fail(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, guardpolicy.ErrInvalid) {
		writeError(writer, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), guardpolicy.ErrInvalid.Error()+": "))
		return
	}
	handlers.logger.ErrorContext(request.Context(), "agents request failed",
		"method", request.Method, "path", request.URL.Path, "error", err)
	writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
}
