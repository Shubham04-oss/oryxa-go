// Package stubauth stands in for the auth service during chaos runs.
//
// Every non-auth service asks auth one question, "who is this token in this
// organization?", so a stub that answers it is enough to run them all. Unlike
// test/internal/authstub, whose five identities are fixed, this one is driven
// while the run is live: the harness registers users, puts them in and out of
// organizations, and makes auth misbehave, to see how the callers cope with a
// dependency that is slow, wrong, or gone. It must never be deployed.
package stubauth

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// User is one person a token stands for.
type User struct {
	ID          string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// Fault is how the stub is currently misbehaving. The zero value is none.
type Fault struct {
	// Mode is one of: "", "status", "garbage", "empty-object", "no-role",
	// "unknown-role", "empty-user", "huge", "hang", "reset", "slow-drip".
	Mode string `json:"mode"`
	// Status is the code Mode "status" answers with.
	Status int `json:"status"`
	// DelayMS is added before any answer, faulty or not.
	DelayMS int `json:"delay_ms"`
	// Percent limits the fault to that share of requests, 0 meaning all.
	Percent int `json:"percent"`
}

// Stub is the running auth stand-in.
type Stub struct {
	mu      sync.RWMutex
	tokens  map[string]User
	members map[string]map[string]string // organization -> user id -> role
	names   map[string]string            // organization -> name
	fault   Fault

	lookups   atomic.Int64
	rejected  atomic.Int64
	faulted   atomic.Int64
	sequence  atomic.Int64
	mux       *http.ServeMux
	byUserRef map[string]User
}

// New returns an empty stub.
func New() *Stub {
	stub := &Stub{
		tokens:    map[string]User{},
		members:   map[string]map[string]string{},
		names:     map[string]string{},
		byUserRef: map[string]User{},
		mux:       http.NewServeMux(),
	}
	stub.mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	stub.mux.HandleFunc("GET /v1/organizations/{organizationID}", stub.organization)
	stub.mux.HandleFunc("GET /v1/organizations/{organizationID}/members", stub.listMembers)

	stub.mux.HandleFunc("POST /_stub/users", stub.registerUser)
	stub.mux.HandleFunc("PUT /_stub/organizations/{organizationID}/members/{userID}", stub.setMember)
	stub.mux.HandleFunc("DELETE /_stub/organizations/{organizationID}/members/{userID}", stub.removeMember)
	stub.mux.HandleFunc("PUT /_stub/fault", stub.setFault)
	stub.mux.HandleFunc("GET /_stub/stats", stub.stats)
	return stub
}

// ServeHTTP implements http.Handler.
func (stub *Stub) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	stub.mux.ServeHTTP(writer, request)
}

// Register makes token stand for user, for callers in the same process.
func (stub *Stub) Register(token string, user User) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.tokens[token] = user
	stub.byUserRef[user.ID] = user
}

// Join puts a user in an organization with a role.
func (stub *Stub) Join(organizationID, userID, role string) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.members[organizationID] == nil {
		stub.members[organizationID] = map[string]string{}
	}
	stub.members[organizationID][userID] = role
}

// SetFault changes how the stub misbehaves.
func (stub *Stub) SetFault(fault Fault) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.fault = fault
}

func (stub *Stub) organization(writer http.ResponseWriter, request *http.Request) {
	if !stub.misbehave(writer, request, true) {
		return
	}
	user, ok := stub.caller(writer, request)
	if !ok {
		return
	}
	organizationID := request.PathValue("organizationID")
	stub.mu.RLock()
	role, member := stub.members[organizationID][user.ID]
	name := stub.names[organizationID]
	stub.mu.RUnlock()
	if !member {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "resource not found"})
		return
	}
	if name == "" {
		name = "Chaos Org " + organizationID
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"organization": map[string]string{"id": organizationID, "name": name},
		"membership": map[string]string{
			"organization_id": organizationID,
			"user_id":         user.ID,
			"email":           user.Email,
			"display_name":    user.DisplayName,
			"role":            role,
		},
	})
}

func (stub *Stub) listMembers(writer http.ResponseWriter, request *http.Request) {
	if !stub.misbehave(writer, request, false) {
		return
	}
	user, ok := stub.caller(writer, request)
	if !ok {
		return
	}
	organizationID := request.PathValue("organizationID")
	stub.mu.RLock()
	roles, member := stub.members[organizationID]
	_, inOrg := roles[user.ID]
	listed := make([]map[string]string, 0, len(roles))
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		person := stub.byUserRef[id]
		listed = append(listed, map[string]string{
			"user_id": id, "email": person.Email, "display_name": person.DisplayName, "role": roles[id],
		})
	}
	stub.mu.RUnlock()
	if !member || !inOrg {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "resource not found"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"members": listed})
}

// caller resolves the request's bearer token or session cookie to a user.
func (stub *Stub) caller(writer http.ResponseWriter, request *http.Request) (User, bool) {
	stub.lookups.Add(1)
	token := ""
	if parts := strings.Fields(request.Header.Get("Authorization")); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		token = parts[1]
	} else if cookie, err := request.Cookie("oryxa_access"); err == nil {
		token = cookie.Value
	}
	stub.mu.RLock()
	user, ok := stub.tokens[token]
	stub.mu.RUnlock()
	if token == "" || !ok {
		stub.rejected.Add(1)
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
		return User{}, false
	}
	return user, true
}

// misbehave applies the configured fault. It reports whether the normal
// answer should still be written.
func (stub *Stub) misbehave(writer http.ResponseWriter, request *http.Request, membership bool) bool {
	stub.mu.RLock()
	fault := stub.fault
	stub.mu.RUnlock()
	if fault.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(fault.DelayMS) * time.Millisecond):
		case <-request.Context().Done():
			return false
		}
	}
	if fault.Mode == "" {
		return true
	}
	if fault.Percent > 0 && int(stub.sequence.Add(1)%100) >= fault.Percent {
		return true
	}
	stub.faulted.Add(1)
	switch fault.Mode {
	case "status":
		status := fault.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		writeJSON(writer, status, map[string]string{"error": "injected fault"})
	case "garbage":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("<html>not json \x00\xff\xfe</html>"))
	case "empty-object":
		writeJSON(writer, http.StatusOK, map[string]any{})
	case "no-role":
		writeJSON(writer, http.StatusOK, map[string]any{
			"organization": map[string]string{"id": request.PathValue("organizationID"), "name": "x"},
			"membership":   map[string]string{"user_id": "user-injected", "email": "i@x.test"},
		})
	case "unknown-role":
		writeJSON(writer, http.StatusOK, map[string]any{
			"organization": map[string]string{"id": request.PathValue("organizationID"), "name": "x"},
			"membership": map[string]string{
				"organization_id": request.PathValue("organizationID"), "user_id": "user-injected",
				"email": "i@x.test", "display_name": "Injected", "role": "superadmin",
			},
		})
	case "empty-user":
		writeJSON(writer, http.StatusOK, map[string]any{
			"organization": map[string]string{"id": request.PathValue("organizationID"), "name": "x"},
			"membership": map[string]string{
				"organization_id": request.PathValue("organizationID"), "user_id": "",
				"email": "", "display_name": "", "role": "owner",
			},
		})
	case "huge":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"organization":{"id":"x","name":"`))
		chunk := []byte(strings.Repeat("A", 64<<10))
		for range 200 { // ~12 MiB, far past any reader's cap
			if _, err := writer.Write(chunk); err != nil {
				return false
			}
		}
		_, _ = writer.Write([]byte(`"}}`))
	case "hang":
		<-request.Context().Done()
	case "reset":
		if hijacker, ok := writer.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	case "slow-drip":
		writer.Header().Set("Content-Type", "application/json")
		flusher, _ := writer.(http.Flusher)
		for _, piece := range strings.SplitAfter(`{"organization":{"id":"x","name":"y"},"membership":{}}`, "") {
			select {
			case <-request.Context().Done():
				return false
			case <-time.After(150 * time.Millisecond):
			}
			_, _ = writer.Write([]byte(piece))
			if flusher != nil {
				flusher.Flush()
			}
		}
	default:
		return true
	}
	_ = membership
	return false
}

func (stub *Stub) registerUser(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Token string `json:"token"`
		User
	}
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&body); err != nil || body.Token == "" || body.ID == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "token and user_id are required"})
		return
	}
	stub.Register(body.Token, body.User)
	writer.WriteHeader(http.StatusNoContent)
}

func (stub *Stub) setMember(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Role string `json:"role"`
		Name string `json:"organization_name"`
	}
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&body); err != nil || body.Role == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "role is required"})
		return
	}
	stub.Join(request.PathValue("organizationID"), request.PathValue("userID"), body.Role)
	if body.Name != "" {
		stub.mu.Lock()
		stub.names[request.PathValue("organizationID")] = body.Name
		stub.mu.Unlock()
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (stub *Stub) removeMember(writer http.ResponseWriter, request *http.Request) {
	stub.mu.Lock()
	delete(stub.members[request.PathValue("organizationID")], request.PathValue("userID"))
	stub.mu.Unlock()
	writer.WriteHeader(http.StatusNoContent)
}

func (stub *Stub) setFault(writer http.ResponseWriter, request *http.Request) {
	var fault Fault
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&fault); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid fault"})
		return
	}
	stub.SetFault(fault)
	writer.WriteHeader(http.StatusNoContent)
}

func (stub *Stub) stats(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]int64{
		"lookups": stub.lookups.Load(), "rejected": stub.rejected.Load(), "faulted": stub.faulted.Load(),
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
