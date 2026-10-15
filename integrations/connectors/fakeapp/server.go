package main

import (
	"context"
	"encoding/json"
	"strconv"

	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

// notes is the whole data set; a note's id is its position, which is also the checkpoint.
var notes = []string{"first", "second", "third"}

type server struct {
	pb.UnimplementedConnectorServer
}

func (server) Describe(context.Context, *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{ManifestJson: manifestJSON, ImplementationVersion: "fakeapp-1"}, nil
}

func (server) Invoke(_ context.Context, req *pb.InvokeRequest) (*pb.InvokeResponse, error) {
	if req.GetTool() != "fakeapp.echo" {
		return errorResponse("not_found", "no such tool"), nil
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(req.GetInputJson()), &in); err != nil {
		return errorResponse("invalid_input", "text is required"), nil
	}
	out, _ := json.Marshal(in)
	return &pb.InvokeResponse{Result: &pb.InvokeResponse_OutputJson{OutputJson: string(out)}}, nil
}

func errorResponse(code, message string) *pb.InvokeResponse {
	return &pb.InvokeResponse{Result: &pb.InvokeResponse_Error{Error: &pb.ToolError{Code: code, Message: message}}}
}

// Sync emits the notes after the checkpoint, then a checkpoint at the last one.
func (server) Sync(req *pb.SyncRequest, stream pb.Connector_SyncServer) error {
	after := 0
	if req.GetMode() == pb.SyncMode_SYNC_MODE_INCREMENTAL {
		after, _ = strconv.Atoi(string(req.GetCheckpoint()))
	}
	for i := after; i < len(notes); i++ {
		item := &pb.Item{
			Id: strconv.Itoa(i + 1), Kind: "note", Title: "Note " + strconv.Itoa(i+1), Content: []byte(notes[i]),
			Url: "https://app.fakeapp.example.com/notes/" + strconv.Itoa(i+1), UpdatedAtMs: int64(i+1) * 1000,
		}
		acl := &pb.Acl{AnyoneInTenant: true}
		if err := stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: item, Acl: acl}}}); err != nil {
			return err
		}
	}
	cp := &pb.Checkpoint{Cursor: []byte(strconv.Itoa(len(notes)))}
	return stream.Send(&pb.SyncEvent{Event: &pb.SyncEvent_Checkpoint{Checkpoint: cp}})
}
