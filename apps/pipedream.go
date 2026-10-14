package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
)

// pipedreamPort is application.Pipedream on internal/pipedream's client: it
// turns the client's types into apps' own, walks its pages, and says what its
// errors mean in apps' terms while keeping them, so a caller can still read
// Pipedream's own *pipedream.Error.
type pipedreamPort struct {
	client *pipedream.Client
}

var _ application.Pipedream = pipedreamPort{}

// maxPipedreamPages bounds walking one of Pipedream's paged lists, so a
// cursor that never ends cannot hold a caller for ever.
const maxPipedreamPages = 200

// pipedreamError says what a failed Pipedream call means to apps. An action
// that ran and failed is the app's failure, not Pipedream's, and is answered
// as a *application.PipedreamActionError carrying the app's own words.
func pipedreamError(err error) error {
	var failure *pipedream.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pipedream.ErrApp) && errors.As(err, &failure):
		return &application.PipedreamActionError{Message: failure.Message, Origin: failure.Origin, UpstreamStatus: failure.UpstreamStatus}
	case errors.Is(err, pipedream.ErrNotFound):
		return fmt.Errorf("%w: %w", domain.ErrNotFound, err)
	case errors.Is(err, pipedream.ErrRateLimited):
		return fmt.Errorf("%w: %w", domain.ErrRateLimited, err)
	case errors.Is(err, pipedream.ErrUnavailable), errors.Is(err, pipedream.ErrUnauthorized):
		// A refused project credential is nothing the caller can fix either.
		return fmt.Errorf("%w: %w", domain.ErrUnavailable, err)
	case errors.Is(err, pipedream.ErrInvalid):
		return fmt.Errorf("%w: %w", domain.ErrInvalid, err)
	}
	return err
}

// CreateConnectToken makes the token; request.App and request.WebhookURI are
// not Pipedream's to take here: the app goes on the link, and apps reads the
// account back rather than being told.
func (port pipedreamPort) CreateConnectToken(ctx context.Context, request application.PipedreamTokenRequest) (application.PipedreamConnectToken, error) {
	token, err := port.client.CreateConnectToken(ctx, request.ExternalUserID, request.SuccessRedirectURI, request.ErrorRedirectURI, request.ExpiresIn)
	if err != nil {
		return application.PipedreamConnectToken{}, pipedreamError(err)
	}
	return application.PipedreamConnectToken{Token: token.Token, ConnectLinkURL: token.ConnectLinkURL, ExpiresAt: token.ExpiresAt}, nil
}

func (port pipedreamPort) ListAccounts(ctx context.Context, externalUserID, app string) ([]application.PipedreamAccount, error) {
	accounts, err := port.client.ListAccounts(ctx, externalUserID, app)
	if err != nil {
		return nil, pipedreamError(err)
	}
	answered := make([]application.PipedreamAccount, 0, len(accounts))
	for _, account := range accounts {
		answered = append(answered, viewAccount(account))
	}
	return answered, nil
}

func (port pipedreamPort) GetAccount(ctx context.Context, id string) (application.PipedreamAccount, error) {
	account, err := port.client.GetAccount(ctx, id)
	if err != nil {
		return application.PipedreamAccount{}, pipedreamError(err)
	}
	return viewAccount(account), nil
}

func viewAccount(account pipedream.Account) application.PipedreamAccount {
	return application.PipedreamAccount{
		ID: account.ID, ExternalUserID: account.ExternalID, App: account.App.NameSlug, Name: account.Name,
		Healthy: account.Healthy, Dead: account.Dead, CreatedAt: account.CreatedAt, UpdatedAt: account.UpdatedAt,
	}
}

func (port pipedreamPort) DeleteAccount(ctx context.Context, id string) error {
	return pipedreamError(port.client.DeleteAccount(ctx, id))
}

func (port pipedreamPort) DeleteUser(ctx context.Context, externalUserID string) error {
	return pipedreamError(port.client.DeleteUser(ctx, externalUserID))
}

func (port pipedreamPort) ListApps(ctx context.Context, query string) ([]application.PipedreamApp, error) {
	var apps []application.PipedreamApp
	after := ""
	for range maxPipedreamPages {
		page, err := port.client.ListApps(ctx, pipedream.AppQuery{Search: query, After: after, Limit: 100})
		if err != nil {
			return nil, pipedreamError(err)
		}
		for _, app := range page.Items {
			apps = append(apps, application.PipedreamApp{
				Slug: app.NameSlug, Name: app.Name, Description: app.Description, Logo: app.ImgSrc,
				AuthType: app.AuthType, Categories: app.Categories,
			})
		}
		if after = page.NextCursor; after == "" {
			return apps, nil
		}
	}
	return apps, nil
}

func (port pipedreamPort) ListActions(ctx context.Context, app string) ([]application.PipedreamComponent, error) {
	return port.components(ctx, app, port.client.ListActions)
}

func (port pipedreamPort) ListTriggers(ctx context.Context, app string) ([]application.PipedreamComponent, error) {
	return port.components(ctx, app, port.client.ListTriggers)
}

func (port pipedreamPort) components(ctx context.Context, app string, list func(context.Context, pipedream.ComponentQuery) (pipedream.Page[pipedream.Component], error)) ([]application.PipedreamComponent, error) {
	var components []application.PipedreamComponent
	after := ""
	for range maxPipedreamPages {
		page, err := list(ctx, pipedream.ComponentQuery{App: app, After: after, Limit: 100})
		if err != nil {
			return nil, pipedreamError(err)
		}
		for _, component := range page.Items {
			view := application.PipedreamComponent{
				Key: component.Key, Name: component.Name, Description: component.Description,
				Version: component.Version, Props: component.ConfigurableProps,
			}
			if component.Annotations != nil {
				view.ReadOnlyHint, view.DestructiveHint = component.Annotations.ReadOnlyHint, component.Annotations.DestructiveHint
			}
			components = append(components, view)
		}
		if after = page.NextCursor; after == "" {
			return components, nil
		}
	}
	return components, nil
}

func (port pipedreamPort) RunAction(ctx context.Context, request application.PipedreamRunRequest) (application.PipedreamRunResult, error) {
	result, err := port.client.RunAction(ctx, request.ComponentKey, request.ExternalUserID, request.ConfiguredProps)
	if err != nil {
		return application.PipedreamRunResult{}, pipedreamError(err)
	}
	var exports json.RawMessage
	if result.Exports != nil {
		if exports, err = json.Marshal(result.Exports); err != nil {
			return application.PipedreamRunResult{}, fmt.Errorf("read the action's exports: %w", err)
		}
	}
	return application.PipedreamRunResult{Exports: exports, Return: result.Ret}, nil
}

func (port pipedreamPort) DeployTrigger(ctx context.Context, request application.PipedreamDeployRequest) (application.PipedreamDeployedTrigger, error) {
	deployed, err := port.client.DeployTrigger(ctx, request.ComponentKey, request.ExternalUserID, request.ConfiguredProps, request.WebhookURL, request.Timer)
	if err != nil {
		return application.PipedreamDeployedTrigger{}, pipedreamError(err)
	}
	return application.PipedreamDeployedTrigger{ID: deployed.ID, SigningKey: []byte(deployed.SigningKey)}, nil
}

func (port pipedreamPort) DeleteTrigger(ctx context.Context, externalUserID, id string) error {
	return pipedreamError(port.client.DeleteDeployedTrigger(ctx, id, externalUserID))
}

func (port pipedreamPort) Proxy(ctx context.Context, request application.PipedreamProxyRequest) (application.PipedreamProxyResponse, error) {
	outbound, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
	if err != nil {
		return application.PipedreamProxyResponse{}, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	if request.Header != nil {
		outbound.Header = request.Header.Clone()
	}
	response, err := port.client.Proxy(ctx, request.ExternalUserID, request.AccountID, outbound)
	if err != nil {
		return application.PipedreamProxyResponse{}, pipedreamError(err)
	}
	defer response.Body.Close()
	// The client has already read the body into memory, within its cap.
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return application.PipedreamProxyResponse{}, pipedreamError(err)
	}
	return application.PipedreamProxyResponse{Status: response.StatusCode, Header: response.Header, Body: body}, nil
}
