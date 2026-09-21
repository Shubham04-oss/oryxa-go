package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/rig"
)

// Identity is one person the stub knows.
type Identity struct {
	Name   string
	Token  string
	UserID string
	Email  string
}

// Org is an organization with people in each role.
type Org struct {
	Key     string // "a" or "b"
	ID      string
	Owner   Identity
	Admin   Identity
	Member  Identity
	Member2 Identity
}

// World is the cast and the scenery of a run: two organizations that must
// never see each other, and people who belong to neither.
type World struct {
	State rig.State
	C     *Client
	A, B  Org
	// Ghost has a valid token and belongs to no organization.
	Ghost Identity
	// Dual is a member of both organizations, the case that shows whether an
	// ID from one is honoured under the other.
	Dual Identity

	mu   sync.Mutex
	vars map[string]map[string]string // org key -> name -> value
}

func randomID(prefix string) string {
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}

// Bootstrap registers everyone on the stub and puts them in their organizations.
func Bootstrap(ctx context.Context, state rig.State, client *Client) (*World, error) {
	world := &World{State: state, C: client, vars: map[string]map[string]string{"a": {}, "b": {}}}
	newPerson := func(name string) Identity {
		id := randomID("user_")
		return Identity{Name: name, Token: "tok-" + strings.ToLower(name) + "-" + id[5:11], UserID: id, Email: strings.ToLower(name) + "-" + strings.ToLower(id[5:11]) + "@chaos.test"}
	}
	orgOf := func(key string) Org {
		return Org{
			Key: key, ID: randomID("org_"),
			Owner: newPerson(key + "-owner"), Admin: newPerson(key + "-admin"),
			Member: newPerson(key + "-member"), Member2: newPerson(key + "-member2"),
		}
	}
	world.A, world.B = orgOf("a"), orgOf("b")
	world.Ghost, world.Dual = newPerson("ghost"), newPerson("dual")

	people := []Identity{world.Ghost, world.Dual}
	for _, org := range []Org{world.A, world.B} {
		people = append(people, org.Owner, org.Admin, org.Member, org.Member2)
	}
	for _, org := range []Org{world.A, world.B} {
		world.Set(org.Key, "owner_uid", org.Owner.UserID)
		world.Set(org.Key, "admin_uid", org.Admin.UserID)
		world.Set(org.Key, "member_uid", org.Member.UserID)
		world.Set(org.Key, "member2_uid", org.Member2.UserID)
		world.Set(org.Key, "dual_uid", world.Dual.UserID)
	}
	for _, person := range people {
		if err := stubCall(ctx, state.StubURL, http.MethodPost, "/_stub/users", map[string]string{
			"token": person.Token, "user_id": person.UserID, "email": person.Email, "display_name": person.Name,
		}); err != nil {
			return nil, err
		}
	}
	if err := world.seedAuthTables(ctx, people); err != nil {
		return nil, err
	}
	for _, org := range []Org{world.A, world.B} {
		for _, seat := range []struct {
			role   string
			person Identity
		}{{"owner", org.Owner}, {"admin", org.Admin}, {"member", org.Member}, {"member", org.Member2}} {
			if err := stubCall(ctx, state.StubURL, http.MethodPut, fmt.Sprintf("/_stub/organizations/%s/members/%s", org.ID, seat.person.UserID),
				map[string]string{"role": seat.role, "organization_name": "Chaos " + strings.ToUpper(org.Key)}); err != nil {
				return nil, err
			}
		}
		if err := stubCall(ctx, state.StubURL, http.MethodPut, fmt.Sprintf("/_stub/organizations/%s/members/%s", org.ID, world.Dual.UserID),
			map[string]string{"role": "member"}); err != nil {
			return nil, err
		}
	}
	return world, nil
}

// seedAuthTables mirrors the stub's people into auth's own tables, which the
// agents service reads directly to learn a member's name and role.
func (world *World) seedAuthTables(ctx context.Context, people []Identity) error {
	conn, err := pgx.Connect(ctx, world.State.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	now := time.Now().UTC()
	for _, person := range people {
		if _, err := conn.Exec(ctx, `INSERT INTO auth_users (id, email, display_name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4) ON CONFLICT (id) DO NOTHING`, person.UserID, person.Email, person.Name, now); err != nil {
			return fmt.Errorf("seed auth_users: %w", err)
		}
	}
	for _, org := range []Org{world.A, world.B} {
		slug := strings.ToLower(strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, org.ID))
		if _, err := conn.Exec(ctx, `INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5) ON CONFLICT (id) DO NOTHING`, org.ID, "Chaos "+strings.ToUpper(org.Key), slug, org.Owner.UserID, now); err != nil {
			return fmt.Errorf("seed auth_organizations: %w", err)
		}
		for _, seat := range []struct {
			role   string
			person Identity
		}{{"owner", org.Owner}, {"admin", org.Admin}, {"member", org.Member}, {"member", org.Member2}, {"member", world.Dual}} {
			if _, err := conn.Exec(ctx, `INSERT INTO auth_memberships (organization_id, user_id, role, joined_at)
				VALUES ($1, $2, $3, $4) ON CONFLICT (organization_id, user_id) DO NOTHING`, org.ID, seat.person.UserID, seat.role, now); err != nil {
				return fmt.Errorf("seed auth_memberships: %w", err)
			}
		}
	}
	return nil
}

func stubCall(ctx context.Context, base, method, path string, body any) error {
	encoded, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("stub %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("stub %s %s: %s", method, path, response.Status)
	}
	return nil
}

// Org returns "a" or "b".
func (world *World) Org(key string) Org {
	if key == "b" {
		return world.B
	}
	return world.A
}

// Set remembers a value made during setup, such as a team id, for an org.
func (world *World) Set(orgKey, name, value string) {
	world.mu.Lock()
	defer world.mu.Unlock()
	world.vars[orgKey][name] = value
}

// Get reads one back.
func (world *World) Get(orgKey, name string) string {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.vars[orgKey][name]
}

// Resolve fills the {placeholders} of a path template for an organization.
// {org} is the organization's id; anything else comes from the vars.
func (world *World) Resolve(orgKey, template string) (string, []string) {
	var missing []string
	out := template
	for {
		start := strings.Index(out, "{")
		if start < 0 {
			return out, missing
		}
		end := strings.Index(out[start:], "}")
		if end < 0 {
			return out, missing
		}
		name := out[start+1 : start+end]
		value := world.Get(orgKey, name)
		if name == "org" {
			value = world.Org(orgKey).ID
		}
		if value == "" {
			missing = append(missing, name)
			value = "missing-" + name
		}
		out = out[:start] + value + out[start+end+1:]
	}
}

// Identity finds a person by "a.owner", "b.member", "ghost", "dual", "none".
func (world *World) Identity(name string) Identity {
	switch name {
	case "ghost":
		return world.Ghost
	case "dual":
		return world.Dual
	case "none", "":
		return Identity{Name: "none"}
	}
	key, role, _ := strings.Cut(name, ".")
	org := world.Org(key)
	switch role {
	case "owner":
		return org.Owner
	case "admin":
		return org.Admin
	case "member2":
		return org.Member2
	default:
		return org.Member
	}
}
