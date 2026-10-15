package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/host"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

// Case kinds. A stream needs full, empty and rate_limit cases; an incremental
// stream also incremental and resume; a stream that detects deletes also delete.
const (
	kindFull        = "full"
	kindIncremental = "incremental"
	kindResume      = "resume"
	kindEmpty       = "empty"
	kindDelete      = "delete"
	kindRateLimit   = "rate_limit"
)

// scenario is testdata/conformance.json: what each recorded run must produce.
type scenario struct {
	Streams []streamCase `json:"streams"`
	Tools   []toolCase   `json:"tools"`
}

// streamCase is one sync run against a cassette (testdata/cassettes/<name>.json).
type streamCase struct {
	Name     string `json:"name"`
	Stream   string `json:"stream"`
	Kind     string `json:"kind"`
	Cassette string `json:"cassette"`
	// After names an earlier case whose final checkpoint this run resumes from, as an incremental sync.
	After string `json:"after,omitempty"`
	// ResumeCassette answers the second leg of a resume case.
	ResumeCassette string `json:"resume_cassette,omitempty"`
	// Upserts and Deletes are the item ids the run must emit, once each.
	Upserts []string `json:"upserts,omitempty"`
	Deletes []string `json:"deletes,omitempty"`
}

// toolCase is one call: it either returns Output or fails with the ToolError code Error.
type toolCase struct {
	Tool     string          `json:"tool"`
	Cassette string          `json:"cassette,omitempty"`
	Input    json.RawMessage `json:"input"`
	Output   json.RawMessage `json:"output,omitempty"`
	Error    string          `json:"error,omitempty"`
}

func loadScenario(dir string) (*scenario, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "testdata", "conformance.json"))
	if err != nil {
		return nil, err
	}
	var sc scenario
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sc); err != nil {
		return nil, fmt.Errorf("testdata/conformance.json: %w", err)
	}
	return &sc, nil
}

// maxEvents ends a run that never finishes its pagination.
const maxEvents = 10000

var (
	errNeverEnds = errors.New("more than 10000 events and no end: pagination never ends")
	errStop      = errors.New("gate: stop after the first checkpoint")
)

// replayer runs cases through the host, each against a fresh cassette broker.
type replayer struct {
	h           *host.Host
	m           *manifest.Manifest
	dir         string
	checkpoints map[string][]byte // the last checkpoint of each case that ran
}

// outcome is what one run emitted.
type outcome struct {
	upserts, deletes []string
	checkpoint       []byte
	// committed is what was emitted up to the first checkpoint.
	committedUpserts, committedDeletes []string
	err                                error
	b                                  *broker
}

func (r *replayer) cassette(name string) (*broker, error) {
	var es []entry
	if name != "" {
		var err error
		if es, err = loadCassette(filepath.Join(r.dir, "testdata", "cassettes", name+".json")); err != nil {
			return nil, err
		}
	}
	return newBroker(es), nil
}

func egressFor(b *broker) *pb.Egress {
	return &pb.Egress{BrokerOrigin: b.srv.URL, Handles: map[string]string{"default": "gate-handle"}}
}

// sync runs one stream. With stopAtFirst, it ends the run at the first
// checkpoint, as a crash after a page would.
func (r *replayer) sync(ctx context.Context, c streamCase, cassette string, mode pb.SyncMode, cp []byte, stopAtFirst bool) (*outcome, error) {
	b, err := r.cassette(cassette)
	if err != nil {
		return nil, err
	}
	o := &outcome{b: b}
	var events int
	var firstCP bool
	req := &pb.SyncRequest{Stream: c.Stream, Mode: mode, Checkpoint: cp, Egress: egressFor(b)}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	o.err = r.h.Sync(ctx, r.m.ID, req, func(ev *pb.SyncEvent) error {
		if events++; events > maxEvents {
			return errNeverEnds
		}
		switch e := ev.GetEvent().(type) {
		case *pb.SyncEvent_Upsert:
			o.upserts = append(o.upserts, e.Upsert.GetItem().GetId())
		case *pb.SyncEvent_Delete:
			o.deletes = append(o.deletes, e.Delete.GetId())
		case *pb.SyncEvent_Checkpoint:
			o.checkpoint = e.Checkpoint.GetCursor()
			if !firstCP {
				firstCP = true
				o.committedUpserts, o.committedDeletes = slices.Clone(o.upserts), slices.Clone(o.deletes)
				if stopAtFirst {
					return errStop
				}
			}
		}
		return nil
	})
	return o, nil
}

func sameSet(got, want []string) string {
	g, w := slices.Clone(got), slices.Clone(want)
	sort.Strings(g)
	sort.Strings(w)
	if slices.Equal(g, w) {
		return ""
	}
	return fmt.Sprintf("got %v, want %v", g, w)
}

func (r *replayer) run(ctx context.Context, sc *scenario) []Failure {
	var out []Failure
	fail := func(check, format string, args ...any) {
		out = append(out, Failure{check, fmt.Sprintf(format, args...)})
	}

	have := map[string][]string{} // stream -> kinds covered
	for _, c := range sc.Streams {
		what := "stream " + c.Stream + " case " + c.Name
		if !slices.ContainsFunc(r.m.Streams, func(s manifest.Stream) bool { return s.Name == c.Stream }) {
			fail("replay", "%s: the manifest has no such stream", what)
			continue
		}
		have[c.Stream] = append(have[c.Stream], c.Kind)
		switch c.Kind {
		case kindResume:
			out = append(out, r.resume(ctx, c, what)...)
		case kindFull, kindIncremental, kindEmpty, kindDelete, kindRateLimit:
			out = append(out, r.single(ctx, c, what)...)
		default:
			fail("replay", "%s: unknown kind %q", what, c.Kind)
		}
	}
	for _, s := range r.m.Streams {
		want := []string{kindFull, kindEmpty, kindRateLimit}
		if slices.Contains(s.Modes, "incremental") {
			want = append(want, kindIncremental, kindResume)
		}
		if s.Deletes != "" {
			want = append(want, kindDelete)
		}
		for _, k := range want {
			if !slices.Contains(have[s.Name], k) {
				fail("replay", "stream %s has no %s case in testdata/conformance.json", s.Name, k)
			}
		}
	}
	out = append(out, r.tools(ctx, sc)...)
	return out
}

func (r *replayer) single(ctx context.Context, c streamCase, what string) []Failure {
	var out []Failure
	fail := func(check, format string, args ...any) {
		out = append(out, Failure{check, what + ": " + fmt.Sprintf(format, args...)})
	}
	mode, cp := pb.SyncMode_SYNC_MODE_FULL, []byte(nil)
	if c.After != "" {
		var ok bool
		if cp, ok = r.checkpoints[c.After]; !ok {
			fail("replay", "after %q: no such earlier case, or it emitted no checkpoint", c.After)
			return out
		}
		mode = pb.SyncMode_SYNC_MODE_INCREMENTAL
	}
	o, err := r.sync(ctx, c, c.Cassette, mode, cp, false)
	if err != nil {
		fail("replay", "%v", err)
		return out
	}
	defer o.b.close()
	if o.err != nil {
		fail(failCheck(o.err), "%v", o.err)
		return out
	}
	r.checkpoints[c.Name] = o.checkpoint
	for _, m := range o.b.misses() {
		fail("replay", "request not in the cassette: %s", m)
	}
	if d := sameSet(o.upserts, c.Upserts); d != "" {
		fail("replay", "upserts: %s", d)
	}
	if d := sameSet(o.deletes, c.Deletes); d != "" {
		fail(deleteCheck(c), "deletes: %s (a delete must be a tombstone)", d)
	}
	if c.Kind == kindRateLimit {
		if p := o.b.rateLimitProblem(); p != "" {
			fail("rate limit", "%s", p)
		}
	}
	return out
}

func deleteCheck(c streamCase) string {
	if c.Kind == kindDelete {
		return "tombstone"
	}
	return "replay"
}

func failCheck(err error) string {
	if errors.Is(err, errNeverEnds) {
		return "pagination"
	}
	return "replay"
}

// resume runs a full sync that dies at its first checkpoint, then resumes
// from that checkpoint. Together the legs must emit every item once.
func (r *replayer) resume(ctx context.Context, c streamCase, what string) []Failure {
	var out []Failure
	fail := func(check, format string, args ...any) {
		out = append(out, Failure{check, what + ": " + fmt.Sprintf(format, args...)})
	}
	if c.ResumeCassette == "" {
		fail("replay", "a resume case needs resume_cassette")
		return out
	}
	one, err := r.sync(ctx, c, c.Cassette, pb.SyncMode_SYNC_MODE_FULL, nil, true)
	if err != nil {
		fail("replay", "%v", err)
		return out
	}
	defer one.b.close()
	if one.err != nil && !errors.Is(one.err, errStop) {
		fail(failCheck(one.err), "first leg: %v", one.err)
		return out
	}
	if one.checkpoint == nil {
		fail("resume", "the first leg emitted no checkpoint")
		return out
	}
	two, err := r.sync(ctx, c, c.ResumeCassette, pb.SyncMode_SYNC_MODE_INCREMENTAL, one.checkpoint, false)
	if err != nil {
		fail("replay", "%v", err)
		return out
	}
	defer two.b.close()
	if two.err != nil {
		fail(failCheck(two.err), "resumed leg: %v", two.err)
		return out
	}
	for _, m := range append(one.b.misses(), two.b.misses()...) {
		fail("replay", "request not in the cassette: %s", m)
	}
	// The first leg's committed items plus the resumed leg's, with a duplicate or a gap both failing.
	got := append(slices.Clone(one.committedUpserts), two.upserts...)
	if d := sameSet(got, c.Upserts); d != "" {
		fail("resume", "upserts across the crash: %s", d)
	}
	gotDeletes := append(slices.Clone(one.committedDeletes), two.deletes...)
	if d := sameSet(gotDeletes, c.Deletes); d != "" {
		fail("resume", "deletes across the crash: %s", d)
	}
	return out
}

func (r *replayer) tools(ctx context.Context, sc *scenario) []Failure {
	var out []Failure
	ok, bad := map[string]bool{}, map[string]bool{}
	effects := map[string]string{}
	for _, t := range r.m.Tools {
		effects[t.Name] = t.Effect
	}
	for i, c := range sc.Tools {
		what := fmt.Sprintf("tool %s case %d", c.Tool, i+1)
		fail := func(check, format string, args ...any) {
			out = append(out, Failure{check, what + ": " + fmt.Sprintf(format, args...)})
		}
		effect, known := effects[c.Tool]
		if !known {
			fail("replay", "the manifest has no such tool")
			continue
		}
		if (c.Output == nil) == (c.Error == "") {
			fail("replay", "a case has an output or an error, not both or neither")
			continue
		}
		b, err := r.cassette(c.Cassette)
		if err != nil {
			fail("replay", "%v", err)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		resp, err := r.h.Invoke(cctx, r.m.ID, &pb.InvokeRequest{Tool: c.Tool, InputJson: string(c.Input), Egress: egressFor(b)})
		cancel()
		methods, misses := b.methods(), b.misses()
		b.close()
		if err != nil {
			fail("replay", "%v", err)
			continue
		}
		for _, m := range misses {
			fail("replay", "request not in the cassette: %s", m)
		}
		if effect == "read" {
			if i := slices.IndexFunc(methods, func(m string) bool { return m != http.MethodGet && m != http.MethodHead }); i >= 0 {
				fail("effects", "declared read, but the call sent %s", methods[i])
			}
		}
		switch {
		case c.Error != "":
			if got := resp.GetError().GetCode(); got != c.Error {
				fail("replay", "error code %q, want %q", got, c.Error)
			}
			bad[c.Tool] = true
		default:
			if e := resp.GetError(); e != nil {
				fail("replay", "failed with %s: %s", e.Code, e.Message)
			} else if !sameJSON(resp.GetOutputJson(), c.Output) {
				fail("replay", "output %s, want %s", resp.GetOutputJson(), c.Output)
			}
			ok[c.Tool] = true
		}
	}
	for _, t := range r.m.Tools {
		switch {
		case !ok[t.Name]:
			out = append(out, Failure{"replay", "tool " + t.Name + " has no happy-path case"})
		case !bad[t.Name]:
			out = append(out, Failure{"replay", "tool " + t.Name + " has no error case"})
		}
	}
	return out
}

func sameJSON(a string, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	nx, _ := json.Marshal(x)
	ny, _ := json.Marshal(y)
	return bytes.Equal(nx, ny)
}
