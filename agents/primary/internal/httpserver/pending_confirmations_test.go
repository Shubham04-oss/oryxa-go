package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// confirmingThreads is fakeThreads with chats waiting on their member.
type confirmingThreads struct {
	fakeThreads
	waiting []threads.PendingThread
	asked   []threads.Member
}

func (service *confirmingThreads) PendingConfirmations(_ context.Context, member threads.Member) ([]threads.PendingThread, error) {
	service.asked = append(service.asked, member)
	return service.waiting, service.err
}

func TestPendingConfirmationsAnswersTheMembersWaitingChats(t *testing.T) {
	service := &confirmingThreads{waiting: []threads.PendingThread{
		{Thread: threads.Thread{ID: "thread_2", Title: "Delete plan", Origin: threads.OriginChat, UpdatedAt: time.Unix(20, 0).UTC()}, PendingConfirmations: 2},
		{Thread: threads.Thread{ID: "thread_9", Title: "Old one", Origin: threads.OriginChat, UpdatedAt: time.Unix(10, 0).UTC()}, PendingConfirmations: 1},
	}}
	server := New(member(), service, Config{RunTimeout: time.Minute}, nil)

	recorder := serve(server, http.MethodGet, "/v1/organizations/org_1/pending-confirmations", "", bearer)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Threads []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Pending int    `json:"pending_confirmations"`
		} `json:"threads"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 3 || len(body.Threads) != 2 || body.Threads[0].ID != "thread_2" || body.Threads[0].Pending != 2 || body.Threads[1].Title != "Old one" {
		t.Fatalf("body = %+v", body)
	}
	if len(service.asked) != 1 || service.asked[0].UserID != "user_1" || service.asked[0].OrganizationID != "org_1" {
		t.Fatalf("asked for = %+v, want the caller in the path's organization", service.asked)
	}
}

func TestPendingConfirmationsIsAnEmptyListWhenNothingWaits(t *testing.T) {
	server := New(member(), &confirmingThreads{waiting: []threads.PendingThread{}}, Config{RunTimeout: time.Minute}, nil)
	recorder := serve(server, http.MethodGet, "/v1/organizations/org_1/pending-confirmations", "", bearer)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"threads\":[],\"total\":0}\n" {
		t.Fatalf("status = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestPendingConfirmationsNeedsAMemberAndAServiceThatCounts(t *testing.T) {
	server := New(member(), &confirmingThreads{}, Config{RunTimeout: time.Minute}, nil)
	if recorder := serve(server, http.MethodGet, "/v1/organizations/org_1/pending-confirmations", "", nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials = %d", recorder.Code)
	}
	// Only the Threads methods: no count to read.
	plain := New(member(), struct{ Threads }{&fakeThreads{}}, Config{RunTimeout: time.Minute}, nil)
	if recorder := serve(plain, http.MethodGet, "/v1/organizations/org_1/pending-confirmations", "", bearer); recorder.Code != http.StatusNotFound {
		t.Fatalf("without counts = %d", recorder.Code)
	}
}
