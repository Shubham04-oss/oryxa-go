package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func workspacePath(organizationID string) string {
	return "/v1/organizations/" + url.PathEscape(organizationID) + "/workspace"
}

func (client *Client) WorkspacePlan(ctx context.Context, organizationID string, document spec.Document) (Plan, error) {
	encoded, err := document.YAML()
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	err = client.request(ctx, http.MethodPost, workspacePath(organizationID)+":plan", map[string]string{"document": string(encoded)}, &plan)
	return plan, err
}

type WorkspaceApplyResult struct {
	ApplyID   string               `json:"apply_id,omitempty"`
	Completed []CompletedOperation `json:"completed,omitempty"`
	Step      string               `json:"step"`
	Status    string               `json:"status"`
	Remaining Plan                 `json:"remaining"`
	Version   int64                `json:"version"`
}

func (client *Client) WorkspaceApply(ctx context.Context, organizationID string, document spec.Document, approved Plan) (WorkspaceApplyResult, error) {
	encoded, err := document.YAML()
	if err != nil {
		return WorkspaceApplyResult{}, err
	}
	var result WorkspaceApplyResult
	err = client.request(ctx, http.MethodPut, workspacePath(organizationID), map[string]any{
		"document": string(encoded), "approved_plan": approved,
	}, &result)
	return result, err
}

func (client *Client) WorkspacePull(ctx context.Context, organizationID string) (spec.Document, error) {
	return client.workspaceYAML(ctx, workspacePath(organizationID))
}

func (client *Client) WorkspaceVersion(ctx context.Context, organizationID string, number int64) (spec.Document, error) {
	if number <= 0 {
		return spec.Document{}, fmt.Errorf("workspace version must be positive")
	}
	return client.workspaceYAML(ctx, workspacePath(organizationID)+"/versions/"+strconv.FormatInt(number, 10))
}

func (client *Client) workspaceYAML(ctx context.Context, path string) (spec.Document, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+path, nil)
	if err != nil {
		return spec.Document{}, err
	}
	request.Header.Set("Authorization", "Bearer "+client.Token)
	response, err := client.HTTP.Do(request)
	if err != nil {
		return spec.Document{}, fmt.Errorf("GET %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return spec.Document{}, statusError(response, http.MethodGet, path)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return spec.Document{}, err
	}
	// Both endpoints return persisted server documents, which may contain
	// additive fields written by a newer server. Submission remains strict.
	return spec.ParseStored(data)
}

func (client *Client) WorkspaceRollback(ctx context.Context, organizationID string, number int64, approved Plan) (WorkspaceApplyResult, error) {
	if number <= 0 {
		return WorkspaceApplyResult{}, fmt.Errorf("workspace version must be positive")
	}
	var result WorkspaceApplyResult
	err := client.request(ctx, http.MethodPost, workspacePath(organizationID)+":rollback", map[string]any{
		"to": number, "approved_plan": approved,
	}, &result)
	return result, err
}

func (client *Client) WorkspaceRollbackPlan(ctx context.Context, organizationID string, number int64) (Plan, error) {
	if number <= 0 {
		return Plan{}, fmt.Errorf("workspace version must be positive")
	}
	var plan Plan
	err := client.request(ctx, http.MethodPost, workspacePath(organizationID)+":rollback-plan", map[string]int64{"to": number}, &plan)
	return plan, err
}

func (client *Client) WorkspaceStatus(ctx context.Context, organizationID string) (json.RawMessage, error) {
	var status json.RawMessage
	err := client.request(ctx, http.MethodGet, workspacePath(organizationID)+"/status", nil, &status)
	return status, err
}
