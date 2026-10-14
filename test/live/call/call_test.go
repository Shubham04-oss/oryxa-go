package call

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// How long the agent gets to start answering, and to bring back a
// dispatched run's answer.
const (
	answerWithin   = 30 * time.Second
	dispatchWithin = 5 * time.Minute
	quietFor       = 8 * time.Second
)

// A call starts with the first member in, keeps one meeting for everyone,
// gives a rejoin a fresh participant, and ends when the last one leaves.
func TestCallLifecycle(t *testing.T) {
	r := setup(t).newRoom(t)
	seats := rtkFromEnv(t)

	first := r.join(r.a)
	if first.CallID == "" || first.MeetingID == "" || first.ParticipantID == "" {
		t.Fatalf("join = %+v", first)
	}
	if first.Preset != "oryxa-member" {
		t.Errorf("preset = %q, want oryxa-member", first.Preset)
	}
	if parts := strings.Split(first.Token, "."); len(parts) != 3 {
		t.Errorf("the RealtimeKit token is not a JWT: %d parts", len(parts))
	}
	view := r.call(r.a)
	if view == nil || view.CallID != first.CallID || view.StartedBy != r.a.id || !slices.Equal(view.members(), []string{r.a.id}) {
		t.Fatalf("after A joined, call = %+v", view)
	}
	if !view.Agent.Present || view.Agent.Name != r.agentName {
		t.Errorf("agent = %+v, want %s present", view.Agent, r.agentName)
	}
	if view.Recording.On {
		t.Error("recording is on without anyone asking: it must be opt-in")
	}

	second := r.join(r.b)
	if second.CallID != first.CallID || second.MeetingID != first.MeetingID || second.ParticipantID == first.ParticipantID {
		t.Fatalf("B joined %+v, A joined %+v: want one call and meeting, two participants", second, first)
	}
	if got := r.call(r.b).members(); len(got) != 2 {
		t.Fatalf("participants = %v, want A and B", got)
	}

	again := r.join(r.a)
	if again.CallID != first.CallID || again.ParticipantID == first.ParticipantID || again.Token == first.Token {
		t.Errorf("A's rejoin = %+v: want the same call with a new participant and token", again)
	}
	if got := r.call(r.a).members(); len(got) != 2 {
		t.Errorf("after A rejoined, participants = %v, want A once and B", got)
	}
	seats.gone(t, "after A rejoined", first.MeetingID, first.ParticipantID)
	seats.present(t, "with A and B in the call", first.MeetingID, again.ParticipantID, second.ParticipantID)

	r.must(r.b, http.MethodPost, "/call/leave", map[string]any{}, nil)
	if got := r.call(r.a).members(); !slices.Equal(got, []string{r.a.id}) {
		t.Errorf("after B left, participants = %v", got)
	}
	seats.gone(t, "after B left", first.MeetingID, second.ParticipantID)
	r.must(r.a, http.MethodPost, "/call/leave", map[string]any{}, nil)
	if view := r.call(r.a); view != nil {
		t.Fatalf("the last member left, but the call goes on: %+v", view)
	}
	seats.gone(t, "after the call ended", first.MeetingID, first.ParticipantID, second.ParticipantID, again.ParticipantID)

	events := forCall(r.log(r.a), first.CallID)
	want := []string{"call.started", "call.joined", "call.joined", "call.left", "call.left", "call.ended"}
	got := types(events)
	for _, kind := range want {
		index := slices.Index(got, kind)
		if index < 0 {
			t.Fatalf("the log for the call is %v; want %v in order", got, want)
		}
		got = got[index+1:]
	}

	// A new call is a new call.
	next := r.join(r.a)
	if next.CallID == first.CallID {
		t.Error("a call after the last one ended reused its id")
	}
}

// Only the room's members get a call token, and only the people in the call
// hear the agent or speak into the transcript.
func TestNonMembersGetNoCall(t *testing.T) {
	r := setup(t).newRoom(t)
	call := r.join(r.a)

	if r.outsider != nil {
		if r.admits() {
			if status := r.admit(r.outsider); status != http.StatusNotFound {
				t.Errorf("Go admitted an outsider to the room: %d, want 404", status)
			}
		}
		for _, request := range []struct{ method, rest string }{
			{http.MethodPost, "/call/join"}, {http.MethodGet, "/call"}, {http.MethodPost, "/call/leave"}, {http.MethodPost, "/call/end"},
		} {
			var body any
			if request.method == http.MethodPost {
				body = map[string]any{}
			}
			if status, text := r.doAs(r.outsider, request.method, request.rest, body, nil); status != http.StatusForbidden {
				t.Errorf("an outsider's %s %s = %d %s, want 403", request.method, request.rest, status, text)
			}
		}
		if _, status, err := r.dial(*r.outsider); err == nil || status != http.StatusForbidden {
			t.Errorf("an outsider's /voice/live = %d %v, want 403", status, err)
		}
	} else {
		t.Log("no CALL_LIVE_OUTSIDER_ID/COOKIE: skipped the outsider")
	}

	if status, _ := r.doAs(nil, http.MethodPost, "/call/join", map[string]any{}, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Errorf("a join with no session = %d, want 401 or 403", status)
	}

	// B is a member, but not in the call.
	if _, status, err := r.dial(r.b); err == nil || status != http.StatusForbidden {
		t.Errorf("/voice/live for a member not in the call = %d %v, want 403", status, err)
	}
	if status, text := r.do(r.b, http.MethodPost, "/call/transcript", map[string]any{"callId": call.CallID, "segments": []segment{said(r.b, "hello", false)}}, nil); status != http.StatusForbidden {
		t.Errorf("a transcript from a member not in the call = %d %s, want 403", status, text)
	}
	if status, text := r.do(r.b, http.MethodPost, "/call/agent", map[string]any{"present": false}, nil); status != http.StatusForbidden {
		t.Errorf("dismissing the agent from outside the call = %d %s, want 403", status, text)
	}

	// The refusals are in the log.
	r.eventually(r.a, 10*time.Second, "access.denied events", func(events []edgev1.Event) bool {
		return len(ofType(events, "access.denied")) > 0
	})
}

// Each client forwards its own speaker's words; the room takes only those,
// logs them, and saves the transcript to R2 when the call ends.
func TestTranscriptKeepsEachSpeakerToTheirOwnWords(t *testing.T) {
	r := setup(t).newRoom(t)
	call := r.join(r.a)
	r.join(r.b)

	own := said(r.a, "The launch moves to Thursday.", false)
	if accepted := r.say(r.a, call.CallID, said(r.a, "The launch mo", true), own); accepted != 1 {
		t.Errorf("accepted = %d, want 1: partials are not kept", accepted)
	}
	if accepted := r.say(r.a, call.CallID, own); accepted != 0 {
		t.Errorf("the same segment again was accepted %d times", accepted)
	}
	forged := said(r.b, "Bilal agrees to cut the budget.", false)
	if status, text := r.do(r.a, http.MethodPost, "/call/transcript", map[string]any{"callId": call.CallID, "segments": []segment{forged}}, nil); status != http.StatusForbidden {
		t.Errorf("A forwarding B's words = %d %s, want 403", status, text)
	}
	if status, _ := r.do(r.a, http.MethodPost, "/call/transcript", map[string]any{"callId": "call_00000000000000000000000000000000", "segments": []segment{said(r.a, "x", false)}}, nil); status != http.StatusConflict {
		t.Errorf("a transcript for another call = %d, want 409", status)
	}
	r.say(r.b, call.CallID, said(r.b, "Thursday works for me.", false))

	segments := ofType(forCall(r.log(r.b), call.CallID), "transcript.segment")
	texts := map[string]string{}
	for _, event := range segments {
		text, _ := event.Data["text"].(string)
		texts[text] = event.Actor.ID
	}
	if texts["The launch moves to Thursday."] != r.a.id || texts["Thursday works for me."] != r.b.id {
		t.Errorf("segments in the log = %v", texts)
	}
	if _, ok := texts[forged.Text]; ok {
		t.Error("the forged segment reached the log")
	}

	r.must(r.a, http.MethodPost, "/call/end", map[string]any{}, nil)
	events := r.eventually(r.a, 30*time.Second, "call.transcript.saved", func(events []edgev1.Event) bool {
		for _, event := range ofType(forCall(events, call.CallID), "call.transcript.saved") {
			if event.Data["source"] == "live" {
				return true
			}
		}
		return false
	})
	var key string
	for _, event := range ofType(forCall(events, call.CallID), "call.transcript.saved") {
		if event.Data["source"] == "live" {
			key, _ = event.Data["key"].(string)
		}
	}
	prefix := "calls/" + r.org + "/" + string(r.room.Kind) + "/" + r.room.ID + "/" + call.CallID + "/"
	if key != prefix+"transcript.ndjson" {
		t.Errorf("transcript key = %q, want %stranscript.ndjson", key, prefix)
	}
	if body, ok := readR2(t, key); ok {
		if !bytes.Contains(body, []byte("The launch moves to Thursday.")) || !bytes.Contains(body, []byte("Thursday works for me.")) || bytes.Contains(body, []byte(forged.Text)) {
			t.Errorf("transcript in R2:\n%s", body)
		}
	}
}

// The agent stays quiet until addressed, then answers by voice to everyone
// in the call; talking over it stops it; dismissed, it says nothing.
func TestTheAgentAnswersWhenAddressed(t *testing.T) {
	r := setup(t).newRoom(t)
	call := r.join(r.a)
	r.join(r.b)
	earA, earB := r.listen(r.a), r.listen(r.b)
	for _, e := range []*ear{earA, earB} {
		state := e.expect(10*time.Second, "voice.state on connect", isType("voice.state"))
		if state.Present == nil || !*state.Present || state.Name != r.agentName {
			t.Errorf("%s: first frame = %+v", e.who, state)
		}
	}

	r.say(r.a, call.CallID, said(r.a, "Let's look at the numbers for October.", false))
	earA.never(quietFor, "an answer nobody asked for", isType("voice.speaking"))

	r.say(r.a, call.CallID, said(r.a, r.question("what is twelve times twelve? Just the number."), false))
	speaking, audio := earA.utterance(answerWithin)
	if !strings.Contains(speaking.Text, "144") && !strings.Contains(strings.ToLower(speaking.Text), "hundred") {
		t.Errorf("the agent said %q", speaking.Text)
	}
	heardByB, _ := earB.utterance(answerWithin)
	if heardByB.UtteranceID != speaking.UtteranceID {
		t.Error("A and B heard different utterances")
	}
	t.Logf("answer: %q in %d audio frames", speaking.Text, len(audio))

	// What the agent said is in the transcript.
	r.eventually(r.b, 10*time.Second, "the agent's words in the log", func(events []edgev1.Event) bool {
		for _, event := range ofType(forCall(events, call.CallID), "transcript.segment") {
			if event.Actor.Type == "agent" {
				return true
			}
		}
		return false
	})

	// Barge-in: B talks over a long answer.
	r.say(r.b, call.CallID, said(r.b, r.question("explain in six full sentences how a sailing boat can move against the wind."), false))
	long := earA.expect(answerWithin, "the long answer to start", isType("voice.speaking"))
	earA.expect(answerWithin, "the long answer's first audio", func(f frame) bool { return f.Type == "voice.audio" && f.UtteranceID == long.UtteranceID })
	r.say(r.a, call.CallID, said(r.a, "Sorry, hang on", true))
	stop := earB.expect(10*time.Second, "voice.stop", func(f frame) bool {
		return (f.Type == "voice.stop" && f.UtteranceID == long.UtteranceID) || (f.Type == "voice.audio" && f.UtteranceID == long.UtteranceID && f.Last)
	})
	if stop.Type != "voice.stop" || stop.Reason != "barge_in" {
		t.Errorf("talked over, the agent went on to the end: %+v", stop)
	}

	// Dismissed, it is quiet even when addressed; recalled, it answers again.
	var view struct {
		Call *callView `json:"call"`
	}
	r.must(r.b, http.MethodPost, "/call/agent", map[string]any{"present": false}, &view)
	if view.Call == nil || view.Call.Agent.Present {
		t.Fatalf("after dismissing, call = %+v", view.Call)
	}
	earA.expect(10*time.Second, "voice.state absent", func(f frame) bool { return f.Type == "voice.state" && f.Present != nil && !*f.Present })
	r.say(r.a, call.CallID, said(r.a, r.question("what is two plus two?"), false))
	earA.never(quietFor, "an answer while dismissed", isType("voice.speaking"))

	r.must(r.a, http.MethodPost, "/call/agent", map[string]any{"present": true}, nil)
	earB.expect(10*time.Second, "voice.state present", func(f frame) bool { return f.Type == "voice.state" && f.Present != nil && *f.Present })
	r.say(r.a, call.CallID, said(r.a, r.question("what is two plus two?"), false))
	earB.utterance(answerWithin)

	events := forCall(r.log(r.a), call.CallID)
	if len(ofType(events, "call.agent.left")) != 1 || len(ofType(events, "call.agent.joined")) != 1 {
		t.Errorf("agent events = %v", types(events))
	}

	// The call ending closes the sockets.
	r.must(r.a, http.MethodPost, "/call/end", map[string]any{}, nil)
	select {
	case <-earB.closed:
	case <-time.After(10 * time.Second):
		t.Error("B's socket stayed open after the call ended")
	}
}

// Real work goes to the member's own agent in Go and comes back to them
// alone: the room hears only that it was sent privately.
func TestDispatchRoundTrip(t *testing.T) {
	if os.Getenv("CALL_LIVE_DISPATCH") == "" {
		t.Skip("CALL_LIVE_DISPATCH is not set: a dispatch runs a real agent")
	}
	r := setup(t).newRoom(t)
	call := r.join(r.a)
	r.join(r.b)
	earA, earB := r.listen(r.a), r.listen(r.b)

	r.say(r.a, call.CallID, said(r.a, r.question("ask my agent to look through my recent threads and tell me in two lines what I worked on last."), false))
	events := r.eventually(r.a, answerWithin*2, "voice.dispatch", func(events []edgev1.Event) bool {
		return len(ofType(forCall(events, call.CallID), "voice.dispatch")) > 0
	})
	dispatched := ofType(forCall(events, call.CallID), "voice.dispatch")[0]
	dispatchID, _ := dispatched.Data["dispatchId"].(string)
	if dispatched.Data["runId"] == "" || dispatchID == "" {
		t.Fatalf("voice.dispatch = %+v", dispatched.Data)
	}

	answer := earA.expect(dispatchWithin, "the private answer", func(f frame) bool { return f.Type == "voice.private" })
	if answer.DispatchID != dispatchID || strings.TrimSpace(answer.Text) == "" {
		t.Errorf("voice.private = %+v", answer)
	}
	told := earB.expect(answerWithin, "the room hearing it was sent privately", func(f frame) bool {
		return f.Type == "voice.speaking" && strings.Contains(f.Text, "privately")
	})
	earB.never(time.Second, "A's private answer", isType("voice.private"))
	if strings.Contains(told.Text, answer.Text) {
		t.Error("the room heard the private answer")
	}

	result := func(m member) []edgev1.Event {
		var matched []edgev1.Event
		for _, event := range ofType(r.log(m), "voice.result") {
			if event.Data["dispatchId"] == dispatchID {
				matched = append(matched, event)
			}
		}
		return matched
	}
	if own := result(r.a); len(own) != 1 || !own[0].Private || own[0].Data["spoken"] != false {
		t.Errorf("A's voice.result = %+v", own)
	}
	if other := result(r.b); len(other) != 0 {
		t.Errorf("B can read A's private result: %+v", other)
	}
}

// A dropped socket reconnects into the same call and hears the agent; a
// rejoin keeps the member in the call once.
func TestReconnectAfterADrop(t *testing.T) {
	r := setup(t).newRoom(t)
	call := r.join(r.a)
	first := r.listen(r.a)
	first.expect(10*time.Second, "voice.state", isType("voice.state"))

	// The network goes, without a close frame.
	_ = first.conn.NetConn().Close()
	<-first.closed

	again := r.join(r.a)
	if again.CallID != call.CallID {
		t.Fatalf("after a drop the rejoin found call %s, not %s", again.CallID, call.CallID)
	}
	second := r.listen(r.a)
	second.expect(10*time.Second, "voice.state on reconnect", isType("voice.state"))
	r.say(r.a, call.CallID, said(r.a, r.question("what is three plus four?"), false))
	second.utterance(answerWithin)
	if got := r.call(r.a).members(); !slices.Equal(got, []string{r.a.id}) {
		t.Errorf("participants after reconnecting = %v", got)
	}
}

// A deploy of the edge mid-call: the call, its participants and the agent
// are still there after it, and the sockets come back.
func TestCallSurvivesADeploy(t *testing.T) {
	wait, err := time.ParseDuration(os.Getenv("CALL_LIVE_DEPLOY_WAIT"))
	if err != nil || wait <= 0 {
		t.Skip("CALL_LIVE_DEPLOY_WAIT is not set: someone has to deploy the edge while this waits")
	}
	r := setup(t).newRoom(t)
	call := r.join(r.a)
	r.join(r.b)
	ears := map[string]*ear{r.a.id: r.listen(r.a), r.b.id: r.listen(r.b)}
	t.Logf("call %s is up in %s/%s: deploy the edge now; waiting %s", call.CallID, r.room.Kind, r.room.ID, wait)

	reconnects := 0
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		for _, m := range []member{r.a, r.b} {
			select {
			case <-ears[m.id].closed:
				reconnects++
				t.Logf("%s's socket dropped at %s; reconnecting", m.label, time.Now().Format(time.TimeOnly))
				for attempt := 0; ; attempt++ {
					conn, _, err := r.dial(m)
					if err == nil {
						_ = conn.Close()
						break
					}
					if attempt == 30 {
						t.Fatalf("%s could not reconnect: %v", m.label, err)
					}
					time.Sleep(2 * time.Second)
				}
				ears[m.id] = r.listen(m)
			default:
			}
		}
		time.Sleep(time.Second)
	}
	t.Logf("%d reconnects during the wait", reconnects)

	view := r.call(r.b)
	if view == nil || view.CallID != call.CallID || len(view.Participants) != 2 || !view.Agent.Present {
		t.Fatalf("after the deploy, call = %+v", view)
	}
	r.say(r.a, call.CallID, said(r.a, r.question("are you still with us? Answer yes or no."), false))
	ears[r.b.id].utterance(answerWithin)
}

// readR2 reads key back when the test was given R2 access.
func readR2(t *testing.T, key string) ([]byte, bool) {
	t.Helper()
	endpoint, bucket := os.Getenv("CALL_LIVE_R2_ENDPOINT"), os.Getenv("CALL_LIVE_R2_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Log("no CALL_LIVE_R2_*: the transcript in R2 is not read back")
		return nil, false
	}
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(os.Getenv("CALL_LIVE_R2_ACCESS_KEY_ID"), os.Getenv("CALL_LIVE_R2_SECRET_ACCESS_KEY"), ""),
		UsePathStyle: true,
	})
	output, err := client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Errorf("read %s from R2: %v", key, err)
		return nil, false
	}
	defer output.Body.Close()
	var buffer bytes.Buffer
	_, _ = buffer.ReadFrom(output.Body)
	for _, line := range bytes.Split(bytes.TrimSpace(buffer.Bytes()), []byte("\n")) {
		if !json.Valid(line) {
			t.Errorf("a transcript line is not JSON: %s", line)
		}
	}
	return buffer.Bytes(), true
}
