package webhookroutes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
)

var ErrInvalidInput = errors.New("invalid webhook route input")

type Authorizer interface {
	CanManageOAuthApps(context.Context, string, string) error
}

type Profiles interface {
	OAuthApp(context.Context, string, string) (domain.OAuthAppProfile, error)
}

type Catalog interface {
	TriggerDefinition(string, string) (triggerflow.Definition, bool)
}

type Routes interface {
	CreateWebhookRoute(context.Context, domain.WebhookRoute) error
}

type SecretBox interface {
	Seal(context.Context, secrets.Binding, []byte) (domain.SealedData, error)
}

type RegisterRequest struct {
	OrganizationID string
	ActorID        string
	ProfileID      string
	TriggerName    string
	SigningSecret  string
}

type Registration struct {
	OpaqueRoute string `json:"opaque_route"`
	WebhookURL  string `json:"webhook_url"`
	Provider    string `json:"provider"`
	TriggerName string `json:"trigger_name"`
}

type Service struct {
	Authorizer Authorizer
	Profiles   Profiles
	Catalog    Catalog
	Routes     Routes
	Secrets    SecretBox
	PublicURL  string
	Random     io.Reader
}

func (s Service) Register(ctx context.Context, request RegisterRequest) (Registration, error) {
	if request.OrganizationID == "" || request.ActorID == "" || request.ProfileID == "" || request.TriggerName == "" || request.SigningSecret == "" || s.PublicURL == "" {
		return Registration{}, ErrInvalidInput
	}
	if len(request.SigningSecret) > 4096 {
		return Registration{}, ErrInvalidInput
	}
	if err := s.Authorizer.CanManageOAuthApps(ctx, request.OrganizationID, request.ActorID); err != nil {
		return Registration{}, fmt.Errorf("%w: %v", oauthflow.ErrForbidden, err)
	}
	profile, err := s.Profiles.OAuthApp(ctx, request.OrganizationID, request.ProfileID)
	if err != nil {
		return Registration{}, fmt.Errorf("load OAuth application: %w", err)
	}
	if profile.Status != domain.OAuthAppStatusActive {
		return Registration{}, oauthflow.ErrProfileInactive
	}
	definition, ok := s.Catalog.TriggerDefinition(profile.Provider, request.TriggerName)
	if !ok || definition.Provider != profile.Provider {
		return Registration{}, triggerflow.ErrOperationDisabled
	}
	opaque, err := randomToken(s.Random, 32)
	if err != nil {
		return Registration{}, fmt.Errorf("generate opaque webhook route: %w", err)
	}
	binding := secrets.Binding{OrganizationID: request.OrganizationID, ResourceType: "webhook-route", ResourceID: opaque}
	sealed, err := s.Secrets.Seal(ctx, binding, []byte(request.SigningSecret))
	if err != nil {
		return Registration{}, fmt.Errorf("encrypt webhook signing secret: %w", err)
	}
	route := domain.WebhookRoute{
		OpaqueRoute: opaque, OrganizationID: request.OrganizationID, ProfileID: profile.ID,
		Provider: profile.Provider, PieceName: definition.PieceName, PieceVersion: definition.PieceVersion,
		TriggerName: request.TriggerName, Mode: definition.RouteMode, SigningSecret: sealed,
		TimestampHeader: definition.TimestampHeader, TimestampWindow: definition.TimestampWindow,
		MaxBodyBytes: definition.MaxBodyBytes, Active: true, CredentialRevision: 1,
	}
	if err := s.Routes.CreateWebhookRoute(ctx, route); err != nil {
		return Registration{}, fmt.Errorf("persist webhook route: %w", err)
	}
	return Registration{OpaqueRoute: opaque, WebhookURL: s.PublicURL + "/v1/webhooks/" + opaque,
		Provider: route.Provider, TriggerName: route.TriggerName}, nil
}

func randomToken(source io.Reader, size int) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
