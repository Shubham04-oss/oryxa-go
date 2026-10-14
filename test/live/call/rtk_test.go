package call

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// rtk reads RealtimeKit's own side of a call, when the test was given its
// API: a participant left behind keeps a seat, and its token stays good for
// 100 days, so the edge deletes it on leave and when the call ends.
//
//	CALL_LIVE_RTK_ACCOUNT_ID, CALL_LIVE_RTK_APP_ID, CALL_LIVE_RTK_API_TOKEN
//	                    the staging RealtimeKit app (the edge's REALTIMEKIT_*)
//	CALL_LIVE_RTK_URL   the API's accounts URL, for test/live/call/fakertk
type rtk struct {
	base, token string
	client      *http.Client
}

func rtkFromEnv(t *testing.T) *rtk {
	t.Helper()
	account, app, token := os.Getenv("CALL_LIVE_RTK_ACCOUNT_ID"), os.Getenv("CALL_LIVE_RTK_APP_ID"), os.Getenv("CALL_LIVE_RTK_API_TOKEN")
	if account == "" || app == "" || token == "" {
		t.Log("no CALL_LIVE_RTK_*: RealtimeKit's participants are not checked")
		return nil
	}
	api := strings.TrimRight(envOr("CALL_LIVE_RTK_URL", "https://api.cloudflare.com/client/v4/accounts"), "/")
	return &rtk{
		base:   api + "/" + url.PathEscape(account) + "/realtime/kit/" + url.PathEscape(app),
		token:  token,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// participantExists asks RealtimeKit whether meeting still has participant.
func (k *rtk) participantExists(t *testing.T, meeting, participant string) bool {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, k.base+"/meetings/"+url.PathEscape(meeting)+"/participants/"+url.PathEscape(participant), nil)
	request.Header.Set("Authorization", "Bearer "+k.token)
	response, err := k.client.Do(request)
	if err != nil {
		t.Fatalf("ask RealtimeKit for participant %s: %v", participant, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		return true
	case response.StatusCode == http.StatusNotFound:
		return false
	default:
		t.Fatalf("RealtimeKit answered %d for participant %s", response.StatusCode, participant)
		return false
	}
}

// gone checks RealtimeKit no longer has any of participants in meeting.
func (k *rtk) gone(t *testing.T, when, meeting string, participants ...string) {
	t.Helper()
	if k == nil {
		return
	}
	for _, participant := range participants {
		if k.participantExists(t, meeting, participant) {
			t.Errorf("%s, RealtimeKit still has participant %s in meeting %s: its seat and token were not given back", when, participant, meeting)
		}
	}
}

// present checks RealtimeKit has each of participants in meeting.
func (k *rtk) present(t *testing.T, when, meeting string, participants ...string) {
	t.Helper()
	if k == nil {
		return
	}
	for _, participant := range participants {
		if !k.participantExists(t, meeting, participant) {
			t.Errorf("%s, RealtimeKit has no participant %s in meeting %s", when, participant, meeting)
		}
	}
}
