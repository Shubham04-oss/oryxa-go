package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
)

var testNow = time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)

// fakeStore keeps the same rules as the DynamoDB store's conditions.
type fakeStore struct {
	deliveries   map[string]Delivery
	suppressions map[string]Suppression
}

func newFakeStore() *fakeStore {
	return &fakeStore{deliveries: map[string]Delivery{}, suppressions: map[string]Suppression{}}
}

func (f *fakeStore) Suppression(_ context.Context, address string) (Suppression, bool, error) {
	suppression, ok := f.suppressions[address]
	return suppression, ok, nil
}

func (f *fakeStore) Suppress(_ context.Context, suppression Suppression) error {
	if _, ok := f.suppressions[suppression.Address]; !ok {
		f.suppressions[suppression.Address] = suppression
	}
	return nil
}

func (f *fakeStore) Claim(_ context.Context, delivery Delivery, now time.Time) (bool, error) {
	if existing, ok := f.deliveries[delivery.ID]; ok {
		expired := existing.Status == StatusSending && existing.LeaseExpiresAt.Before(now)
		if existing.Status != StatusFailed && !expired {
			return false, nil
		}
	}
	f.deliveries[delivery.ID] = delivery
	return true, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (Delivery, error) {
	delivery, ok := f.deliveries[id]
	if !ok {
		return Delivery{}, fmt.Errorf("delivery %s not found", id)
	}
	return delivery, nil
}

func (f *fakeStore) MarkSent(_ context.Context, id, sesMessageID string, _ time.Time) (bool, error) {
	delivery := f.deliveries[id]
	if delivery.Status != StatusSending {
		return false, nil
	}
	delivery.Status = StatusSent
	delivery.SESMessageID = sesMessageID
	delivery.LeaseExpiresAt = time.Time{}
	f.deliveries[id] = delivery
	return true, nil
}

func (f *fakeStore) MarkFailed(_ context.Context, id, _ string, _ time.Time) error {
	if delivery := f.deliveries[id]; delivery.Status == StatusSending {
		delivery.Status = StatusFailed
		delivery.LeaseExpiresAt = time.Time{}
		f.deliveries[id] = delivery
	}
	return nil
}

func (f *fakeStore) RecordFeedback(_ context.Context, feedback Feedback, _ time.Time) (bool, error) {
	delivery, ok := f.deliveries[feedback.DeliveryID]
	if !ok || delivery.Status.Rank() > feedback.Status.Rank() {
		return false, nil
	}
	delivery.Status = feedback.Status
	f.deliveries[feedback.DeliveryID] = delivery
	return true, nil
}

type fakeSender struct {
	sent []mail.Email
	err  error
}

func (f *fakeSender) Send(_ context.Context, email mail.Email) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, email)
	return fmt.Sprintf("ses-%d", len(f.sent)), nil
}

func newTestDeliveries(store Store, sender *fakeSender) *Deliveries {
	deliveries := New(store, sender, slog.New(slog.DiscardHandler))
	deliveries.now = func() time.Time { return testNow }
	return deliveries
}

var verificationRequest = Request{
	Event:     EventRef{Source: "oryxa.auth", DetailType: "auth.user.email-verification.delivery-requested.v1", ID: "event-1"},
	Recipient: "Asha@Example.com",
	Message:   mail.Message{Template: "verify-email", Subject: "Confirm your email address"},
	ExpiresAt: testNow.Add(24 * time.Hour),
}

func verificationID() string {
	return ID(verificationRequest.Event, "verify-email", verificationRequest.Recipient)
}

func TestDeliverSendsOnceAndRecordsIt(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	deliveries := newTestDeliveries(store, sender)

	outcome, err := deliveries.Deliver(context.Background(), verificationRequest)
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if outcome != OutcomeSent || len(sender.sent) != 1 {
		t.Fatalf("outcome = %q with %d sends, want sent once", outcome, len(sender.sent))
	}
	if got := sender.sent[0]; got.DeliveryID != verificationID() || got.To != "Asha@Example.com" {
		t.Fatalf("sent %+v, want delivery %s to the address as given", got, verificationID())
	}
	if got := store.deliveries[verificationID()]; got.Status != StatusSent || got.SESMessageID != "ses-1" {
		t.Fatalf("recorded delivery = %+v, want sent as ses-1", got)
	}

	outcome, err = deliveries.Deliver(context.Background(), verificationRequest)
	if err != nil {
		t.Fatalf("redelivered Deliver() error = %v", err)
	}
	if outcome != OutcomeDuplicate || len(sender.sent) != 1 {
		t.Fatalf("redelivery outcome = %q with %d sends, want duplicate and no second send", outcome, len(sender.sent))
	}
}

func TestDeliverRetriesAfterSendFailure(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{err: errors.New("throttled")}
	deliveries := newTestDeliveries(store, sender)

	if _, err := deliveries.Deliver(context.Background(), verificationRequest); err == nil {
		t.Fatal("Deliver() error = nil, want the send error")
	}
	if got := store.deliveries[verificationID()].Status; got != StatusFailed {
		t.Fatalf("status after failed send = %q, want failed", got)
	}

	sender.err = nil
	outcome, err := deliveries.Deliver(context.Background(), verificationRequest)
	if err != nil || outcome != OutcomeSent || len(sender.sent) != 1 {
		t.Fatalf("retry = %q, %v with %d sends; want sent once", outcome, err, len(sender.sent))
	}
}

func TestDeliverWaitsForAnotherAttempt(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	store.deliveries[verificationID()] = Delivery{ID: verificationID(), Status: StatusSending, LeaseExpiresAt: testNow.Add(10 * time.Second)}
	deliveries := newTestDeliveries(store, sender)

	_, err := deliveries.Deliver(context.Background(), verificationRequest)
	if !errors.Is(err, ErrInProgress) {
		t.Fatalf("Deliver() error = %v, want ErrInProgress", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sends = %d, want 0 while another attempt holds the claim", len(sender.sent))
	}
}

// raceStore refuses the claim, as if another attempt took it first, and shows
// that attempt's result when the delivery is read back.
type raceStore struct {
	*fakeStore
	winner Status
}

func (r raceStore) Claim(context.Context, Delivery, time.Time) (bool, error) {
	return false, nil
}

func (r raceStore) Get(_ context.Context, id string) (Delivery, error) {
	return Delivery{ID: id, Status: r.winner}, nil
}

func TestDeliverTreatsOnlySettledDeliveriesAsDuplicates(t *testing.T) {
	for status, wantDuplicate := range map[Status]bool{
		StatusSending:    false,
		StatusFailed:     false,
		StatusSent:       true,
		StatusSuppressed: true,
		StatusBounced:    true,
	} {
		sender := &fakeSender{}
		deliveries := newTestDeliveries(raceStore{fakeStore: newFakeStore(), winner: status}, sender)

		outcome, err := deliveries.Deliver(context.Background(), verificationRequest)
		if wantDuplicate && (err != nil || outcome != OutcomeDuplicate) {
			t.Fatalf("lost the claim to a %s delivery: %q, %v; want duplicate", status, outcome, err)
		}
		if !wantDuplicate && !errors.Is(err, ErrInProgress) {
			t.Fatalf("lost the claim to a %s delivery: %q, %v; want ErrInProgress so the message is retried", status, outcome, err)
		}
		if len(sender.sent) != 0 {
			t.Fatalf("lost the claim to a %s delivery but sent anyway", status)
		}
	}
}

func TestDeliverTakesOverExpiredClaim(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	store.deliveries[verificationID()] = Delivery{ID: verificationID(), Status: StatusSending, LeaseExpiresAt: testNow.Add(-time.Second)}
	deliveries := newTestDeliveries(store, sender)

	outcome, err := deliveries.Deliver(context.Background(), verificationRequest)
	if err != nil || outcome != OutcomeSent {
		t.Fatalf("Deliver() = %q, %v; want sent after the abandoned claim expired", outcome, err)
	}
}

func TestDeliverSkipsSuppressedRecipient(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	store.suppressions["asha@example.com"] = Suppression{Address: "asha@example.com", Reason: ReasonBounce}
	deliveries := newTestDeliveries(store, sender)

	outcome, err := deliveries.Deliver(context.Background(), verificationRequest)
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if outcome != OutcomeSuppressed || len(sender.sent) != 0 {
		t.Fatalf("outcome = %q with %d sends, want suppressed and nothing sent", outcome, len(sender.sent))
	}
	if got := store.deliveries[verificationID()].Status; got != StatusSuppressed {
		t.Fatalf("recorded status = %q, want suppressed", got)
	}
}

func TestDeliverSkipsExpiredEmail(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	deliveries := newTestDeliveries(store, sender)
	request := verificationRequest
	request.ExpiresAt = testNow

	outcome, err := deliveries.Deliver(context.Background(), request)
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if outcome != OutcomeExpired || len(sender.sent) != 0 {
		t.Fatalf("outcome = %q with %d sends, want expired and nothing sent", outcome, len(sender.sent))
	}
	if got := store.deliveries[verificationID()]; got.Status != StatusExpired || !got.LeaseExpiresAt.IsZero() {
		t.Fatalf("recorded delivery = %+v, want expired without a claim lease", got)
	}

	outcome, err = deliveries.Deliver(context.Background(), request)
	if err != nil || outcome != OutcomeDuplicate {
		t.Fatalf("redelivery = %q, %v; want duplicate", outcome, err)
	}
}

func TestDeliverSendsJustBeforeExpiry(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	deliveries := newTestDeliveries(store, sender)
	request := verificationRequest
	request.ExpiresAt = testNow.Add(time.Second)

	if outcome, err := deliveries.Deliver(context.Background(), request); err != nil || outcome != OutcomeSent {
		t.Fatalf("Deliver() = %q, %v; want sent while the link still works", outcome, err)
	}
}

func TestDeliverRequiresEventRecipientAndTemplate(t *testing.T) {
	tests := map[string]func(*Request){
		"event id":  func(request *Request) { request.Event.ID = "" },
		"recipient": func(request *Request) { request.Recipient = "" },
		"template":  func(request *Request) { request.Message.Template = "" },
	}

	for name, clear := range tests {
		t.Run(name, func(t *testing.T) {
			request := verificationRequest
			clear(&request)
			deliveries := newTestDeliveries(newFakeStore(), &fakeSender{})
			if _, err := deliveries.Deliver(context.Background(), request); err == nil {
				t.Fatalf("Deliver() error = nil without %s", name)
			}
		})
	}
}

func TestIDIdentifiesEmailPerEvent(t *testing.T) {
	event := verificationRequest.Event
	base := ID(event, "verify-email", "asha@example.com")

	if got := ID(event, "verify-email", " ASHA@example.com "); got != base {
		t.Fatal("ID changed with the recipient's case or surrounding space")
	}
	otherEvent := event
	otherEvent.ID = "event-2"
	for name, other := range map[string]string{
		"event":     ID(otherEvent, "verify-email", "asha@example.com"),
		"template":  ID(event, "password-reset", "asha@example.com"),
		"recipient": ID(event, "verify-email", "ravi@example.com"),
	} {
		if other == base {
			t.Fatalf("ID did not change with the %s", name)
		}
	}
}

func TestRecordFeedbackSuppressesAndRecordsStatus(t *testing.T) {
	store, sender := newFakeStore(), &fakeSender{}
	deliveries := newTestDeliveries(store, sender)
	if _, err := deliveries.Deliver(context.Background(), verificationRequest); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	err := deliveries.RecordFeedback(context.Background(), Feedback{
		DeliveryID:   verificationID(),
		SESMessageID: "ses-1",
		Status:       StatusBounced,
		Recipients:   []string{"Asha@Example.com"},
		Detail:       "Permanent/General",
		Suppress:     true,
		Reason:       ReasonBounce,
	})
	if err != nil {
		t.Fatalf("RecordFeedback() error = %v", err)
	}
	if got := store.deliveries[verificationID()].Status; got != StatusBounced {
		t.Fatalf("status = %q, want bounced", got)
	}
	if got, ok := store.suppressions["asha@example.com"]; !ok || got.Reason != ReasonBounce || got.DeliveryID != verificationID() {
		t.Fatalf("suppression = %+v (present %v), want a bounce suppression for asha@example.com", got, ok)
	}
}

func TestRecordFeedbackKeepsLaterStatus(t *testing.T) {
	store := newFakeStore()
	store.deliveries[verificationID()] = Delivery{ID: verificationID(), Status: StatusDelivered}
	deliveries := newTestDeliveries(store, &fakeSender{})

	if err := deliveries.RecordFeedback(context.Background(), Feedback{DeliveryID: verificationID(), Status: StatusDelayed}); err != nil {
		t.Fatalf("RecordFeedback() error = %v", err)
	}
	if got := store.deliveries[verificationID()].Status; got != StatusDelivered {
		t.Fatalf("status = %q, want delivered to survive a late delay", got)
	}
}

func TestRecordFeedbackWithoutDeliveryStillSuppresses(t *testing.T) {
	store := newFakeStore()
	deliveries := newTestDeliveries(store, &fakeSender{})

	err := deliveries.RecordFeedback(context.Background(), Feedback{
		Status:     StatusComplained,
		Recipients: []string{"ravi@example.com"},
		Suppress:   true,
		Reason:     ReasonComplaint,
	})
	if err != nil {
		t.Fatalf("RecordFeedback() error = %v", err)
	}
	if _, ok := store.suppressions["ravi@example.com"]; !ok {
		t.Fatal("complaint without a delivery ID did not suppress the recipient")
	}
}

func TestStatusRankOrdersFeedback(t *testing.T) {
	order := []Status{StatusSending, StatusSent, StatusDelayed, StatusDelivered, StatusBounced, StatusComplained}
	for index := 1; index < len(order); index++ {
		if order[index-1].Rank() >= order[index].Rank() {
			t.Fatalf("%s ranks %d, not below %s at %d", order[index-1], order[index-1].Rank(), order[index], order[index].Rank())
		}
	}
	if StatusRejected.Rank() != StatusBounced.Rank() {
		t.Fatal("rejected and bounced should be equally final")
	}
}
