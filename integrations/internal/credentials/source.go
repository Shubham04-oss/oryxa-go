package credentials

import (
	"context"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type SecretBox interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

// EnvelopeSource is the current non-refreshing credential source. It is kept
// behind a narrow interface so providers with expiring access tokens can add a
// leased refresh manager without changing action or trigger execution.
type EnvelopeSource struct {
	Secrets SecretBox
}

func (s EnvelopeSource) ForExecution(ctx context.Context, connection domain.Connection) ([]byte, error) {
	if s.Secrets == nil || connection.OrganizationID == "" || connection.ID == "" {
		return nil, fmt.Errorf("connection credential source is not configured")
	}
	return s.Secrets.Open(ctx, secrets.Binding{
		OrganizationID: connection.OrganizationID,
		ResourceType:   "connection",
		ResourceID:     connection.ID,
	}, connection.EncryptedCredentials)
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
