package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"testing"

	lambdaevents "github.com/aws/aws-lambda-go/events"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
)

// The data auth writes for each subscribed event, field for field.
const (
	registeredData   = `{"user_id":"user-1","email":"asha@example.com","display_name":"Asha","registered_at":"2026-09-13T12:00:00Z"}`
	verificationData = `{"request_id":"act_1","user_id":"user-1","email":"asha@example.com","verification_url":"http://localhost:3000/auth/verify-email?token=verify","expires_at":"2026-09-14T12:00:00Z"}`
	resetData        = `{"request_id":"act_2","user_id":"user-1","email":"asha@example.com","reset_url":"http://localhost:3000/auth/reset-password?token=reset","expires_at":"2026-09-13T12:30:00Z"}`
	invitedData      = `{"invitation_id":"inv_1","organization_id":"org_1","organization_name":"Acme","email":"ravi@example.com","role":"admin","invited_by":"user-1","accept_url":"http://localhost:3000/invitations/accept?token=invite","expires_at":"2026-09-20T12:00:00Z"}`
	revokedData      = `{"invitation_id":"inv_2","organization_id":"org_1","organization_name":"Acme","email":"sam@example.com","role":"member","revoked_by":"user-1","revoked_at":"2026-09-13T12:00:00Z"}`
	roleChangedData  = `{"organization_id":"org_1","organization_name":"Acme","user_id":"user-2","email":"ravi@example.com","previous_role":"member","role":"admin","changed_by":"user-1","changed_at":"2026-09-13T12:00:00Z"}`
	removedData      = `{"organization_id":"org_1","organization_name":"Acme","user_id":"user-2","email":"ravi@example.com","previous_role":"admin","reason":"organization_deleted","removed_by":"user-1","removed_at":"2026-09-13T12:00:00Z"}`
)

type fakeApplication struct {
	registrations []autheventsv1.UserRegistered
	verifications []autheventsv1.EmailVerificationDeliveryRequested
	resets        []autheventsv1.PasswordResetDeliveryRequested
	invitations   []autheventsv1.MemberInvited
	revocations   []autheventsv1.MemberInvitationRevoked
	roleChanges   []autheventsv1.MemberRoleChanged
	removals      []autheventsv1.MemberRemoved
	refs          []delivery.EventRef
	feedback      []delivery.Feedback
	err           error
}

func (f *fakeApplication) MemberInvitationRevoked(_ context.Context, ref delivery.EventRef, data autheventsv1.MemberInvitationRevoked) error {
	f.refs = append(f.refs, ref)
	f.revocations = append(f.revocations, data)
	return f.err
}

func (f *fakeApplication) MemberRoleChanged(_ context.Context, ref delivery.EventRef, data autheventsv1.MemberRoleChanged) error {
	f.refs = append(f.refs, ref)
	f.roleChanges = append(f.roleChanges, data)
	return f.err
}

func (f *fakeApplication) MemberRemoved(_ context.Context, ref delivery.EventRef, data autheventsv1.MemberRemoved) error {
	f.refs = append(f.refs, ref)
	f.removals = append(f.removals, data)
	return f.err
}

func (f *fakeApplication) UserRegistered(_ context.Context, ref delivery.EventRef, data autheventsv1.UserRegistered) error {
	f.refs = append(f.refs, ref)
	f.registrations = append(f.registrations, data)
	return f.err
}

func (f *fakeApplication) EmailVerificationRequested(_ context.Context, ref delivery.EventRef, data autheventsv1.EmailVerificationDeliveryRequested) error {
	f.refs = append(f.refs, ref)
	f.verifications = append(f.verifications, data)
	return f.err
}

func (f *fakeApplication) PasswordResetRequested(_ context.Context, ref delivery.EventRef, data autheventsv1.PasswordResetDeliveryRequested) error {
	f.refs = append(f.refs, ref)
	f.resets = append(f.resets, data)
	return f.err
}

func (f *fakeApplication) MemberInvited(_ context.Context, ref delivery.EventRef, data autheventsv1.MemberInvited) error {
	f.refs = append(f.refs, ref)
	f.invitations = append(f.invitations, data)
	return f.err
}

func (f *fakeApplication) SESFeedback(_ context.Context, feedback delivery.Feedback) error {
	f.feedback = append(f.feedback, feedback)
	return f.err
}

func TestHandleSQSDispatchesAuthEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))

	response, err := consumer.HandleSQS(context.Background(), batchOf(map[string]string{
		"1-verification": domainBody(autheventsv1.EmailVerificationDeliveryRequestedDetailType, 1, verificationData),
		"2-reset":        domainBody(autheventsv1.PasswordResetDeliveryRequestedDetailType, 1, resetData),
		"3-invitation":   domainBody(autheventsv1.MemberInvitedDetailType, 1, invitedData),
		"4-registered":   domainBody(autheventsv1.UserRegisteredDetailType, 1, registeredData),
		"5-revoked":      domainBody(autheventsv1.MemberInvitationRevokedDetailType, 1, revokedData),
		"6-role-changed": domainBody(autheventsv1.MemberRoleChangedDetailType, 1, roleChangedData),
		"7-removed":      domainBody(autheventsv1.MemberRemovedDetailType, 1, removedData),
	}))
	if err != nil {
		t.Fatalf("HandleSQS() error = %v", err)
	}
	if len(response.BatchItemFailures) != 0 {
		t.Fatalf("failures = %+v, want none", response.BatchItemFailures)
	}

	if len(app.revocations) != 1 || len(app.roleChanges) != 1 || len(app.removals) != 1 {
		t.Fatalf("dispatched %d revocations, %d role changes, %d removals; want one each", len(app.revocations), len(app.roleChanges), len(app.removals))
	}
	if got := app.revocations[0]; got.OrganizationName != "Acme" || got.Email != "sam@example.com" || got.RevokedAt.IsZero() {
		t.Fatalf("decoded revocation = %+v", got)
	}
	if got := app.roleChanges[0]; got.Email != "ravi@example.com" || got.PreviousRole != "member" || got.Role != "admin" || got.ChangedBy != "user-1" {
		t.Fatalf("decoded role change = %+v", got)
	}
	if got := app.removals[0]; got.Reason != autheventsv1.MemberRemovedReasonOrganizationDeleted || got.Email != "ravi@example.com" || got.RemovedBy != "user-1" {
		t.Fatalf("decoded removal = %+v", got)
	}

	if len(app.verifications) != 1 || len(app.resets) != 1 || len(app.invitations) != 1 || len(app.registrations) != 1 {
		t.Fatalf("dispatched %d verifications, %d resets, %d invitations, %d registrations; want one each",
			len(app.verifications), len(app.resets), len(app.invitations), len(app.registrations))
	}
	if got := app.registrations[0]; got.Email != "asha@example.com" || got.DisplayName != "Asha" || got.RegisteredAt.IsZero() {
		t.Fatalf("decoded registration = %+v", got)
	}
	if got := app.verifications[0]; got.Email != "asha@example.com" || got.VerificationURL == "" || got.ExpiresAt.IsZero() {
		t.Fatalf("decoded verification = %+v", got)
	}
	if got := app.resets[0]; got.ResetURL == "" || got.ExpiresAt.IsZero() {
		t.Fatalf("decoded reset = %+v", got)
	}
	if got := app.invitations[0]; got.OrganizationName != "Acme" || got.Role != "admin" || got.AcceptURL == "" {
		t.Fatalf("decoded invitation = %+v", got)
	}
	want := delivery.EventRef{Source: autheventsv1.Source, DetailType: autheventsv1.EmailVerificationDeliveryRequestedDetailType, ID: "evt_1"}
	if app.refs[0] != want {
		t.Fatalf("event ref = %+v, want the envelope's event_id %+v", app.refs[0], want)
	}
}

func TestHandleSQSDispatchesSESFeedback(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))
	record := `{"eventType":"Bounce","bounce":{"bounceType":"Permanent","bounceSubType":"General","bouncedRecipients":[{"emailAddress":"asha@example.com"}],"timestamp":"2026-09-13T12:00:05.000Z"},` + sesMail + `}`

	response, err := consumer.HandleSQS(context.Background(), batchOf(map[string]string{
		"message-1": eventBody(SESSource, "Email Bounced", record),
	}))
	if err != nil {
		t.Fatalf("HandleSQS() error = %v", err)
	}
	if len(response.BatchItemFailures) != 0 || len(app.feedback) != 1 {
		t.Fatalf("failures = %+v, feedback calls = %d; want one clean feedback call", response.BatchItemFailures, len(app.feedback))
	}
	if got := app.feedback[0]; got.Status != delivery.StatusBounced || !got.Suppress || got.DeliveryID != "delivery-1" {
		t.Fatalf("feedback = %+v", got)
	}
}

func TestHandleSQSReportsOnlyFailedRecords(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))

	response, err := consumer.HandleSQS(context.Background(), batchOf(map[string]string{
		"handled":          domainBody(autheventsv1.PasswordResetDeliveryRequestedDetailType, 1, resetData),
		"not-json":         "{",
		"unconsumed-auth":  domainBody(autheventsv1.UserSignedInDetailType, 1, `{"user_id":"user-1","provider":"Google","signed_in_at":"2026-09-13T12:00:00Z"}`),
		"unknown-source":   eventBody("oryxa.billing", autheventsv1.PasswordResetDeliveryRequestedDetailType, `{"event_id":"evt_1","version":1,"data":`+resetData+`}`),
		"future-version":   domainBody(autheventsv1.PasswordResetDeliveryRequestedDetailType, 2, resetData),
		"bad-data":         domainBody(autheventsv1.PasswordResetDeliveryRequestedDetailType, 1, `{"email":42}`),
		"no-event-id":      eventBody(autheventsv1.Source, autheventsv1.PasswordResetDeliveryRequestedDetailType, `{"version":1,"data":`+resetData+`}`),
		"unconsumed-ses":   eventBody(SESSource, "Email Opened", `{"eventType":"Open",`+sesMail+`}`),
		"incomplete-ses":   eventBody(SESSource, "Email Bounced", `{"eventType":"Bounce",`+sesMail+`}`),
		"handled-feedback": eventBody(SESSource, "Email Rejected", `{"eventType":"Reject",`+sesMail+`,"reject":{"reason":"Bad content"}}`),
	}))
	if err != nil {
		t.Fatalf("HandleSQS() error = %v", err)
	}

	var failed []string
	for _, failure := range response.BatchItemFailures {
		failed = append(failed, failure.ItemIdentifier)
	}
	slices.Sort(failed)
	want := []string{"bad-data", "future-version", "incomplete-ses", "no-event-id", "not-json", "unconsumed-auth", "unconsumed-ses", "unknown-source"}
	if !slices.Equal(failed, want) {
		t.Fatalf("failed records = %v, want %v", failed, want)
	}
	if len(app.resets) != 1 || len(app.feedback) != 1 {
		t.Fatalf("PasswordResetRequested calls = %d, SESFeedback calls = %d; want 1 each", len(app.resets), len(app.feedback))
	}
}

func TestHandleSQSReportsApplicationError(t *testing.T) {
	for _, appErr := range []error{errors.New("ses throttled"), fmt.Errorf("delivery d-1: %w", delivery.ErrInProgress)} {
		consumer := NewConsumer(&fakeApplication{err: appErr}, slog.New(slog.DiscardHandler))

		response, err := consumer.HandleSQS(context.Background(), batchOf(map[string]string{
			"message-1": domainBody(autheventsv1.MemberInvitedDetailType, 1, invitedData),
		}))
		if err != nil {
			t.Fatalf("HandleSQS() error = %v", err)
		}
		if len(response.BatchItemFailures) != 1 || response.BatchItemFailures[0].ItemIdentifier != "message-1" {
			t.Fatalf("with %v: failures = %+v, want message-1 retried", appErr, response.BatchItemFailures)
		}
	}
}

func TestConsumerHandlesEverySubscription(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))

	want := Subscriptions()
	for _, detailType := range SESFeedbackDetailTypes() {
		want = append(want, Subscription{Source: SESSource, DetailType: detailType})
	}
	if len(consumer.handlers) != len(want) {
		t.Fatalf("handlers = %d, subscriptions = %d", len(consumer.handlers), len(want))
	}
	for _, subscription := range want {
		if _, ok := consumer.handlers[subscription]; !ok {
			t.Fatalf("no handler for %+v", subscription)
		}
	}
}

// domainBody is an SQS message body as an EventBridge rule delivers an auth
// event, around the envelope auth's publisher puts in detail.
func domainBody(detailType string, version int, data string) string {
	return eventBody(autheventsv1.Source, detailType, fmt.Sprintf(`{"event_id":"evt_1","version":%d,"occurred_at":"2026-09-13T12:00:00Z","data":%s}`, version, data))
}

func eventBody(source, detailType, detail string) string {
	return fmt.Sprintf(`{"version":"0","id":"eventbridge-1","detail-type":%q,"source":%q,"account":"000000000000","time":"2026-09-13T12:00:00Z","region":"ap-south-1","resources":[],"detail":%s}`,
		detailType, source, detail)
}

func batchOf(bodies map[string]string) lambdaevents.SQSEvent {
	var batch lambdaevents.SQSEvent
	for _, messageID := range slices.Sorted(maps.Keys(bodies)) {
		batch.Records = append(batch.Records, lambdaevents.SQSMessage{MessageId: messageID, ReceiptHandle: "receipt-" + messageID, Body: bodies[messageID]})
	}
	return batch
}
