package providers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// hubspotSigned builds a delivery signed the way HubSpot signs one: the v3
// signature over method + URI + body + timestamp, keyed with the app's client
// secret and base64-encoded. The URI the signature covers is the absolute one
// HubSpot called, so a path is completed with the host here exactly as the
// provider completes it.
func hubspotSigned(secret []byte, method, uri string, body []byte, timestamp string) Delivered {
	signed := uri
	if uri != "" && uri[0] == '/' {
		signed = "https://conn.example.com" + hubspotDecodedQuery(uri)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(method + signed + string(body) + timestamp))
	header := http.Header{}
	header.Set("X-HubSpot-Signature-Version", "v3")
	header.Set("X-HubSpot-Signature-V3", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	header.Set("X-HubSpot-Request-Timestamp", timestamp)
	header.Set("Host", "conn.example.com")
	return Delivered{Header: header, Body: body, Method: method, URI: uri}
}

const hubspotTestRoute = "/v1/webhooks/hubspot/route-secret-0123456789abcdef"

// hubspotTestStamp is HubSpot's millisecond timestamp for a delivery arriving
// at fixedNow, so the replay window accepts it.
var hubspotTestStamp = strconv.FormatInt(fixedNow.UnixMilli(), 10)

// hubspotTestStampAgo is a timestamp a given time before it, for checking the
// window from either side.
func hubspotTestStampAgo(offset time.Duration) string {
	return strconv.FormatInt(fixedNow.Add(offset).UnixMilli(), 10)
}

func TestHubspotTarget(t *testing.T) {
	provider := HubSpot()
	// A HubSpot app delivers its accounts' events to one URL, so a
	// subscription names no target.
	if target, events, err := provider.Target(map[string]string{}, nil); err != nil || len(target) != 0 || len(events) != 0 {
		t.Fatalf("Target() = %v, %v, %v, want an empty target", target, events, err)
	}
	if _, _, err := provider.Target(map[string]string{}, []string{"contact.creation", "deal.propertyChange"}); err != nil {
		t.Fatalf("Target() with real event types error = %v", err)
	}
	if _, _, err := provider.Target(map[string]string{"portal": "123"}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target() with a target error = %v, want ErrInvalid", err)
	}
	if _, _, err := provider.Target(map[string]string{}, []string{"contact"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target() with a bare event name error = %v, want ErrInvalid", err)
	}
}

func TestHubspotAccountKey(t *testing.T) {
	provider := HubSpot()
	key, err := provider.AccountKey(domain.ProviderAccount{ID: "12345678", Name: "Acme"})
	if err != nil || key != "12345678" {
		t.Fatalf("AccountKey() = %q, %v, want the portal", key, err)
	}
	if _, err := provider.AccountKey(domain.ProviderAccount{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AccountKey() without a portal error = %v, want ErrInvalid", err)
	}
}

func TestHubspotReceive(t *testing.T) {
	secret := []byte("hubspot-client-secret-value")

	t.Run("a valid delivery reaches the portal it names", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":101,"eventId":5,"occurredAt":1790000000000,"eventType":"contact.propertyChange","subscriptionId":7,"attemptNumber":0,"propertyName":"email","propertyValue":"ada@example.com","changeSource":"CRM_UI"}]`)
		reception, err := hubspotReceive(hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, hubspotTestStamp), secret, fixedNow)
		if err != nil {
			t.Fatalf("hubspotReceive() error = %v", err)
		}
		if len(reception.AccountKeys) != 1 || reception.AccountKeys[0] != "33" {
			t.Fatalf("AccountKeys = %v, want the portal", reception.AccountKeys)
		}
		if len(reception.Events) != 1 {
			t.Fatalf("Events = %v, want 1", reception.Events)
		}
		event := reception.Events[0]
		if event.Name != "contact.propertyChange" || event.Type != "contact.propertyChange" {
			t.Errorf("Name, Type = %q, %q", event.Name, event.Type)
		}
		if event.Resource.Type != "contact" || event.Resource.ID != "101" || event.Resource.Name != "ada@example.com" {
			t.Errorf("Resource = %+v, want the contact and its new value", event.Resource)
		}
		if event.DeliveryID != "5" {
			t.Errorf("DeliveryID = %q, want the event ID", event.DeliveryID)
		}
		// HubSpot says eventId is not unique and a retry repeats it, so the
		// attempt number is what tells a redelivery from a new event.
		if event.Key != "33:5:0" {
			t.Errorf("Key = %q, want portal:event:attempt", event.Key)
		}
		if event.OccurredAt.IsZero() {
			t.Error("OccurredAt was not read from the delivery")
		}
	})

	t.Run("one delivery carries several events", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation","attemptNumber":0},` +
			`{"portalId":44,"objectId":2,"eventId":2,"eventType":"deal.creation","attemptNumber":0}]`)
		reception, err := hubspotReceive(hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, hubspotTestStamp), secret, fixedNow)
		if err != nil {
			t.Fatalf("hubspotReceive() error = %v", err)
		}
		if len(reception.Events) != 2 {
			t.Fatalf("Events = %d, want 2", len(reception.Events))
		}
		if len(reception.AccountKeys) != 2 || reception.AccountKeys[0] != "33" || reception.AccountKeys[1] != "44" {
			t.Fatalf("AccountKeys = %v, want both portals", reception.AccountKeys)
		}
		if reception.Events[1].Resource.Type != "deal" {
			t.Errorf("second event resource = %+v, want a deal", reception.Events[1].Resource)
		}
	})

	t.Run("a wrong signature is refused", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation"}]`)
		delivery := hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, hubspotTestStamp)
		delivery.Header.Set("X-HubSpot-Signature-V3", base64.StdEncoding.EncodeToString([]byte("not-the-signature")))
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() error = %v, want ErrUnverified", err)
		}
	})

	t.Run("a body altered after signing is refused", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation"}]`)
		delivery := hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, hubspotTestStamp)
		delivery.Body = []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.deletion"}]`)
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() error = %v, want ErrUnverified", err)
		}
	})

	t.Run("a missing signature is refused", func(t *testing.T) {
		delivery := Delivered{Header: http.Header{}, Body: []byte(`[]`), Method: http.MethodPost, URI: hubspotTestRoute}
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() error = %v, want ErrUnverified", err)
		}
	})

	// The five-minute window is the only replay protection v3 has, so it has
	// to actually reject a stale delivery.
	t.Run("a delivery older than the window is refused", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation"}]`)
		stale := hubspotTestStampAgo(-16 * time.Minute)
		delivery := hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, stale)
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() with a stale delivery error = %v, want ErrUnverified", err)
		}
	})

	// A timestamp far in the future is as suspect as an old one.
	t.Run("a delivery from the future is refused", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation"}]`)
		future := hubspotTestStampAgo(90 * time.Minute)
		delivery := hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, future)
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() with a future delivery error = %v, want ErrUnverified", err)
		}
	})

	// An older scheme has no replay protection at all, so a delivery that
	// names one is refused rather than quietly accepted.
	t.Run("an older signature version is refused", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation"}]`)
		delivery := hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, hubspotTestStamp)
		delivery.Header.Set("X-HubSpot-Signature-Version", "v2")
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() with a v2 delivery error = %v, want ErrUnverified", err)
		}
	})

	t.Run("a payload that is not an array is refused", func(t *testing.T) {
		body := []byte(`{"portalId":33}`)
		if _, err := hubspotReceive(hubspotSigned(secret, http.MethodPost, hubspotTestRoute, body, hubspotTestStamp), secret, fixedNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("hubspotReceive() error = %v, want ErrInvalid", err)
		}
	})

	// A signature is over the URI HubSpot called, so signing a different one
	// must not verify.
	t.Run("a delivery signed for another URI is refused", func(t *testing.T) {
		body := []byte(`[{"portalId":33,"objectId":1,"eventId":1,"eventType":"contact.creation"}]`)
		delivery := hubspotSigned(secret, http.MethodPost, "/v1/webhooks/hubspot/someone-elses-route", body, hubspotTestStamp)
		delivery.URI = hubspotTestRoute
		if _, err := hubspotReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("hubspotReceive() error = %v, want ErrUnverified", err)
		}
	})
}

// TestHubspotSignedURIDecodesOnlyTheQuery covers the detail implementers get
// wrong: HubSpot's own samples decode the query string only, so an encoded
// slash in the path must stay encoded.
func TestHubspotSignedURIDecodesOnlyTheQuery(t *testing.T) {
	for _, testCase := range []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "an encoded slash in the path stays encoded",
			uri:  "/v1/webhooks/hubspot/a%2Fb",
			want: "https://conn.example.com/v1/webhooks/hubspot/a%2Fb",
		},
		{
			name: "an encoded colon in the query is decoded",
			uri:  "/v1/webhooks/hubspot/route?at=2026-09-17T10%3A00%3A00Z",
			want: "https://conn.example.com/v1/webhooks/hubspot/route?at=2026-09-17T10:00:00Z",
		},
		{
			name: "an encoded question mark inside the query is decoded",
			uri:  "/v1/webhooks/hubspot/route?q=a%3Fb",
			want: "https://conn.example.com/v1/webhooks/hubspot/route?q=a?b",
		},
		{
			name: "the question mark opening the query is left alone",
			uri:  "/v1/webhooks/hubspot/route?a=1",
			want: "https://conn.example.com/v1/webhooks/hubspot/route?a=1",
		},
		{
			name: "a URI with no query is unchanged",
			uri:  hubspotTestRoute,
			want: "https://conn.example.com" + hubspotTestRoute,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			delivery := Delivered{URI: testCase.uri, Header: http.Header{}}
			delivery.Header.Set("Host", "conn.example.com")
			if got := hubspotSignedURI(delivery); got != testCase.want {
				t.Fatalf("hubspotSignedURI() = %q, want %q", got, testCase.want)
			}
		})
	}
}
