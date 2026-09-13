package piecerunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

type LambdaAPI interface {
	Invoke(context.Context, *lambda.InvokeInput, ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

type Client struct {
	Lambda       LambdaAPI
	FunctionName string
}

type RunnerError struct {
	Code string
}

func (e RunnerError) Error() string { return "piece runner failed: " + e.Code }

func (c Client) RunTrigger(ctx context.Context, invocation piece.TriggerInvocation) (piece.TriggerResult, error) {
	if invocation.Operation != "trigger.enable" && invocation.Operation != "trigger.disable" &&
		invocation.Operation != "trigger.renew" && invocation.Operation != "trigger.handshake" &&
		invocation.Operation != "trigger.run" {
		return piece.TriggerResult{}, errors.New("unsupported trigger operation")
	}
	request := runnerRequest{
		InvocationID: invocation.InvocationID, OrganizationID: invocation.OrganizationID,
		PieceName: invocation.PieceName, PieceVersion: invocation.PieceVersion,
		Operation: invocation.Operation, TriggerName: invocation.TriggerName,
		Auth: invocation.Auth, Props: invocation.Props, WebhookURL: invocation.WebhookURL,
		PublicURL: invocation.PublicURL, InitialStore: invocation.InitialStore,
	}
	if invocation.Operation == "trigger.run" {
		request.ProviderPayload = webhookPayload{
			Method: invocation.Payload.Method, Headers: invocation.Payload.Headers,
			QueryParams: invocation.Payload.Query, Body: invocation.Payload.Body,
			RawBody: string(invocation.Payload.RawBody),
		}
	}
	response, err := c.invoke(ctx, request)
	if err != nil {
		return piece.TriggerResult{}, err
	}
	return piece.TriggerResult{
		Output:         append(json.RawMessage(nil), response.Result.Output...),
		StoreMutations: append([]piece.StoreMutation(nil), response.Result.StoreMutations...),
		Listeners:      append([]piece.Listener(nil), response.Result.Listeners...),
	}, nil
}

func (c Client) VerifyAndParse(ctx context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	providerPayload := webhookPayload{
		Method:      request.Method,
		Headers:     request.Headers,
		QueryParams: request.Query,
		Body:        request.Body,
		RawBody:     string(request.RawBody),
	}
	base := runnerRequest{
		OrganizationID:  request.Route.OrganizationID,
		PieceName:       request.Route.PieceName,
		PieceVersion:    request.Route.PieceVersion,
		ProviderPayload: providerPayload,
	}
	verify := base
	verify.InvocationID = request.InvocationID + "-verify"
	verify.Operation = "events.verify"
	verify.WebhookSecret = string(request.SigningSecret)
	verifyResponse, err := c.invoke(ctx, verify)
	if err != nil {
		return webhooks.RunnerResult{}, err
	}
	var verified bool
	if err := json.Unmarshal(verifyResponse.Result.Output, &verified); err != nil {
		return webhooks.RunnerResult{}, errors.New("piece runner returned invalid verification output")
	}
	if !verified {
		return webhooks.RunnerResult{Verified: false}, nil
	}

	parse := base
	parse.InvocationID = request.InvocationID + "-parse"
	parse.Operation = "events.parse-and-reply"
	parseResponse, err := c.invoke(ctx, parse)
	if err != nil {
		return webhooks.RunnerResult{}, err
	}
	var output parseOutput
	if err := json.Unmarshal(parseResponse.Result.Output, &output); err != nil {
		return webhooks.RunnerResult{}, errors.New("piece runner returned invalid event output")
	}
	result := webhooks.RunnerResult{
		Verified:        true,
		Event:           output.Event,
		IdentifierValue: output.IdentifierValue,
		ProviderEventID: providerEventID(request.Body),
	}
	if output.Reply != nil {
		result.Reply = &webhooks.Reply{
			Status:  output.Reply.Status,
			Headers: output.Reply.Headers,
			Body:    output.Reply.Body,
		}
	}
	return result, nil
}

func (c Client) invoke(ctx context.Context, request runnerRequest) (runnerResponse, error) {
	if c.Lambda == nil || c.FunctionName == "" {
		return runnerResponse{}, errors.New("piece runner Lambda is not configured")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return runnerResponse{}, fmt.Errorf("encode piece runner request: %w", err)
	}
	defer wipe(payload)
	output, err := c.Lambda.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   &c.FunctionName,
		InvocationType: types.InvocationTypeRequestResponse,
		Payload:        payload,
	})
	if err != nil {
		return runnerResponse{}, fmt.Errorf("invoke piece runner: %w", err)
	}
	if output.FunctionError != nil {
		return runnerResponse{}, errors.New("piece runner Lambda returned a function error")
	}
	if output.StatusCode < 200 || output.StatusCode >= 300 {
		return runnerResponse{}, fmt.Errorf("piece runner Lambda returned status %d", output.StatusCode)
	}
	var response runnerResponse
	if err := json.Unmarshal(output.Payload, &response); err != nil {
		return runnerResponse{}, errors.New("piece runner Lambda returned invalid JSON")
	}
	if !response.OK {
		code := response.Error.Code
		if code == "" {
			code = "piece_failed"
		}
		return runnerResponse{}, RunnerError{Code: code}
	}
	return response, nil
}

type runnerRequest struct {
	InvocationID    string          `json:"invocationId"`
	OrganizationID  string          `json:"organizationId"`
	PieceName       string          `json:"pieceName"`
	PieceVersion    string          `json:"pieceVersion"`
	Operation       string          `json:"operation"`
	TriggerName     string          `json:"triggerName,omitempty"`
	Auth            json.RawMessage `json:"auth,omitempty"`
	Props           json.RawMessage `json:"props,omitempty"`
	WebhookURL      string          `json:"webhookUrl,omitempty"`
	PublicURL       string          `json:"publicUrl,omitempty"`
	InitialStore    map[string]any  `json:"initialStore,omitempty"`
	WebhookSecret   string          `json:"webhookSecret,omitempty"`
	ProviderPayload webhookPayload  `json:"providerPayload"`
}

type webhookPayload struct {
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers"`
	QueryParams map[string]string `json:"queryParams"`
	Body        any               `json:"body"`
	RawBody     string            `json:"rawBody"`
}

type runnerResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		Output         json.RawMessage       `json:"output"`
		StoreMutations []piece.StoreMutation `json:"storeMutations"`
		Listeners      []piece.Listener      `json:"listeners"`
	} `json:"result"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

type parseOutput struct {
	Event           string `json:"event"`
	IdentifierValue string `json:"identifierValue"`
	Reply           *struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    any               `json:"body"`
	} `json:"reply"`
}

func providerEventID(body any) string {
	values, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := values["event_id"].(string)
	return value
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
