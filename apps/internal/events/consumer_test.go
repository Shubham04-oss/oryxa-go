package events

import (
	"context"
	"testing"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

type stubApp struct{ application }

func (stubApp) OrganizationDeleted(context.Context, authv1.OrganizationDeleted) error { return nil }
func (stubApp) MemberRemoved(context.Context, authv1.MemberRemoved) error             { return nil }
func (stubApp) NativeConnectionChanged(context.Context, integrationsv1.Connection) error {
	return nil
}
func (stubApp) NativeConnectionRemoved(context.Context, integrationsv1.Connection) error {
	return nil
}

// Every detail type apps subscribes to must have a handler, or provisioning
// would route events the consumer then refuses.
func TestEverySubscriptionHasAHandler(t *testing.T) {
	t.Parallel()
	consumer, err := NewConsumer(stubApp{}, host.TestRegistry().Verifier(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for source, detailTypes := range Subscriptions() {
		for _, detailType := range detailTypes {
			if subscribed, ok := consumer.handlers[detailType]; !ok || subscribed.source != source {
				t.Errorf("%s from %s has no handler", detailType, source)
			}
		}
	}
}
