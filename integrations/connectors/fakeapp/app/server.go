// Package app is the fakeapp connector's server. It is a package so the gate's
// broken fixtures can wrap it.
package app

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

// ManifestJSON is the connector's manifest.
//
//go:embed manifest.json
var ManifestJSON string

// Host is the app's host name; it is what the egress proxy forwards to.
const Host = "api.fakeapp.example.com"

// egressHostHeader names the upstream host the broker forwards to (broker-proxy.json).
const egressHostHeader = "x-oryxa-egress-host"

// notes is what a request without egress sees, so the host's tests need no broker.
var notes = []string{"first", "second", "third"}

// Server serves the contract.
type Server struct {
	pb.UnimplementedConnectorServer
}

// New returns the server.
func New() *Server { return &Server{} }

func (*Server) Describe(context.Context, *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{ManifestJson: ManifestJSON, ImplementationVersion: "fakeapp-2"}, nil
}

// call sends one request to the app through the broker with the connector's handle.
func call(ctx context.Context, e *pb.Egress, method, path string, body any) (int, []byte, http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.GetBrokerOrigin()+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set(egressHostHeader, Host)
	req.Header.Set("Authorization", "Bearer "+e.GetHandles()["default"])
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, resp.Header, err
}

func errorResponse(code, message string) *pb.InvokeResponse {
	return &pb.InvokeResponse{Result: &pb.InvokeResponse_Error{Error: &pb.ToolError{Code: code, Message: message}}}
}

func output(v any) *pb.InvokeResponse {
	b, _ := json.Marshal(v)
	return &pb.InvokeResponse{Result: &pb.InvokeResponse_OutputJson{OutputJson: string(b)}}
}

// statusError maps an app status to the contract's tool error codes.
func statusError(status int) *pb.InvokeResponse {
	switch status {
	case http.StatusNotFound:
		return errorResponse("not_found", "no such note")
	case http.StatusTooManyRequests:
		return errorResponse("rate_limited", "the app is rate limiting; try later")
	}
	return errorResponse("provider_error", fmt.Sprintf("the app answered %d", status))
}

func (*Server) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeResponse, error) {
	var in struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(req.GetInputJson()), &in); err != nil {
		return errorResponse("invalid_input", "the input is not valid"), nil
	}
	switch req.GetTool() {
	case "fakeapp.echo":
		if in.Text == "" {
			return errorResponse("invalid_input", "text must not be empty"), nil
		}
		return output(map[string]string{"text": in.Text}), nil
	case "fakeapp.get_note":
		status, body, _, err := call(ctx, req.GetEgress(), http.MethodGet, "/notes/"+url.PathEscape(in.ID), nil)
		if err != nil {
			return errorResponse("provider_error", "the app could not be reached"), nil
		}
		if status != http.StatusOK {
			return statusError(status), nil
		}
		var n struct{ ID, Text string }
		if json.Unmarshal(body, &n) != nil || n.ID == "" {
			return errorResponse("provider_error", "the app sent an unreadable note"), nil
		}
		return output(map[string]string{"id": n.ID, "text": n.Text}), nil
	case "fakeapp.add_note":
		status, body, _, err := call(ctx, req.GetEgress(), http.MethodPost, "/notes", map[string]string{"text": in.Text})
		if err != nil {
			return errorResponse("provider_error", "the app could not be reached"), nil
		}
		if status != http.StatusCreated {
			return statusError(status), nil
		}
		var n struct{ ID string }
		if json.Unmarshal(body, &n) != nil || n.ID == "" {
			return errorResponse("provider_error", "the app sent an unreadable note"), nil
		}
		return output(map[string]string{"id": n.ID}), nil
	}
	return errorResponse("not_found", "no such tool"), nil
}

// page is the app's change feed: notes in the order they changed. seq is the
// checkpoint; a deleted note is a tombstone entry.
type page struct {
	Notes []struct {
		ID      string `json:"id"`
		Seq     int    `json:"seq"`
		Text    string `json:"text"`
		Deleted bool   `json:"deleted"`
	} `json:"notes"`
	HasMore bool `json:"has_more"`
}

// maxRetryWait caps how long a Retry-After may make a run wait.
const maxRetryWait = 5 * time.Second

// Sync reads the change feed after the checkpoint (empty for a full sync), a
// page at a time, and checkpoints after each page.
func (*Server) Sync(req *pb.SyncRequest, stream pb.Connector_SyncServer) error {
	if req.GetEgress() == nil {
		return syncStatic(req, stream)
	}
	ctx := stream.Context()
	after := 0
	if len(req.GetCheckpoint()) > 0 {
		var err error
		if after, err = strconv.Atoi(string(req.GetCheckpoint())); err != nil {
			return errors.New("fakeapp: unreadable checkpoint")
		}
	}
	for {
		status, body, header, err := call(ctx, req.GetEgress(), http.MethodGet, "/notes?after="+strconv.Itoa(after), nil)
		if err != nil {
			return err
		}
		if status == http.StatusTooManyRequests {
			wait, _ := strconv.Atoi(header.Get("Retry-After"))
			select {
			case <-time.After(min(time.Duration(wait)*time.Second, maxRetryWait)):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		var p page
		if status != http.StatusOK || json.Unmarshal(body, &p) != nil {
			return fmt.Errorf("fakeapp: the app answered %d", status)
		}
		for _, n := range p.Notes {
			var ev *pb.SyncEvent
			if n.Deleted {
				ev = &pb.SyncEvent{Event: &pb.SyncEvent_Delete{Delete: &pb.Delete{Id: n.ID}}}
			} else {
				item := &pb.Item{
					Id: n.ID, Kind: "note", Title: "Note " + n.ID, Content: []byte(n.Text),
					Url: "https://app.fakeapp.example.com/notes/" + n.ID, UpdatedAtMs: int64(n.Seq) * 1000,
				}
				ev = &pb.SyncEvent{Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: item, Acl: &pb.Acl{AnyoneInTenant: true}}}}
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
			after = max(after, n.Seq)
		}
		cp := &pb.Checkpoint{Cursor: []byte(strconv.Itoa(after))}
		if err := stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Checkpoint{Checkpoint: cp}}); err != nil {
			return err
		}
		if !p.HasMore {
			return nil
		}
	}
}

// syncStatic emits the fixed notes after the checkpoint, then a checkpoint at the last one.
func syncStatic(req *pb.SyncRequest, stream pb.Connector_SyncServer) error {
	after := 0
	if req.GetMode() == pb.SyncMode_SYNC_MODE_INCREMENTAL {
		after, _ = strconv.Atoi(string(req.GetCheckpoint()))
	}
	for i := after; i < len(notes); i++ {
		id := strconv.Itoa(i + 1)
		item := &pb.Item{Id: id, Kind: "note", Title: "Note " + id, Content: []byte(notes[i]),
			Url: "https://app.fakeapp.example.com/notes/" + id, UpdatedAtMs: int64(i+1) * 1000}
		if err := stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: item, Acl: &pb.Acl{AnyoneInTenant: true}}}}); err != nil {
			return err
		}
	}
	cp := &pb.Checkpoint{Cursor: []byte(strconv.Itoa(len(notes)))}
	return stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Checkpoint{Checkpoint: cp}})
}
