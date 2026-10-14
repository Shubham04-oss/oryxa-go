package serviceconfig

import (
	"maps"
	"strings"
	"testing"
	"time"
)

func TestPipedreamPollFloorIsWholePositiveSeconds(t *testing.T) {
	t.Parallel()
	if unset, err := FromEnv(environment(required())); err != nil || unset.PipedreamMinPoll != 0 {
		t.Fatalf("unset = %v, %v; want zero, apps' default", unset.PipedreamMinPoll, err)
	}
	values := required()
	values["APPS_PIPEDREAM_MIN_POLL_SECONDS"] = "1800"
	if set, err := FromEnv(environment(values)); err != nil || set.PipedreamMinPoll != 30*time.Minute {
		t.Fatalf("1800 = %v, %v", set.PipedreamMinPoll, err)
	}
	for _, value := range []string{"0", "-5", "15m", "1.5"} {
		values["APPS_PIPEDREAM_MIN_POLL_SECONDS"] = value
		if _, err := FromEnv(environment(values)); err == nil {
			t.Errorf("%q was accepted", value)
		}
	}
}

func TestPipedreamIsOnOnlyWithAllThreeCredentials(t *testing.T) {
	t.Parallel()
	off, err := FromEnv(environment(required()))
	if err != nil || off.Pipedream != nil {
		t.Fatalf("unset = %+v, %v", off.Pipedream, err)
	}

	values := required()
	values["PIPEDREAM_CLIENT_ID"], values["PIPEDREAM_CLIENT_SECRET"], values["PIPEDREAM_PROJECT_ID"] = "id", "secret", "proj_1"
	on, err := FromEnv(environment(values))
	if err != nil || on.Pipedream == nil || on.Pipedream.Environment != "development" || on.Pipedream.ProjectID != "proj_1" {
		t.Fatalf("set = %+v, %v", on.Pipedream, err)
	}

	for name, change := range map[string][2]string{
		"one credential missing":          {"PIPEDREAM_PROJECT_ID", ""},
		"an unknown environment":          {"PIPEDREAM_ENVIRONMENT", "staging"},
		"a base URL that is not a fake's": {"PIPEDREAM_BASE_URL", "https://pipedream.example.com/v1"},
	} {
		broken := maps.Clone(values)
		broken[change[0]] = change[1]
		if _, err := FromEnv(environment(broken)); err == nil || strings.Contains(err.Error(), values["PIPEDREAM_CLIENT_SECRET"]+" ") {
			t.Errorf("%s = %v, want a refusal", name, err)
		}
	}
	values["PIPEDREAM_BASE_URL"] = "http://127.0.0.1:18094/v1"
	if fake, err := FromEnv(environment(values)); err != nil || fake.Pipedream.BaseURL != "http://127.0.0.1:18094/v1" {
		t.Fatalf("loopback = %+v, %v", fake, err)
	}
}
