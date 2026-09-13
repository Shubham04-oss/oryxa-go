package application

import (
	"context"
	"errors"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

type eventPublisher interface {
	Publish(context.Context, string, any) error
}

type Service struct {
	events eventPublisher
}

func New(events eventPublisher) *Service {
	return &Service{events: events}
}

func (s *Service) UserRegistered(ctx context.Context, event autheventsv1.UserRegistered) error {
	if event.UserID == "" || event.Email == "" || event.RegisteredAt.IsZero() {
		return errors.New("user id, email, and registration time are required")
	}
	return s.events.Publish(ctx, autheventsv1.UserRegisteredDetailType, event)
}
