package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

// fakeChart is the workspace service: it answers a chart to whoever presents
// a token it issued, and remembers who asked.
type fakeChart struct {
	chart spec.OrgChart
	err   error
	loads []chartLoad
}

type chartLoad struct{ Organization, Token string }

func (chart *fakeChart) Load(_ context.Context, organizationID, token string) (agents.DirectoryContext, error) {
	chart.loads = append(chart.loads, chartLoad{organizationID, token})
	if chart.err != nil {
		return agents.DirectoryContext{}, chart.err
	}
	return agents.DirectoryContext{OrgChart: chart.chart}, nil
}

// fakeTokens is the agent-token exchange: a token for each member who has
// granted agents access, and memberauth.ErrGrantRequired for the rest.
type fakeTokens struct {
	granted map[string]bool
	err     error
	minted  []string
}

func (tokens *fakeTokens) AgentToken(_ context.Context, userID string, _ memberauth.Agent) (string, time.Time, error) {
	tokens.minted = append(tokens.minted, userID)
	if tokens.err != nil {
		return "", time.Time{}, tokens.err
	}
	if !tokens.granted[userID] {
		// AuthClient wraps nothing here, but a caller may.
		return "", time.Time{}, fmt.Errorf("agent token: %w", memberauth.ErrGrantRequired)
	}
	return "token-for-" + userID, time.Now().Add(time.Hour), nil
}

func acmeChart() spec.OrgChart {
	return spec.OrgChart{
		Executives: []string{"chief@acme.com"},
		Authority:  []string{"manages"},
		Relationships: []spec.Relationship{
			{From: "lead@acme.com", Relation: "manages", To: "dev@acme.com"},
			{From: "chief@acme.com", Relation: "manages", To: "lead@acme.com"},
			{From: "dev@acme.com", Relation: "mentors", To: "intern@acme.com"},
		},
	}
}

func person(userID, email string) Person { return Person{UserID: userID, Email: email} }

func TestChartAuthorityAnswersFromTheChart(t *testing.T) {
	chief, lead, dev, intern := person("usr_chief", "chief@acme.com"), person("usr_lead", "lead@acme.com"), person("usr_dev", "dev@acme.com"), person("usr_intern", "intern@acme.com")
	for _, test := range []struct {
		name         string
		asker, owner Person
		want         bool
	}{
		{"an executive over a manager", chief, lead, true},
		{"an executive over anyone the chart names", chief, dev, true},
		{"a manager over a report", lead, dev, true},
		{"a report over a manager", dev, lead, false},
		{"nobody over an executive", lead, chief, false},
		{"a mentor is not authority", dev, intern, false},
		{"a person over themself", lead, lead, false},
		{"someone the chart does not name", chief, person("usr_x", "outsider@acme.com"), false},
		{"someone with no address", chief, person("usr_x", ""), false},
		{"an asker with no address", person("usr_x", ""), lead, false},
		{"an asker with no id", person("", "chief@acme.com"), lead, false},
		{"addresses in any case", person("usr_chief", " Chief@ACME.com "), person("usr_lead", "LEAD@acme.com"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			chart := &fakeChart{chart: acmeChart()}
			authority := NewChartAuthority(chart, &fakeTokens{granted: map[string]bool{test.asker.UserID: true}})
			got, err := authority.Outranks(context.Background(), testOrg, test.asker, test.owner)
			if err != nil || got != test.want {
				t.Fatalf("Outranks = %v, %v, want %v", got, err, test.want)
			}
		})
	}
}

// TestTheChartIsReadAsTheAsker: the token is the asker's own, minted for
// them, and the chart is the organization's.
func TestTheChartIsReadAsTheAsker(t *testing.T) {
	chart := &fakeChart{chart: acmeChart()}
	tokens := &fakeTokens{granted: map[string]bool{"usr_chief": true, "usr_lead": true}}
	authority := NewChartAuthority(chart, tokens)
	if _, err := authority.Outranks(context.Background(), testOrg, person("usr_chief", "chief@acme.com"), person("usr_lead", "lead@acme.com")); err != nil {
		t.Fatal(err)
	}
	if len(tokens.minted) != 1 || tokens.minted[0] != "usr_chief" {
		t.Fatalf("tokens minted for %v, want the asker's alone", tokens.minted)
	}
	if len(chart.loads) != 1 || chart.loads[0] != (chartLoad{Organization: testOrg, Token: "token-for-usr_chief"}) {
		t.Fatalf("chart loads = %+v, want one as the asker", chart.loads)
	}
}

// TestNoGrantNoBypass: an asker who has not given agents a standing grant has
// no token, so the chart is not read and they outrank nobody, however high
// the chart puts them.
func TestNoGrantNoBypass(t *testing.T) {
	chart := &fakeChart{chart: acmeChart()}
	tokens := &fakeTokens{}
	authority := NewChartAuthority(chart, tokens)
	got, err := authority.Outranks(context.Background(), testOrg, person("usr_chief", "chief@acme.com"), person("usr_lead", "lead@acme.com"))
	if err != nil || got {
		t.Fatalf("Outranks = %v, %v, want false and no error", got, err)
	}
	if len(chart.loads) != 0 {
		t.Fatalf("the chart was read without a token: %+v", chart.loads)
	}
}

func TestAChartThatCannotBeReadIsAnError(t *testing.T) {
	asker, owner := person("usr_chief", "chief@acme.com"), person("usr_lead", "lead@acme.com")
	granted := map[string]bool{"usr_chief": true}

	authority := NewChartAuthority(&fakeChart{err: errors.New("workspace context returned 502")}, &fakeTokens{granted: granted})
	if got, err := authority.Outranks(context.Background(), testOrg, asker, owner); err == nil || got {
		t.Fatalf("Outranks = %v, %v, want an error and no bypass", got, err)
	}
	authority = NewChartAuthority(&fakeChart{chart: acmeChart()}, &fakeTokens{err: fmt.Errorf("%w: status 503", memberauth.ErrUnavailable)})
	if got, err := authority.Outranks(context.Background(), testOrg, asker, owner); !errors.Is(err, memberauth.ErrUnavailable) || got {
		t.Fatalf("Outranks = %v, %v, want the sign-in service being down", got, err)
	}
}

func TestWithoutAChartOrTokensNobodyOutranksAnybody(t *testing.T) {
	asker, owner := person("usr_chief", "chief@acme.com"), person("usr_lead", "lead@acme.com")
	for name, authority := range map[string]*ChartAuthority{
		"no authority": nil,
		"no chart":     NewChartAuthority(nil, &fakeTokens{granted: map[string]bool{"usr_chief": true}}),
		"no tokens":    NewChartAuthority(&fakeChart{chart: acmeChart()}, nil),
	} {
		if got, err := authority.Outranks(context.Background(), testOrg, asker, owner); err != nil || got {
			t.Errorf("%s: Outranks = %v, %v", name, got, err)
		}
	}
}

// TestTheChartDecidesABypassEndToEnd runs a mention through a router whose
// authority is the real ChartAuthority: an executive with a grant bypasses
// the manager under them, and without one is one more asker.
func TestTheChartDecidesABypassEndToEnd(t *testing.T) {
	for _, test := range []struct {
		name    string
		granted bool
		bypass  *consentpolicy.Bypass
	}{
		{"an executive with a standing grant", true, &outranksDM},
		{"an executive who never gave one", false, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.member("UCHIEF", "Chief")
			h.member("ULEAD", "Lead")
			chart := &fakeChart{chart: acmeChart()}
			h.router.deps.Authority = NewChartAuthority(chart, &fakeTokens{granted: map[string]bool{"usr_chief": test.granted}})
			job := h.mention(toBot("ask <@ULEAD> about the roadmap"), func(m *Mention) { m.UserID = "UCHIEF" })
			h.mustRun(job)
			got := h.ask().Owners[0].Bypass
			if (got == nil) != (test.bypass == nil) || (got != nil && *got != *test.bypass) {
				t.Fatalf("bypass = %+v, want %+v", got, test.bypass)
			}
			if wantLoads := map[bool]int{true: 1, false: 0}[test.granted]; len(chart.loads) != wantLoads {
				t.Fatalf("chart loaded %d times, want %d", len(chart.loads), wantLoads)
			}
		})
	}
}
