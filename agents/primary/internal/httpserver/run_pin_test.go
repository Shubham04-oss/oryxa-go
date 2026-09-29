package httpserver

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

const runsPath = "/v1/organizations/org_1/threads/thread_1/runs"

// TestARunBodyMayPinItToATeamspace: a side conversation's run names the
// teamspace it is about, and it reaches the run as that run's pin, trimmed,
// whether the run answers a message or the member's confirmations. Left out,
// or null, there is none.
func TestARunBodyMayPinItToATeamspace(t *testing.T) {
	for name, testCase := range map[string]struct {
		body string
		want threads.RunInput
	}{
		"a message and a teamspace": {
			`{"message":"Ask the launch room","teamspace_id":"tsp_launch"}`,
			threads.RunInput{Message: "Ask the launch room", TeamspaceID: "tsp_launch"},
		},
		"a teamspace with spaces around it": {
			`{"message":"Ask the launch room","teamspace_id":"  tsp_launch\t"}`,
			threads.RunInput{Message: "Ask the launch room", TeamspaceID: "tsp_launch"},
		},
		"confirmations and a teamspace": {
			`{"confirmations":[{"id":"c1","confirmed":true}],"teamspace_id":"tsp_launch"}`,
			threads.RunInput{Confirmations: []threads.Answer{{ID: "c1", Confirmed: true}}, TeamspaceID: "tsp_launch"},
		},
		"a teamspace of 128 characters": {
			`{"message":"hi","teamspace_id":"` + strings.Repeat("t", 128) + `"}`,
			threads.RunInput{Message: "hi", TeamspaceID: strings.Repeat("t", 128)},
		},
		"no teamspace":     {`{"message":"hi"}`, threads.RunInput{Message: "hi"}},
		"a null teamspace": {`{"message":"hi","teamspace_id":null}`, threads.RunInput{Message: "hi"}},
		"a separate publication question": {
			`{"message":"wrapped side prompt","publication_request":{"request":"When is launch?","context":["Bo: It was Friday."]}}`,
			threads.RunInput{Message: "wrapped side prompt", PublicationRequest: &threads.PublicationRequest{Request: "When is launch?", Context: []string{"Bo: It was Friday."}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeThreads{run: &fakeRun{}}
			response := serve(newTestServer(member(), service), http.MethodPost, runsPath, testCase.body, bearer)
			if response.Code != http.StatusOK || len(service.inputs) != 1 {
				t.Fatalf("run = %d %s with %d input(s), want the run started once", response.Code, response.Body.String(), len(service.inputs))
			}
			got := service.inputs[0]
			if got.Message != testCase.want.Message || got.TeamspaceID != testCase.want.TeamspaceID || !reflect.DeepEqual(got.PublicationRequest, testCase.want.PublicationRequest) ||
				len(got.Confirmations) != len(testCase.want.Confirmations) {
				t.Fatalf("the run was started with %+v, want %+v", got, testCase.want)
			}
			if !service.run.executed {
				t.Error("the run was not executed")
			}
		})
	}
}

func TestPublicationQuestionCannotCarryProvenanceOrGuardSettings(t *testing.T) {
	for _, question := range []string{
		`{"request":"When is launch?","sources":[]}`,
		`{"request":"When is launch?","mode":"off"}`,
		`{"request":"When is launch?","exempt":true}`,
		`{"request":123}`,
		`{"request":"Question","context":"not a list"}`,
	} {
		service := &fakeThreads{run: &fakeRun{}}
		response := serve(newTestServer(member(), service), http.MethodPost, runsPath, `{"message":"wrapped prompt","publication_request":`+question+`}`, bearer)
		if response.Code != http.StatusBadRequest || len(service.inputs) != 0 {
			t.Fatalf("untrusted metadata reached the run: status=%d inputs=%+v", response.Code, service.inputs)
		}
	}
}

// TestARunBodyWhoseTeamspaceIsNotOneIsRefusedBeforeItStarts: a teamspace that
// is blank, holds whitespace, is longer than 128 characters or is not a string
// is the caller's mistake, answered 400 invalid_request with the run never
// started, so a run that was meant to be pinned is never quietly left free.
func TestARunBodyWhoseTeamspaceIsNotOneIsRefusedBeforeItStarts(t *testing.T) {
	for name, body := range map[string]string{
		"an empty string":                     `{"message":"hi","teamspace_id":""}`,
		"only spaces":                         `{"message":"hi","teamspace_id":"   "}`,
		"a space inside":                      `{"message":"hi","teamspace_id":"tsp launch"}`,
		"a tab inside":                        `{"message":"hi","teamspace_id":"tsp\tlaunch"}`,
		"a newline inside":                    `{"message":"hi","teamspace_id":"tsp_launch\ntsp_other"}`,
		"a no-break space":                    `{"message":"hi","teamspace_id":"tsp launch"}`,
		"129 characters":                      `{"message":"hi","teamspace_id":"` + strings.Repeat("t", 129) + `"}`,
		"129 characters, é":                   `{"message":"hi","teamspace_id":"` + strings.Repeat("é", 129) + `"}`,
		"a number":                            `{"message":"hi","teamspace_id":123}`,
		"a list":                              `{"message":"hi","teamspace_id":["tsp_launch"]}`,
		"an object":                           `{"message":"hi","teamspace_id":{"id":"tsp_launch"}}`,
		"an unknown field beside a teamspace": `{"message":"hi","teamspace_id":"tsp_launch","teamspace_ids":["tsp_other"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeThreads{run: &fakeRun{}}
			response := serve(newTestServer(member(), service), http.MethodPost, runsPath, body, bearer)
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
				t.Fatalf("run = %d %s, want 400 invalid_request", response.Code, response.Body.String())
			}
			if len(service.inputs) != 0 || service.run.executed {
				t.Fatalf("a run was started for it: %+v", service.inputs)
			}
		})
	}
}

// TestARunsTeamspaceOnlyPinsAThreadsRun: the agent runs and the other routes
// take no teamspace of theirs; the pin belongs to a thread's run.
func TestARunsTeamspaceOnlyPinsAThreadsRun(t *testing.T) {
	service := &fakeThreads{run: &fakeRun{}}
	server := newTestServer(member(), service)
	for _, path := range []string{"/v1/organizations/org_1/threads", "/v1/organizations/org_1/agents/agt_sales/runs"} {
		response := serve(server, http.MethodPost, path, `{"message":"hi","teamspace_id":"tsp_launch"}`, bearer)
		if response.Code != http.StatusBadRequest {
			t.Errorf("POST %s with a teamspace = %d %s, want it refused as an unknown field", path, response.Code, response.Body.String())
		}
	}
	if len(service.inputs) != 0 {
		t.Fatalf("a run was started: %+v", service.inputs)
	}
}
