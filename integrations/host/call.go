package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/grpc"

	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

// Limits from the contract (connector.proto).
const (
	maxOutput     = 256 << 10
	maxContent    = 1 << 20
	maxCheckpoint = 64 << 10
	maxTitle      = 1 << 10
	maxMetadata   = 32
	maxErrMessage = 1 << 10
)

var (
	connectorIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	itemIDRE      = regexp.MustCompile(`^[A-Za-z0-9._:@/+=-]{1,256}$`)
	kindRE        = regexp.MustCompile(`^[a-z_]{1,64}$`)
	errCodeRE     = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

func validConnectorID(id string) bool { return connectorIDRE.MatchString(id) }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidMessage, fmt.Sprintf(format, args...))
}

type toolSchemas struct{ in, out *jsonschema.Resolved }

// compileTools resolves every tool's schemas once, so a call is checked
// without parsing. A schema Go can't resolve refuses the connector.
func compileTools(m *manifest.Manifest) (map[string]toolSchemas, error) {
	tools := make(map[string]toolSchemas, len(m.Tools))
	for _, t := range m.Tools {
		var ts toolSchemas
		for _, s := range []struct {
			name string
			raw  json.RawMessage
			dst  **jsonschema.Resolved
		}{{"input_schema", t.InputSchema, &ts.in}, {"output_schema", t.OutputSchema, &ts.out}} {
			var schema jsonschema.Schema
			if err := json.Unmarshal(s.raw, &schema); err != nil {
				return nil, fmt.Errorf("describe: tool %s %s: %w", t.Name, s.name, err)
			}
			r, err := schema.Resolve(nil)
			if err != nil {
				return nil, fmt.Errorf("describe: tool %s %s: %w", t.Name, s.name, err)
			}
			*s.dst = r
		}
		tools[t.Name] = ts
	}
	return tools, nil
}

func validateJSON(r *jsonschema.Resolved, raw string) error {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return errors.New("not JSON")
	}
	return r.Validate(v)
}

// Invoke runs one of the connector's tools. The input must satisfy the tool's
// input_schema and a successful output its output_schema, within 256 KiB.
// A tool's own failure comes back as the response's ToolError.
func (h *Host) Invoke(ctx context.Context, id string, req *pb.InvokeRequest) (*pb.InvokeResponse, error) {
	c, ok := h.get(id)
	if !ok {
		return nil, ErrUnknown
	}
	ts, ok := c.tools[req.GetTool()]
	if !ok {
		return nil, invalid("%s has no tool %q", id, req.GetTool())
	}
	if err := validateJSON(ts.in, req.GetInputJson()); err != nil {
		return nil, invalid("input for %s: %v", req.GetTool(), err)
	}
	client, err := c.ready()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.InvokeTimeout)
	defer cancel()
	resp, err := client.Invoke(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("host: invoke %s: %w", req.GetTool(), err)
	}
	switch r := resp.GetResult().(type) {
	case *pb.InvokeResponse_Error:
		e := r.Error
		if e == nil || !errCodeRE.MatchString(e.GetCode()) || len(e.GetMessage()) > maxErrMessage {
			return nil, invalid("%s returned a malformed error", req.GetTool())
		}
	case *pb.InvokeResponse_OutputJson:
		if len(r.OutputJson) > maxOutput {
			return nil, invalid("output of %s is over %d bytes", req.GetTool(), maxOutput)
		}
		if err := validateJSON(ts.out, r.OutputJson); err != nil {
			return nil, invalid("output of %s: %v", req.GetTool(), err)
		}
	default:
		return nil, invalid("%s returned neither an output nor an error", req.GetTool())
	}
	return resp, nil
}

var syncModes = map[pb.SyncMode]string{
	pb.SyncMode_SYNC_MODE_FULL: "full", pb.SyncMode_SYNC_MODE_INCREMENTAL: "incremental", pb.SyncMode_SYNC_MODE_WEBHOOK: "webhook",
}

// Sync runs one stream and hands each validated event to emit, in order.
// emit sees a Checkpoint only after the events it covers; an error from emit
// ends the run. A run that ends without a final Checkpoint, goes silent for
// longer than the idle limit, or sends an invalid event fails.
func (h *Host) Sync(ctx context.Context, id string, req *pb.SyncRequest, emit func(*pb.SyncEvent) error) error {
	c, ok := h.get(id)
	if !ok {
		return ErrUnknown
	}
	s, ok := c.streams[req.GetStream()]
	if !ok {
		return invalid("%s has no stream %q", id, req.GetStream())
	}
	mode, ok := syncModes[req.GetMode()]
	if !ok || !contains(s.Modes, mode) {
		return invalid("stream %s does not support mode %v", s.Name, req.GetMode())
	}
	if req.GetMode() == pb.SyncMode_SYNC_MODE_FULL && len(req.GetCheckpoint()) > 0 {
		return invalid("a full sync starts without a checkpoint")
	}
	if len(req.GetCheckpoint()) > maxCheckpoint {
		return invalid("checkpoint is over %d bytes", maxCheckpoint)
	}
	client, err := c.ready()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.SyncLimit)
	defer cancel()
	var idled atomic.Bool
	idle := time.AfterFunc(h.cfg.SyncIdle, func() { idled.Store(true); cancel() })
	defer idle.Stop()

	stream, err := client.Sync(ctx, req, grpc.WaitForReady(false))
	if err != nil {
		return fmt.Errorf("host: sync %s: %w", s.Name, err)
	}
	var last *pb.SyncEvent
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if idled.Load() {
				return fmt.Errorf("host: sync %s: silent for %s", s.Name, h.cfg.SyncIdle)
			}
			return fmt.Errorf("host: sync %s: %w", s.Name, err)
		}
		idle.Reset(h.cfg.SyncIdle)
		if err := validateEvent(ev); err != nil {
			return err
		}
		if err := emit(ev); err != nil {
			return err
		}
		last = ev
	}
	if last.GetCheckpoint() == nil {
		return invalid("sync of %s ended without a final checkpoint", s.Name)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func validateEvent(ev *pb.SyncEvent) error {
	switch e := ev.GetEvent().(type) {
	case *pb.SyncEvent_Upsert:
		if e.Upsert.GetItem() == nil {
			return invalid("upsert without an item")
		}
		if e.Upsert.GetAcl() == nil {
			return invalid("upsert of %q without an ACL", e.Upsert.Item.GetId()) // no ACL: visible to no one
		}
		if err := validateItem(e.Upsert.Item); err != nil {
			return err
		}
		return validateACL(e.Upsert.Acl)
	case *pb.SyncEvent_Delete:
		return validateItemID("delete id", e.Delete.GetId())
	case *pb.SyncEvent_SetAcl:
		if e.SetAcl.GetAcl() == nil {
			return invalid("set_acl without an ACL")
		}
		if err := validateItemID("set_acl id", e.SetAcl.GetId()); err != nil {
			return err
		}
		return validateACL(e.SetAcl.Acl)
	case *pb.SyncEvent_Checkpoint:
		if len(e.Checkpoint.GetCursor()) > maxCheckpoint {
			return invalid("checkpoint is over %d bytes", maxCheckpoint)
		}
		return nil
	}
	return invalid("event of no known type")
}

func validateItemID(what, id string) error {
	if !itemIDRE.MatchString(id) || strings.Contains(id, "..") {
		return invalid("%s is not a valid id", what)
	}
	return nil
}

func validateItem(it *pb.Item) error {
	if err := validateItemID("item id", it.GetId()); err != nil {
		return err
	}
	if it.GetParentId() != "" {
		if err := validateItemID("parent id", it.GetParentId()); err != nil {
			return err
		}
	}
	switch {
	case !kindRE.MatchString(it.GetKind()):
		return invalid("item %q: kind must be lowercase letters and underscores", it.GetId())
	case len(it.GetTitle()) > maxTitle:
		return invalid("item %q: title is over %d bytes", it.GetId(), maxTitle)
	case len(it.GetContent()) > maxContent:
		return invalid("item %q: content is over %d bytes", it.GetId(), maxContent)
	case len(it.GetMetadata()) > maxMetadata:
		return invalid("item %q: more than %d metadata entries", it.GetId(), maxMetadata)
	}
	if u := it.GetUrl(); u != "" {
		if p, err := url.Parse(u); err != nil || p.Scheme != "https" || p.Host == "" {
			return invalid("item %q: url must be https", it.GetId())
		}
	}
	return nil
}

func validateACL(a *pb.Acl) error {
	for _, p := range append(append([]string{}, a.GetUsers()...), a.GetGroups()...) {
		if p == "" {
			return invalid("ACL has an empty principal")
		}
	}
	return nil
}
