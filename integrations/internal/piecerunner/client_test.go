package piecerunner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

type fakeLambda struct {
	requests []runnerRequest
	outputs  [][]byte
}

func TestRunTriggerSendsOnlyScopedExecutionContext(t *testing.T) {
	t.Parallel()
	lambdaClient := &fakeLambda{outputs: [][]byte{[]byte(`{"ok":true,"result":{"output":[{"channel":"C123"}],"storeMutations":[{"operation":"put","key":"cursor","value":"2"}],"listeners":[]}}`)}}
	client := Client{Lambda: lambdaClient, FunctionName: "runner"}
	result, err := client.RunTrigger(context.Background(), piece.TriggerInvocation{
		InvocationID: "exe-1", OrganizationID: "org-1", PieceName: "piece", PieceVersion: "1",
		Operation: "trigger.run", TriggerName: "hook", Auth: json.RawMessage(`{"access_token":"one-token"}`),
		Props: json.RawMessage(`{"ignoreBots":true}`), InitialStore: map[string]any{"cursor": "1"},
		Payload: webhooks.Envelope{Method: "POST", Headers: map[string]string{"x-event": "one"}, Body: map[string]any{"event": "value"}, RawBody: []byte(`{"event":"value"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lambdaClient.requests) != 1 {
		t.Fatalf("calls = %d", len(lambdaClient.requests))
	}
	request := lambdaClient.requests[0]
	if request.Operation != "trigger.run" || request.TriggerName != "hook" || string(request.Auth) != `{"access_token":"one-token"}` || request.ProviderPayload.RawBody != `{"event":"value"}` {
		t.Fatalf("request = %+v", request)
	}
	if len(result.StoreMutations) != 1 || result.StoreMutations[0].Key != "cursor" || string(result.Output) != `[{"channel":"C123"}]` {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunActionSendsOnlyOneConnectionAndApprovedInput(t *testing.T) {
	t.Parallel()
	lambdaClient := &fakeLambda{outputs: [][]byte{[]byte(`{"ok":true,"result":{"output":{"ok":true,"channel":"C123","ts":"171.2"}}}`)}}
	client := Client{Lambda: lambdaClient, FunctionName: "runner"}
	result, err := client.RunAction(context.Background(), piece.ActionInvocation{
		InvocationID: "act-1", OrganizationID: "org-1", PieceName: "piece", PieceVersion: "1",
		ActionName: "slack_post_message", Auth: json.RawMessage(`{"access_token":"one-token"}`),
		Props: json.RawMessage(`{"channel":"C123","text":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lambdaClient.requests) != 1 {
		t.Fatalf("calls = %d", len(lambdaClient.requests))
	}
	request := lambdaClient.requests[0]
	if request.Operation != "action.run" || request.ActionName != "slack_post_message" ||
		string(request.Auth) != `{"access_token":"one-token"}` || string(request.Props) != `{"channel":"C123","text":"hello"}` {
		t.Fatalf("request = %+v", request)
	}
	if string(result.Output) != `{"ok":true,"channel":"C123","ts":"171.2"}` {
		t.Fatalf("result = %s", result.Output)
	}
}

func TestRunActionForwardsFilesBroker(t *testing.T) {
	t.Parallel()
	lambdaClient := &fakeLambda{outputs: [][]byte{[]byte(`{"ok":true,"result":{"output":{"ok":true}}}`)}}
	client := Client{Lambda: lambdaClient, FunctionName: "runner"}
	_, err := client.RunAction(context.Background(), piece.ActionInvocation{
		InvocationID: "act-1", OrganizationID: "org-1", PieceName: "piece", PieceVersion: "1",
		ActionName: "uploadFile", Auth: json.RawMessage(`{"access_token":"one-token"}`),
		Props:       json.RawMessage(`{"file":{"file_id":"file_abc"}}`),
		FilesBroker: &piece.FilesBroker{URL: "https://connect.example/v1/internal/piece-files", Token: "fbk_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lambdaClient.requests) != 1 || lambdaClient.requests[0].FilesBroker == nil ||
		lambdaClient.requests[0].FilesBroker.URL != "https://connect.example/v1/internal/piece-files" ||
		lambdaClient.requests[0].FilesBroker.Token != "fbk_1" {
		t.Fatalf("request = %+v", lambdaClient.requests)
	}
}

func (f *fakeLambda) Invoke(_ context.Context, input *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	var request runnerRequest
	if err := json.Unmarshal(input.Payload, &request); err != nil {
		return nil, err
	}
	f.requests = append(f.requests, request)
	output := f.outputs[0]
	f.outputs = f.outputs[1:]
	return &lambda.InvokeOutput{StatusCode: 200, Payload: output}, nil
}

func TestVerifyAndParseUsesClosedOperationsAndPreservesRawBody(t *testing.T) {
	t.Parallel()
	lambdaClient := &fakeLambda{outputs: [][]byte{
		[]byte(`{"ok":true,"result":{"invocationId":"i-verify","output":true}}`),
		[]byte(`{"ok":true,"result":{"invocationId":"i-parse","output":{"event":"message","identifierValue":"T123"}}}`),
	}}
	client := Client{Lambda: lambdaClient, FunctionName: "oryxa-piece-runner"}
	raw := []byte(`{"event_id":"Ev123","team_id":"T123"}`)
	result, err := client.VerifyAndParse(context.Background(), webhooks.RunnerRequest{
		InvocationID: "invocation-1",
		Route: domain.WebhookRoute{
			OrganizationID: "org-1", PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
		},
		Method: "POST", Headers: map[string]string{"x-slack-signature": "v0=test"},
		Body: map[string]any{"event_id": "Ev123", "team_id": "T123"}, RawBody: raw,
		SigningSecret: []byte("signing-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lambdaClient.requests) != 2 || lambdaClient.requests[0].Operation != "events.verify" || lambdaClient.requests[1].Operation != "events.parse-and-reply" {
		t.Fatalf("unexpected operations: %+v", lambdaClient.requests)
	}
	if lambdaClient.requests[0].WebhookSecret != "signing-secret" || lambdaClient.requests[1].WebhookSecret != "" {
		t.Fatal("signing secret was not scoped only to verification")
	}
	if lambdaClient.requests[0].ProviderPayload.RawBody != string(raw) || lambdaClient.requests[1].ProviderPayload.RawBody != string(raw) {
		t.Fatal("raw body changed between verification and parsing")
	}
	if !result.Verified || result.IdentifierValue != "T123" || result.ProviderEventID != "Ev123" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyFalseSkipsParserInvocation(t *testing.T) {
	t.Parallel()
	lambdaClient := &fakeLambda{outputs: [][]byte{
		[]byte(`{"ok":true,"result":{"output":false}}`),
	}}
	client := Client{Lambda: lambdaClient, FunctionName: "runner"}
	result, err := client.VerifyAndParse(context.Background(), webhooks.RunnerRequest{
		InvocationID: "i", Route: domain.WebhookRoute{OrganizationID: "o", PieceName: "p", PieceVersion: "1"},
	})
	if err != nil || result.Verified || len(lambdaClient.requests) != 1 {
		t.Fatalf("result/error/calls = %+v/%v/%d", result, err, len(lambdaClient.requests))
	}
}

func TestRunnerErrorExposesStableCodeOnly(t *testing.T) {
	t.Parallel()
	lambdaClient := &fakeLambda{outputs: [][]byte{
		[]byte(`{"ok":false,"error":{"code":"piece_not_allowed","message":"sensitive provider text"}}`),
	}}
	client := Client{Lambda: lambdaClient, FunctionName: "runner"}
	_, err := client.VerifyAndParse(context.Background(), webhooks.RunnerRequest{
		InvocationID: "i", Route: domain.WebhookRoute{OrganizationID: "o", PieceName: "p", PieceVersion: "1"},
	})
	var runnerError RunnerError
	if !errors.As(err, &runnerError) || runnerError.Code != "piece_not_allowed" ||
		runnerError.Message != "piece and exact version are not allowed" {
		t.Fatalf("error = %+v / %v", runnerError, err)
	}
	if err.Error() != "piece runner failed: piece_not_allowed" || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("error leaked upstream message: %q", err.Error())
	}
}
