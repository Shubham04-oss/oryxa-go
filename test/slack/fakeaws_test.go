package slack

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAWS is the part of AWS the two services under test reach, in one server
// that AWS_ENDPOINT_URL points every SDK client at: KMS, which signs service
// tokens and seals what apps and agents keep; the event bus and the one SQS
// queue agents reads; and Bedrock's guardrails. It keeps no state that outlives
// the test, and calls nothing.
//
// The bus is real in the one way that matters here: what a service publishes
// is delivered to agents' queue, as the rule agents' provisioner makes would
// deliver it, and agents' consumer reads it from there by the SQS API, so every
// event travels the way it does in production, signed and verified, with the
// consumer's own polling, visibility and deletion.
type fakeAWS struct {
	t      testing.TB
	server *httptest.Server

	mu       sync.Mutex
	keys     map[string]*ecdsa.PrivateKey
	blobs    map[string]dataKey
	queued   []*queuedMessage
	events   []busEvent
	sequence int

	// guardrail answers a call to Bedrock's ApplyGuardrail. Tests replace it to
	// make a guardrail fail, or score an answer as they choose.
	guardrail func(guardrailCall) guardrailAnswer
}

// dataKey is a data key KMS made, which a ciphertext blob stands for. It opens
// only with the encryption context it was made with.
type dataKey struct {
	plaintext []byte
	context   map[string]string
}

type queuedMessage struct {
	id, receipt, body string
	invisibleUntil    time.Time
	deleted           bool
}

// busEvent is one event a service published.
type busEvent struct {
	Source     string
	DetailType string
	Detail     json.RawMessage
	At         time.Time
}

// queueName is the name of the one queue, the one agents' events rule feeds.
const queueName = "agents-events"

func newFakeAWS(t testing.TB) *fakeAWS {
	t.Helper()
	f := &fakeAWS{
		t: t, keys: map[string]*ecdsa.PrivateKey{}, blobs: map[string]dataKey{},
		guardrail: defaultGuardrail,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// URL is the endpoint every AWS client of the process is pointed at.
func (f *fakeAWS) URL() string { return f.server.URL }

// QueueURL is the URL of agents' queue.
func (f *fakeAWS) QueueURL() string { return f.server.URL + "/000000000000/" + queueName }

func (f *fakeAWS) serve(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	target := request.Header.Get("X-Amz-Target")
	switch {
	case strings.HasPrefix(target, "TrentService."):
		f.kms(writer, strings.TrimPrefix(target, "TrentService."), body)
	case strings.HasPrefix(target, "AWSEvents."):
		f.eventbridge(writer, strings.TrimPrefix(target, "AWSEvents."), body)
	case strings.HasPrefix(target, "AmazonSQS."):
		f.sqs(writer, strings.TrimPrefix(target, "AmazonSQS."), body)
	case strings.HasPrefix(request.URL.Path, "/guardrail/"):
		f.bedrock(writer, request.URL.Path, body)
	default:
		f.t.Logf("fake AWS: no such call %s %s (%s)", request.Method, request.URL.Path, target)
		jsonError(writer, http.StatusNotFound, "UnknownOperationException", "the fake AWS does not know "+target+request.URL.Path)
	}
}

func jsonError(writer http.ResponseWriter, status int, kind, message string) {
	writer.Header().Set("Content-Type", "application/x-amz-json-1.1")
	writer.Header().Set("X-Amzn-Errortype", kind)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"__type": kind, "message": message})
}

func jsonReply(writer http.ResponseWriter, contentType string, value any) {
	writer.Header().Set("Content-Type", contentType)
	_ = json.NewEncoder(writer).Encode(value)
}

// ---- KMS ----

func (f *fakeAWS) keyFor(id string) *ecdsa.PrivateKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.keys[id]
	if !ok {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			panic(err)
		}
		f.keys[id] = key
	}
	return key
}

func (f *fakeAWS) kms(writer http.ResponseWriter, operation string, body []byte) {
	var in struct {
		KeyID             string            `json:"KeyId"`
		Message           []byte            `json:"Message"`
		CiphertextBlob    []byte            `json:"CiphertextBlob"`
		EncryptionContext map[string]string `json:"EncryptionContext"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		jsonError(writer, http.StatusBadRequest, "ValidationException", err.Error())
		return
	}
	switch operation {
	case "Sign":
		digest := sha256.Sum256(in.Message)
		signature, err := ecdsa.SignASN1(rand.Reader, f.keyFor(in.KeyID), digest[:])
		if err != nil {
			jsonError(writer, http.StatusInternalServerError, "KMSInternalException", err.Error())
			return
		}
		jsonReply(writer, "application/x-amz-json-1.1", map[string]any{
			"KeyId": in.KeyID, "Signature": signature, "SigningAlgorithm": "ECDSA_SHA_256",
		})
	case "GetPublicKey":
		der, err := x509.MarshalPKIXPublicKey(&f.keyFor(in.KeyID).PublicKey)
		if err != nil {
			jsonError(writer, http.StatusInternalServerError, "KMSInternalException", err.Error())
			return
		}
		jsonReply(writer, "application/x-amz-json-1.1", map[string]any{
			"KeyId": in.KeyID, "PublicKey": der, "KeySpec": "ECC_NIST_P256", "KeyUsage": "SIGN_VERIFY",
			"SigningAlgorithms": []string{"ECDSA_SHA_256"},
		})
	case "GenerateDataKey":
		plaintext := make([]byte, 32)
		blob := make([]byte, 24)
		_, _ = rand.Read(plaintext)
		_, _ = rand.Read(blob)
		f.mu.Lock()
		f.blobs[string(blob)] = dataKey{plaintext: plaintext, context: in.EncryptionContext}
		f.mu.Unlock()
		jsonReply(writer, "application/x-amz-json-1.1", map[string]any{
			"KeyId": in.KeyID, "Plaintext": plaintext, "CiphertextBlob": blob,
		})
	case "Decrypt":
		f.mu.Lock()
		key, ok := f.blobs[string(in.CiphertextBlob)]
		f.mu.Unlock()
		// A data key opens only under the context it was made with, which is
		// what binds a sealed value to the service, the resource type and the
		// row it was sealed for.
		if !ok || !sameContext(key.context, in.EncryptionContext) {
			jsonError(writer, http.StatusBadRequest, "InvalidCiphertextException", "the ciphertext cannot be opened with that context")
			return
		}
		jsonReply(writer, "application/x-amz-json-1.1", map[string]any{"KeyId": in.KeyID, "Plaintext": key.plaintext})
	default:
		jsonError(writer, http.StatusBadRequest, "UnknownOperationException", "the fake KMS does not do "+operation)
	}
}

// sameContext reports whether two encryption contexts are the same one, an
// absent context being an empty one.
func sameContext(made, given map[string]string) bool {
	if len(made) != len(given) {
		return false
	}
	for name, value := range made {
		if other, ok := given[name]; !ok || other != value {
			return false
		}
	}
	return true
}

// ---- EventBridge and SQS ----

// The rule agents' provisioner makes matches these, so they are what reaches its
// queue: apps' three Slack events, and the ledger's request events except the
// two that nothing subscribes to.
func agentsRuleMatches(source, detailType string) bool {
	switch {
	case source == "oryxa.apps" && strings.HasPrefix(detailType, "apps.slack."):
		return true
	case source == "oryxa.agents" && strings.HasPrefix(detailType, "agents.interaction."):
		return detailType != "agents.interaction.lease.revoked.v1" && detailType != "agents.interaction.secret.viewed.v1"
	}
	return false
}

func (f *fakeAWS) eventbridge(writer http.ResponseWriter, operation string, body []byte) {
	if operation != "PutEvents" {
		jsonError(writer, http.StatusBadRequest, "UnknownOperationException", "the fake EventBridge does not do "+operation)
		return
	}
	var in struct {
		Entries []struct {
			Source, DetailType, Detail string
		}
	}
	if err := json.Unmarshal(body, &in); err != nil {
		jsonError(writer, http.StatusBadRequest, "ValidationException", err.Error())
		return
	}
	entries := make([]map[string]string, 0, len(in.Entries))
	for _, entry := range in.Entries {
		f.mu.Lock()
		f.sequence++
		id := fmt.Sprintf("evt-%06d", f.sequence)
		f.events = append(f.events, busEvent{Source: entry.Source, DetailType: entry.DetailType, Detail: json.RawMessage(entry.Detail), At: time.Now()})
		if agentsRuleMatches(entry.Source, entry.DetailType) {
			envelope, _ := json.Marshal(map[string]any{
				"version": "0", "id": id, "detail-type": entry.DetailType, "source": entry.Source, "account": "000000000000",
				"time": time.Now().UTC().Format(time.RFC3339), "region": "us-west-2", "resources": []string{},
				"detail": json.RawMessage(entry.Detail),
			})
			f.queued = append(f.queued, &queuedMessage{id: id, receipt: "rh-" + id, body: string(envelope)})
		}
		f.mu.Unlock()
		entries = append(entries, map[string]string{"EventId": id})
	}
	jsonReply(writer, "application/x-amz-json-1.1", map[string]any{"FailedEntryCount": 0, "Entries": entries})
}

// published are the events the services published, oldest first, of the detail
// types named, or all of them when none is.
func (f *fakeAWS) published(detailTypes ...string) []busEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []busEvent
	for _, event := range f.events {
		if len(detailTypes) == 0 || contains(detailTypes, event.DetailType) {
			found = append(found, event)
		}
	}
	return found
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (f *fakeAWS) sqs(writer http.ResponseWriter, operation string, body []byte) {
	var in struct {
		QueueURL            string `json:"QueueUrl"`
		MaxNumberOfMessages int    `json:"MaxNumberOfMessages"`
		WaitTimeSeconds     int    `json:"WaitTimeSeconds"`
		ReceiptHandle       string `json:"ReceiptHandle"`
		VisibilityTimeout   int    `json:"VisibilityTimeout"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		jsonError(writer, http.StatusBadRequest, "ValidationException", err.Error())
		return
	}
	switch operation {
	case "ReceiveMessage":
		// A long poll waits for a message, and here for less than it was told to:
		// the queue is never left idle for twenty seconds under a test.
		deadline := time.Now().Add(min(time.Duration(in.WaitTimeSeconds)*time.Second, 600*time.Millisecond))
		for {
			messages := f.take(max(in.MaxNumberOfMessages, 1))
			if len(messages) > 0 || !time.Now().Before(deadline) {
				jsonReply(writer, "application/x-amz-json-1.0", map[string]any{"Messages": messages})
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	case "DeleteMessage":
		f.mu.Lock()
		for _, message := range f.queued {
			if message.receipt == in.ReceiptHandle {
				message.deleted = true
			}
		}
		f.mu.Unlock()
		jsonReply(writer, "application/x-amz-json-1.0", map[string]any{})
	case "ChangeMessageVisibility":
		f.mu.Lock()
		for _, message := range f.queued {
			if message.receipt == in.ReceiptHandle && !message.deleted {
				message.invisibleUntil = time.Now().Add(time.Duration(in.VisibilityTimeout) * time.Second)
			}
		}
		f.mu.Unlock()
		jsonReply(writer, "application/x-amz-json-1.0", map[string]any{})
	default:
		jsonError(writer, http.StatusBadRequest, "UnknownOperationException", "the fake SQS does not do "+operation)
	}
}

// take hands out up to limit visible messages, each invisible for a minute, the
// visibility timeout agents' provisioner sets.
func (f *fakeAWS) take(limit int) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	var out []map[string]any
	for _, message := range f.queued {
		if len(out) >= limit {
			break
		}
		if message.deleted || now.Before(message.invisibleUntil) {
			continue
		}
		message.invisibleUntil = now.Add(time.Minute)
		sum := md5.Sum([]byte(message.body))
		out = append(out, map[string]any{
			"MessageId": message.id, "ReceiptHandle": message.receipt, "Body": message.body, "MD5OfBody": hex.EncodeToString(sum[:]),
		})
	}
	return out
}

// queueDepth is how many messages wait in agents' queue, undelivered or in
// flight, which is nothing once the consumer has handled everything.
func (f *fakeAWS) queueDepth() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	depth := 0
	for _, message := range f.queued {
		if !message.deleted {
			depth++
		}
	}
	return depth
}

// ---- Bedrock ----

// guardrailCall is a call to ApplyGuardrail, as much of it as a test decides on.
type guardrailCall struct {
	// Guardrail is the guardrail's identifier.
	Guardrail string
	// Grounding says the call carries a grounding source and a query, as the
	// consent guard's does; Source, Query and Answer are then the three.
	Grounding bool
	Source    string
	Query     string
	Answer    string
	// Text is what an ordinary call, the answer guard's, was asked about.
	Text string
}

// guardrailAnswer is what the fake answers: a status, and a body for a 200.
type guardrailAnswer struct {
	Status  int
	Kind    string
	Message string
	Body    map[string]any
}

// defaultGuardrail finds nothing in anything, and scores every answer as well
// grounded in its sources and as relevant as can be.
func defaultGuardrail(call guardrailCall) guardrailAnswer {
	if call.Grounding {
		return guardrailAnswer{Status: 200, Body: groundingBody(0.95, 0.95, nil)}
	}
	return guardrailAnswer{Status: 200, Body: map[string]any{"action": "NONE", "outputs": []any{}, "assessments": []any{}, "usage": map[string]any{}}}
}

// groundingBody is a contextual grounding response with the scores given, and
// the denied topics detected.
func groundingBody(grounded, relevant float64, topics []string) map[string]any {
	filter := func(kind string, threshold, score float64) map[string]any {
		detected := score < threshold
		action := "NONE"
		if detected {
			action = "BLOCKED"
		}
		return map[string]any{"type": kind, "threshold": threshold, "score": score, "action": action, "detected": detected}
	}
	assessment := map[string]any{"contextualGroundingPolicy": map[string]any{"filters": []any{
		filter("GROUNDING", 0.75, grounded), filter("RELEVANCE", 0.5, relevant),
	}}}
	if len(topics) > 0 {
		found := make([]any, 0, len(topics))
		for _, topic := range topics {
			found = append(found, map[string]any{"name": topic, "type": "DENY", "action": "BLOCKED", "detected": true})
		}
		assessment["topicPolicy"] = map[string]any{"topics": found}
	}
	return map[string]any{"action": "NONE", "outputs": []any{}, "assessments": []any{assessment}, "usage": map[string]any{}}
}

// failGuardrail is a guardrail that says no to the call, as one that is down or
// misconfigured does. A 400 is not retried by the SDK, so it fails at once.
func failGuardrail(guardrailCall) guardrailAnswer {
	return guardrailAnswer{Status: http.StatusBadRequest, Kind: "ValidationException", Message: "the guardrail is not set up for this"}
}

func (f *fakeAWS) bedrock(writer http.ResponseWriter, path string, body []byte) {
	// /guardrail/{id}/version/{version}/apply
	parts := strings.Split(strings.Trim(path, "/"), "/")
	call := guardrailCall{}
	if len(parts) >= 2 {
		call.Guardrail = parts[1]
	}
	var in struct {
		Content []struct {
			Text struct {
				Text       string   `json:"text"`
				Qualifiers []string `json:"qualifiers"`
			} `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		jsonError(writer, http.StatusBadRequest, "ValidationException", err.Error())
		return
	}
	for _, block := range in.Content {
		switch {
		case contains(block.Text.Qualifiers, "grounding_source"):
			call.Grounding, call.Source = true, block.Text.Text
		case contains(block.Text.Qualifiers, "query"):
			call.Query = block.Text.Text
		case contains(block.Text.Qualifiers, "guard_content"):
			call.Answer = block.Text.Text
		default:
			call.Text = block.Text.Text
		}
	}
	f.mu.Lock()
	decide := f.guardrail
	f.mu.Unlock()
	answer := decide(call)
	if answer.Status != http.StatusOK {
		jsonError(writer, answer.Status, answer.Kind, answer.Message)
		return
	}
	jsonReply(writer, "application/json", answer.Body)
}

// setGuardrail replaces what the fake Bedrock answers, and returns what puts the
// default back.
func (f *fakeAWS) setGuardrail(decide func(guardrailCall) guardrailAnswer) (restore func()) {
	f.mu.Lock()
	previous := f.guardrail
	f.guardrail = decide
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.guardrail = previous
		f.mu.Unlock()
	}
}
