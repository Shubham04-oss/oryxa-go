// Command brokenapp is fakeapp with one defect, chosen at build time with
// -ldflags "-X main.defect=<name>", so each gate check has something to fail.
package main

import (
	"context"
	"encoding/json"
	"log"
	"strconv"

	"github.com/Shubham04-oss/oryxa-go/integrations/connectors/fakeapp/app"
	"github.com/Shubham04-oss/oryxa-go/integrations/gosdk"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

var defect string

type server struct{ *app.Server }

func main() {
	if err := gosdk.Main(server{app.New()}, "fakeapp"); err != nil {
		log.Fatal(err)
	}
}

func (s server) Describe(ctx context.Context, req *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(app.ManifestJSON), &m); err != nil {
		return nil, err
	}
	tools := m["tools"].([]any)
	switch defect {
	case "effect": // add_note POSTs but says it only reads
		for _, t := range tools {
			if t := t.(map[string]any); t["name"] == "fakeapp.add_note" {
				t["effect"] = "read"
			}
		}
	case "description":
		tools[0].(map[string]any)["description"] = "The echo tool."
	case "budget":
		for i := range 16 {
			t := map[string]any{}
			b, _ := json.Marshal(tools[0])
			_ = json.Unmarshal(b, &t)
			t["name"] = "fakeapp.extra_" + strconv.Itoa(i)
			t["description"] = "Echo the text number " + strconv.Itoa(i) + " back. Use when a test needs one more tool."
			tools = append(tools, t)
		}
		m["tools"] = tools
	}
	raw, _ := json.Marshal(m)
	return &pb.DescribeResponse{ManifestJson: string(raw), ImplementationVersion: "brokenapp"}, nil
}

func (s server) Sync(req *pb.SyncRequest, stream pb.Connector_SyncServer) error {
	switch defect {
	case "pagination": // the same page, forever
		for {
			item := &pb.Item{Id: "1", Kind: "note", Title: "Note 1"}
			if err := stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: item, Acl: &pb.Acl{AnyoneInTenant: true}}}}); err != nil {
				return err
			}
			if err := stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Checkpoint{Checkpoint: &pb.Checkpoint{Cursor: []byte("1")}}}); err != nil {
				return err
			}
		}
	case "resume": // a resumed run loses its first change
		if len(req.GetCheckpoint()) > 0 {
			stream = &droppingFirst{Connector_SyncServer: stream}
		}
	case "delete": // a delete goes out as an upsert
		stream = untombstoned{stream}
	}
	return s.Server.Sync(req, stream)
}

// untombstoned turns each Delete into an Upsert of the same id.
type untombstoned struct{ pb.Connector_SyncServer }

func (u untombstoned) Send(ev *pb.SyncEvent) error {
	if d := ev.GetDelete(); d != nil {
		item := &pb.Item{Id: d.Id, Kind: "note", Title: "Note " + d.Id}
		ev = &pb.SyncEvent{Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: item, Acl: &pb.Acl{AnyoneInTenant: true}}}}
	}
	return u.Connector_SyncServer.Send(ev)
}

// droppingFirst swallows the first Upsert.
type droppingFirst struct {
	pb.Connector_SyncServer
	dropped bool
}

func (d *droppingFirst) Send(ev *pb.SyncEvent) error {
	if ev.GetUpsert() != nil && !d.dropped {
		d.dropped = true
		return nil
	}
	return d.Connector_SyncServer.Send(ev)
}
