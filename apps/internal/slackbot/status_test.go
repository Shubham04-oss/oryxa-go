package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

func TestSlackStatusIsMemberMetadataWithoutOAuthOrTokenAccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"member", "admin", "owner"} {
		view, err := h.service.Status(ctx, bearer(name), testOrg)
		if err != nil || view.Status != StatusNotInstalled || view.CanManage != (name != "member") {
			t.Fatalf("%s status=%+v err=%v", name, view, err)
		}
	}
	install := h.installFor(testOrg)
	for _, status := range []string{StatusActive, StatusEnded} {
		install.Status = status
		h.store.mu.Lock()
		h.store.installs[install.TeamID] = install
		h.store.mu.Unlock()
		view, err := h.service.Status(ctx, bearer("member"), testOrg)
		if err != nil || view.Status != status || view.CanManage {
			t.Fatalf("member status=%+v err=%v", view, err)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 2 || fields["status"] != status || fields["can_manage"] != false {
			t.Fatalf("status fields=%s err=%v", encoded, err)
		}
	}
	if len(h.store.flows) != 0 || len(h.store.published()) != 0 || len(h.fake.Calls()) != 0 || h.keys.opened() != 0 {
		t.Fatalf("status mutated or contacted an integration: flows=%d events=%d calls=%d token opens=%d", len(h.store.flows), len(h.store.published()), len(h.fake.Calls()), h.keys.opened())
	}
}

func TestSlackStatusRefusesOtherMembershipAndPreservesReadFailures(t *testing.T) {
	h := newHarness(t)
	for _, test := range []struct {
		name, org string
		want      error
	}{
		{"agent", testOrg, domain.ErrForbidden},
		{"member", "org_elsewhere", domain.ErrForbidden},
		{"stranger", testOrg, domain.ErrForbidden},
		{"", testOrg, domain.ErrUnauthorized},
		{"member", "", domain.ErrInvalid},
	} {
		if _, err := h.service.Status(context.Background(), authclient.Credentials{Authorization: test.name}, test.org); !errors.Is(err, test.want) {
			t.Fatalf("%s/%s=%v, want %v", test.name, test.org, err, test.want)
		}
	}
	failure := errors.New("database unavailable")
	h.store.fail = failure
	if view, err := h.service.Status(context.Background(), bearer("member"), testOrg); !errors.Is(err, failure) || view.Status != "" {
		t.Fatalf("failed metadata read=%+v err=%v", view, err)
	}
	install := h.installFor(testOrg)
	install.Status = "unknown"
	h.store.installs[install.TeamID] = install
	if view, err := h.service.Status(context.Background(), bearer("member"), testOrg); !errors.Is(err, domain.ErrUnavailable) || view.Status != "" {
		t.Fatalf("invalid metadata=%+v err=%v", view, err)
	}
}
