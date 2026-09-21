package events

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

// signingHarness holds one signer per Oryxa source email listens to — auth,
// billing, teams — and a verifier that trusts them, so a domain event's
// envelope is signed the way the service it names would have signed it. SES's
// own feedback is exempt: AWS publishes it, and no Oryxa key could sign it.
var (
	harnessOnce   sync.Once
	harnessErr    error
	harnessSigns  = map[string]*eventauth.Signer{}
	harnessVerif  *eventauth.Verifier
	harnessSource = map[string]string{
		autheventsv1.Source:    "auth",
		billingeventsv1.Source: "billing",
		teamseventsv1.Source:   "teams",
	}
)

func signingHarness() (*eventauth.Verifier, error) {
	harnessOnce.Do(func() {
		keys := map[string]*ecdsa.PublicKey{}
		for source, service := range harnessSource {
			signer, key, err := eventauth.NewLocalSigner(service, time.Now)
			if err != nil {
				harnessErr = err
				return
			}
			harnessSigns[source] = signer
			keys[service] = key
		}
		harnessVerif = eventauth.NewVerifier(keys, nil, time.Now)
	})
	return harnessVerif, harnessErr
}

// consumerFor builds the consumer the way mount does, with a verifier that
// trusts the harness's signers.
func consumerFor(app application) (*Consumer, error) {
	verifier, err := signingHarness()
	if err != nil {
		return nil, err
	}
	return NewConsumer(app, verifier, slog.New(slog.DiscardHandler))
}

// signedEnvelope wraps an envelope's fields the way the named source's
// publisher would: with a signature over exactly what the envelope holds.
func signedEnvelope(source, detailType, eventID string, version int, occurredAt, data string) string {
	if _, err := signingHarness(); err != nil {
		panic(err)
	}
	signer, ok := harnessSigns[source]
	if !ok {
		return fmt.Sprintf(`{"event_id":%q,"version":%d,"occurred_at":%q,"data":%s}`, eventID, version, occurredAt, data)
	}
	signature, err := signer.Sign(source, detailType, eventID, version, occurredAt, []byte(data))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"event_id":%q,"version":%d,"occurred_at":%q,"data":%s,"sig":%q}`, eventID, version, occurredAt, data, signature)
}

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

// The data billing writes for each subscribed event.
const (
	paymentFailedData   = `{"organization_id":"org_1","organization_name":"Acme","subscription_id":"sub_1","billing_email":"billing@example.com","grace_ends_at":"2026-09-16T12:00:00Z","billing_url":"http://localhost:3000/settings/billing?organization_id=org_1","failed_at":"2026-09-13T12:00:00Z"}`
	endedData           = `{"organization_id":"org_1","organization_name":"Acme","subscription_id":"sub_1","billing_email":"billing@example.com","reason":"cancelled","billing_url":"http://localhost:3000/settings/billing?organization_id=org_1","ended_at":"2026-09-13T12:00:00Z"}`
	renewalUpcomingData = `{"organization_id":"org_1","organization_name":"Acme","subscription_id":"sub_1","billing_email":"billing@example.com","plan":"team","renews_at":"2026-09-20T12:00:00Z","billing_url":"http://localhost:3000/settings/billing?organization_id=org_1"}`
	memberPingedData    = `{"team_id":"team_1","team_name":"Engineering","organization_id":"org_1","organization_name":"Acme","user_id":"user-2","email":"ravi@example.com","role":"member","tool":"call_team","notify_email":true,"called_by":"user-1","called_at":"2026-09-13T12:00:00Z"}`
)

type fakeApplication struct {
	registrations []autheventsv1.UserRegistered
	verifications []autheventsv1.EmailVerificationDeliveryRequested
	resets        []autheventsv1.PasswordResetDeliveryRequested
	invitations   []autheventsv1.MemberInvited
	revocations   []autheventsv1.MemberInvitationRevoked
	roleChanges   []autheventsv1.MemberRoleChanged
	removals      []autheventsv1.MemberRemoved
	failures      []billingeventsv1.SubscriptionPaymentFailed
	endings       []billingeventsv1.SubscriptionEnded
	renewals      []billingeventsv1.SubscriptionRenewalUpcoming
	pings         []teamseventsv1.MemberPinged
	refs          []delivery.EventRef
	feedback      []delivery.Feedback
	err           error
}

func (f *fakeApplication) SubscriptionPaymentFailed(_ context.Context, ref delivery.EventRef, data billingeventsv1.SubscriptionPaymentFailed) error {
	f.refs = append(f.refs, ref)
	f.failures = append(f.failures, data)
	return f.err
}

func (f *fakeApplication) SubscriptionEnded(_ context.Context, ref delivery.EventRef, data billingeventsv1.SubscriptionEnded) error {
	f.refs = append(f.refs, ref)
	f.endings = append(f.endings, data)
	return f.err
}

func (f *fakeApplication) SubscriptionRenewalUpcoming(_ context.Context, ref delivery.EventRef, data billingeventsv1.SubscriptionRenewalUpcoming) error {
	f.refs = append(f.refs, ref)
	f.renewals = append(f.renewals, data)
	return f.err
}

func TestHandleSQSDispatchesBillingEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	billingBody := func(detailType, data string) string {
		return eventBody(billingeventsv1.Source, detailType, signedEnvelope(billingeventsv1.Source, detailType, "evt_billing_1", 1, "2026-09-13T12:00:00Z", data))
	}

	response, err := consumer.HandleSQS(context.Background(), batchOf(map[string]string{
		"1-failed":  billingBody(billingeventsv1.SubscriptionPaymentFailedDetailType, paymentFailedData),
		"2-ended":   billingBody(billingeventsv1.SubscriptionEndedDetailType, endedData),
		"3-renewal": billingBody(billingeventsv1.SubscriptionRenewalUpcomingDetailType, renewalUpcomingData),
		"4-auth":    eventBody(autheventsv1.Source, billingeventsv1.SubscriptionEndedDetailType, signedEnvelope(autheventsv1.Source, billingeventsv1.SubscriptionEndedDetailType, "evt_x", 1, "2026-09-13T12:00:00Z", endedData)),
	}))
	if err != nil {
		t.Fatalf("HandleSQS() error = %v", err)
	}
	if len(response.BatchItemFailures) != 1 || response.BatchItemFailures[0].ItemIdentifier != "4-auth" {
		t.Fatalf("failures = %+v, want only the billing event claimed by auth", response.BatchItemFailures)
	}
	if len(app.failures) != 1 || app.failures[0].GraceEndsAt == nil || app.failures[0].BillingEmail != "billing@example.com" ||
		len(app.endings) != 1 || app.endings[0].Reason != "cancelled" ||
		len(app.renewals) != 1 || app.renewals[0].RenewsAt.IsZero() || app.renewals[0].Plan != "team" {
		t.Fatalf("dispatched failures=%+v endings=%+v renewals=%+v", app.failures, app.endings, app.renewals)
	}
	if app.refs[0] != (delivery.EventRef{Source: billingeventsv1.Source, DetailType: billingeventsv1.SubscriptionPaymentFailedDetailType, ID: "evt_billing_1"}) {
		t.Fatalf("event ref = %+v", app.refs[0])
	}
}

func TestHandleSQSDispatchesTeamEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	response, err := consumer.HandleSQS(context.Background(), batchOf(map[string]string{
		"1-ping": eventBody(teamseventsv1.Source, teamseventsv1.MemberPingedDetailType, signedEnvelope(teamseventsv1.Source, teamseventsv1.MemberPingedDetailType, "evt_teams_1", 1, "2026-09-13T12:00:00Z", memberPingedData)),
	}))
	if err != nil {
		t.Fatalf("HandleSQS() error = %v", err)
	}
	if len(response.BatchItemFailures) != 0 || len(app.pings) != 1 {
		t.Fatalf("failures = %+v, pings = %d", response.BatchItemFailures, len(app.pings))
	}
	if got := app.pings[0]; got.Email != "ravi@example.com" || !got.NotifyEmail || got.Tool != teamseventsv1.ToolCallTeam || got.TeamName != "Engineering" {
		t.Fatalf("decoded ping = %+v", got)
	}
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

func (f *fakeApplication) MemberPinged(_ context.Context, ref delivery.EventRef, data teamseventsv1.MemberPinged) error {
	f.refs = append(f.refs, ref)
	f.pings = append(f.pings, data)
	return f.err
}

func (f *fakeApplication) SESFeedback(_ context.Context, feedback delivery.Feedback) error {
	f.feedback = append(f.feedback, feedback)
	return f.err
}

func TestHandleSQSDispatchesAuthEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}

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
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
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
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}

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
		consumer, err := consumerFor(&fakeApplication{err: appErr})
		if err != nil {
			t.Fatal(err)
		}

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
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}

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
	return eventBody(autheventsv1.Source, detailType, signedEnvelope(autheventsv1.Source, detailType, "evt_1", version, "2026-09-13T12:00:00Z", data))
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
