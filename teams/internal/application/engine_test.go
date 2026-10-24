package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// decider answers every decision with answer and err.
type decider struct {
	answer bool
	err    error
}

func (d decider) Decide(context.Context, access.Ask, bool) (bool, error) { return d.answer, d.err }

// DecideMany answers as *shadow.Shadow does: answer narrows the code's, no
// answer leaves the code's, and any other error fails the batch.
func (d decider) DecideMany(_ context.Context, asks []access.Ask, codeAllowed []bool) ([]bool, error) {
	if d.err != nil && !errors.Is(d.err, access.ErrNoAnswer) {
		return nil, d.err
	}
	allowed := make([]bool, len(asks))
	for i := range asks {
		allowed[i] = codeAllowed[i] && (d.answer || d.err != nil)
	}
	return allowed, nil
}

// counted is a decider that counts its calls and what a batch asked.
type counted struct {
	decider
	singles int
	batches [][]access.Ask
}

func (c *counted) Decide(ctx context.Context, ask access.Ask, code bool) (bool, error) {
	c.singles++
	return c.decider.Decide(ctx, ask, code)
}

func (c *counted) DecideMany(ctx context.Context, asks []access.Ask, code []bool) ([]bool, error) {
	c.batches = append(c.batches, asks)
	return c.decider.DecideMany(ctx, asks, code)
}

type teamWrites struct{ log []string }

func (w *teamWrites) TeamCreated(_ context.Context, team domain.Team, parentID string) error {
	w.log = append(w.log, "team "+team.ID+" parent "+parentID)
	return errors.New("spicedb unavailable")
}

func (w *teamWrites) TeamsDeleted(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		w.log = append(w.log, "deleted "+id)
	}
	return errors.New("spicedb unavailable")
}

func (w *teamWrites) MemberSet(_ context.Context, _, teamID, userID string, role domain.Role) error {
	w.log = append(w.log, "member "+teamID+" "+userID+" "+string(role))
	return errors.New("spicedb unavailable")
}

func (w *teamWrites) MemberRemoved(_ context.Context, _, teamID, userID string) error {
	w.log = append(w.log, "removed "+teamID+" "+userID)
	return errors.New("spicedb unavailable")
}

// With the engine on, SpiceDB can refuse what the code allows but not grant
// what it refuses, for one team and for a list.
func TestTheEngineOnlyNarrows(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	service.WithEngine(decider{answer: true}, nil)
	if _, err := service.Team(ctx, creds("user-outsider"), "org_1", team.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB alone let an outsider see the team: %v", err)
	}
	detail, err := service.Team(ctx, creds("user-owner"), "org_1", team.ID)
	if err != nil || !detail.CanManage {
		t.Fatalf("owner: %+v, %v", detail, err)
	}

	service.WithEngine(decider{answer: false}, nil)
	if _, err := service.Team(ctx, creds("user-owner"), "org_1", team.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused, but Team = %v", err)
	}
	if listed, err := service.Teams(ctx, creds("user-owner"), "org_1"); err != nil || len(listed) != 0 {
		t.Fatalf("SpiceDB refused, but Teams listed %d, %v", len(listed), err)
	}
	if err := service.DeleteTeam(ctx, creds("user-owner"), "org_1", team.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SpiceDB refused, but DeleteTeam = %v", err)
	}
}

// No answer from SpiceDB refuses; no model of the case falls back to the code.
func TestTheEngineFailsClosed(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	service.WithEngine(decider{answer: true, err: errors.New("spicedb unavailable")}, nil)
	if _, err := service.Team(ctx, creds("user-owner"), "org_1", team.ID); err == nil {
		t.Fatal("a SpiceDB failure allowed the read")
	}
	if _, err := service.Teams(ctx, creds("user-owner"), "org_1"); err == nil {
		t.Fatal("a SpiceDB failure allowed the list")
	}
	service.WithEngine(decider{err: access.ErrNoAnswer}, nil)
	if _, err := service.Team(ctx, creds("user-owner"), "org_1", team.ID); err != nil {
		t.Fatalf("with no answer the code's allow should stand: %v", err)
	}
}

// Teams and members are written as they change, a subteam with its parent,
// and a failure to write them fails none of it.
func TestRelationshipsFollowTeamsAndMembers(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService()
	writes := &teamWrites{}
	service.WithEngine(decider{answer: true}, writes)
	parent, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Payments", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", child.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeMemberRole(ctx, creds("user-owner"), "org_1", child.ID, "user-member", domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveMember(ctx, creds("user-owner"), "org_1", child.ID, "user-member"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteTeam(ctx, creds("user-owner"), "org_1", parent.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"team " + parent.ID + " parent ", "member " + parent.ID + " user-owner admin",
		"team " + child.ID + " parent " + parent.ID, "member " + child.ID + " user-owner admin",
		"member " + child.ID + " user-member member",
		"member " + child.ID + " user-member admin",
		"removed " + child.ID + " user-member",
	}
	if len(writes.log) < len(want)+2 {
		t.Fatalf("writes = %v", writes.log)
	}
	for i, line := range want {
		if writes.log[i] != line {
			t.Fatalf("write %d = %q, want %q (all: %v)", i, writes.log[i], line, writes.log)
		}
	}
	deleted := writes.log[len(want):]
	if len(deleted) != 2 || !(deleted[0] == "deleted "+parent.ID || deleted[1] == "deleted "+parent.ID) {
		t.Fatalf("deleting the parent deleted %v, want it and its subteam", deleted)
	}
}

// A list asks the engine once, for seeing and managing every team on it.
func TestAListIsDecidedInOneCall(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService()
	for _, name := range []string{"Engineering", "Design"} {
		if _, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	engine := &counted{decider: decider{answer: true}}
	service.WithEngine(engine, nil)
	listed, err := service.Teams(ctx, creds("user-owner"), "org_1")
	if err != nil || len(listed) != 2 || !listed[0].CanManage || !listed[1].CanManage {
		t.Fatalf("Teams = %+v, %v; want both, managed", listed, err)
	}
	if engine.singles != 0 || len(engine.batches) != 1 {
		t.Fatalf("engine asked %d singles and %d batches; want 1 batch", engine.singles, len(engine.batches))
	}
	var actions []string
	for _, ask := range engine.batches[0] {
		actions = append(actions, ask.Action)
	}
	if want := []string{access.ActionRead, access.ActionRead, access.ActionWrite, access.ActionWrite}; !slices.Equal(actions, want) {
		t.Fatalf("the batch asked %v, want %v", actions, want)
	}
}
