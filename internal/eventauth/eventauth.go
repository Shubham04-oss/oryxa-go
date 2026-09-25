// Package eventauth proves who published an event, so a message on a queue
// cannot claim to be from a service that never sent it.
//
// The queues an event lands on are reachable only through their EventBridge
// rules today, and that is a strong boundary — but it is a boundary of
// configuration, not of proof: one mis-scoped SendMessage policy, or one
// compromised publisher, and an attacker could speak as any service to any
// consumer, including the destructive events (an organization deleted, a team
// gone) whose payloads consumers act on without asking anyone. So the
// envelope carries a signature over exactly the bytes a consumer reads to
// decide and to act, made with the publisher's own key, and a consumer
// verifies it before any handler runs.
//
// A signature never names its own key. The consumer resolves the key from
// the source the event claims — the same source its subscription already
// matched — so a signature made with one key cannot be replayed as another
// service by editing the envelope. A service given a KMS key signs with it
// and is verified through it. A service without one signs with a P-256 key
// its process minted at boot, which every consumer in that process trusts —
// and which dies with the process: an event still on a queue when the process
// restarts, or published by the old task of a rolling deploy, cannot be
// verified by the next one. So refusing what does not verify is for a
// deployment whose every service has a KMS key; until then a registry is
// report-only (ReportOnly), and every event is signed and checked while a
// failure is reported rather than refused.
//
// External sources — SES's own feedback, published by AWS rather than by an
// Oryxa service — are exempt: no Oryxa key could sign them, and their
// authenticity rests on the queue policy, as it always has.
package eventauth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// ErrUnverified means an event's signature is missing, malformed, not the
// claimed source's, or too old. Nothing in the event was used.
var ErrUnverified = errors.New("the event is not signed by the service it claims to be from")

// maxAge bounds how long a signature stays valid, unless a verifier is given
// MaxAge. A replayed event is a duplicate to a consumer that is idempotent —
// and these consumers are — but a bound keeps an old message from being
// replayed at a moment of someone's choosing for ever.
const maxAge = 24 * time.Hour

// SignatureVersion is the wire version of the sig field this package writes
// and reads.
const SignatureVersion = "v1"

// IsExternal reports whether source publishes from outside Oryxa and so
// carries no Oryxa signature. AWS SES publishes its own delivery feedback;
// a consumer still knows the delivery came from its rule, and that boundary
// is the one this package does not replace.
func IsExternal(source string) bool { return source == "aws.ses" }

// canonical is the string a signature is computed over: the whole decision
// surface of an event — who says it (source), what it is (detail type), its
// identity, its envelope version, when it happened, when it was signed, and
// what it carries. The NUL separators mean no value can make two different
// events canonicalize the same, whatever a JSON encoder does with either.
//
// signedAt is covered because a verifier bounds a signature's age by it. Left
// outside, it would be the one field an attacker could edit freely: rewriting
// it on a captured envelope would lift that bound and make a stale event fresh
// again, with the signature still matching every field it did cover.
//
// The data is normalized first, because the bus is not a byte pipe:
// EventBridge itself decodes and re-encodes an event's detail on the way
// through, which reorders object keys. Two semantically equal JSON documents
// are therefore the same data, and canonicalData makes both sides of a
// signature agree on one encoding of it.
func canonical(source, detailType, eventID string, version int, occurredAt string, signedAt int64, data []byte) []byte {
	out := make([]byte, 0, len(source)+len(detailType)+len(eventID)+len(occurredAt)+len(data)+64)
	out = append(out, "oryxa.event.v1\x00"...)
	out = append(out, source...)
	out = append(out, '\x00')
	out = append(out, detailType...)
	out = append(out, '\x00')
	out = append(out, eventID...)
	out = append(out, '\x00')
	out = strconv.AppendInt(out, int64(version), 10)
	out = append(out, '\x00')
	out = append(out, occurredAt...)
	out = append(out, '\x00')
	out = strconv.AppendInt(out, signedAt, 10)
	out = append(out, '\x00')
	out = append(out, canonicalJSON(data)...)
	return out
}

// canonicalJSON re-encodes data so that any two encodings of the same JSON
// value — the publisher's and whatever the bus re-serialized on the way
// through — produce the same bytes. Object keys are sorted, and nesting,
// arrays and scalars keep their structure. Numbers keep their exact value:
// an integer a float64 could not hold stays literal (9007199254740993 never
// becomes 9007199254740992), while every spelling of one value — 1, 1.0,
// 1e0 — meets as one rendering, so a value is what a signature covers, not
// the way it was spelled. Data that is not a JSON value at all is used as
// it is: a publisher that signs non-JSON bytes is signing exactly what it
// carries, and a consumer of the same bytes still agrees.
func canonicalJSON(data []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return data
	}
	normalized, err := json.Marshal(canonicalValue(value))
	if err != nil {
		return data
	}
	return normalized
}

// canonicalValue walks a decoded JSON value, rendering every number as its
// one canonical spelling. Keys are sorted by the marshal itself, since a
// decoded object is a Go map.
func canonicalValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return json.Number(canonicalNumber(typed.String()))
	case map[string]any:
		for key, child := range typed {
			typed[key] = canonicalValue(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = canonicalValue(child)
		}
		return typed
	default:
		return value
	}
}

// canonicalNumber renders one JSON number literal so that every spelling of
// the same value yields the same bytes, and two different values never
// yield the same bytes. Whole numbers spelled plainly keep their exact
// digits however large — a float64 cannot tell 9007199254740993 from
// 9007199254740992, and a signature must — while a whole value spelled with
// a fraction or exponent (1.0, 1e0) meets its integer form, and a genuine
// fraction takes the shortest spelling that parses back to itself.
func canonicalNumber(literal string) string {
	trimmed := strings.TrimSpace(literal)
	if exact, ok := new(big.Int).SetString(trimmed, 10); ok {
		return exact.String()
	}
	if value, err := strconv.ParseFloat(trimmed, 64); err == nil {
		if value == math.Trunc(value) && !math.IsInf(value, 0) && value >= -(1<<63) && value < (1<<63) {
			return strconv.FormatInt(int64(value), 10)
		}
		short := strconv.FormatFloat(value, 'g', -1, 64)
		if back, err := strconv.ParseFloat(short, 64); err == nil && back == value {
			return short
		}
	}
	return literal
}

// joseRS turns a 64-byte JOSE r||s signature into its two integers.
func joseRS(signature []byte) (r, s *big.Int, err error) {
	if len(signature) != 64 {
		return nil, nil, errors.New("signature is not 64 bytes")
	}
	return new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:]), nil
}

// parseDERSignature converts an ASN.1 DER ECDSA signature, as KMS's Sign
// returns, into the two integers of the same signature.
func parseDERSignature(der []byte) (r, s *big.Int, err error) {
	var parsed struct {
		R, S *big.Int
	}
	rest, err := asn1.Unmarshal(der, &parsed)
	if err != nil {
		return nil, nil, fmt.Errorf("read the KMS signature: %w", err)
	}
	if len(rest) != 0 || parsed.R == nil || parsed.S == nil || parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
		return nil, nil, errors.New("the KMS signature is malformed")
	}
	if parsed.R.BitLen() > 256 || parsed.S.BitLen() > 256 {
		return nil, nil, errors.New("the KMS signature exceeds 32 bytes a side")
	}
	return parsed.R, parsed.S, nil
}

// SignAPI is the KMS surface a KMS-backed signer needs.
type SignAPI interface {
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

// KMSSigner signs events with a service's KMS key, for a deployment whose
// services do not share a process. The signature it writes is the same form
// the local signer writes; only the key behind it differs.
type KMSSigner struct {
	Client SignAPI
	KeyID  string
	now    func() time.Time
}

// NewKMSSigner returns a signer for service through the KMS key named.
func NewKMSSigner(client SignAPI, keyID string, now func() time.Time) (*Signer, error) {
	if keyID == "" {
		return nil, errors.New("eventauth: a KMS signer needs a key ID")
	}
	if now == nil {
		now = time.Now
	}
	return &Signer{kms: &KMSSigner{Client: client, KeyID: keyID, now: now}}, nil
}

// Sign signs the canonical payload with the service's KMS key. KMS answers
// an ASN.1 DER signature; the JOSE r||s form this package writes is what a
// verifier reads, so it is converted here.
func (signer *KMSSigner) Sign(source, detailType, eventID string, version int, occurredAt string, data []byte) (string, error) {
	// The signing time is read once and both signed and written, so the field
	// a verifier ages the signature by is the field the signature covers.
	signedAt := signer.now().Unix()
	sum := sha256.Sum256(canonical(source, detailType, eventID, version, occurredAt, signedAt, data))
	output, err := signer.Client.Sign(context.Background(), &kms.SignInput{
		KeyId:            &signer.KeyID,
		Message:          sum[:],
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return "", fmt.Errorf("eventauth: sign with KMS: %w", err)
	}
	r, s, err := parseDERSignature(output.Signature)
	if err != nil {
		return "", fmt.Errorf("eventauth: %w", err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return fmt.Sprintf("%s.%d.%s", SignatureVersion, signedAt,
		base64.RawURLEncoding.EncodeToString(signature)), nil
}

// Signer signs events as one service, with whichever key it holds: a local
// P-256 key for a process that runs services together, or the service's KMS
// key for a split deployment. It is safe for concurrent use.
type Signer struct {
	service string
	key     *ecdsa.PrivateKey
	kms     *KMSSigner
	now     func() time.Time
}

// NewLocalSigner mints a signer with a fresh local P-256 key, for a
// deployment that runs its services together and does not reach KMS: the
// chaos rig, a development stack. The public key is what a consumer of the
// same process trusts for the service.
func NewLocalSigner(service string, now func() time.Time) (*Signer, *ecdsa.PublicKey, error) {
	if service == "" {
		return nil, nil, errors.New("eventauth: a signer names a service")
	}
	if now == nil {
		now = time.Now
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("eventauth: mint a local key for %s: %w", service, err)
	}
	return &Signer{service: service, key: key, now: now}, &key.PublicKey, nil
}

// Sign returns the sig field for one event: the signature version, the
// signing time, and the signature itself. It signs the exact content the
// consumer will read, so nothing between the publisher and the handler can
// change what the signature vouches for.
func (signer *Signer) Sign(source, detailType, eventID string, version int, occurredAt string, data []byte) (string, error) {
	if signer.kms != nil {
		return signer.kms.Sign(source, detailType, eventID, version, occurredAt, data)
	}
	if signer.key == nil {
		return "", fmt.Errorf("eventauth: %s has no signing key: %w", signer.service, ErrUnverified)
	}
	// The signing time is read once and both signed and written, so the field
	// a verifier ages the signature by is the field the signature covers.
	signedAt := signer.now().Unix()
	sum := sha256.Sum256(canonical(source, detailType, eventID, version, occurredAt, signedAt, data))
	r, s, err := ecdsa.Sign(rand.Reader, signer.key, sum[:])
	if err != nil {
		return "", fmt.Errorf("eventauth: sign as %s: %w", signer.service, err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return fmt.Sprintf("%s.%d.%s", SignatureVersion, signedAt,
		base64.RawURLEncoding.EncodeToString(signature)), nil
}

// PublicKeyAPI is the KMS surface a verifier needs to resolve a publisher's
// key. It is what *kms.Client satisfies.
type PublicKeyAPI interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// KMSKeys resolves signing keys from KMS: a service name to its key ID.
type KMSKeys struct {
	Client PublicKeyAPI
	// KeyIDs maps a service name, as a source names it ("teams" for
	// "oryxa.teams"), to the KMS key that service signs events with.
	KeyIDs map[string]string
}

// Verifier checks event signatures against the keys its process holds or
// can reach. It is safe for concurrent use.
type Verifier struct {
	mu    sync.RWMutex
	local map[string]*ecdsa.PublicKey
	// registry, for a verifier a registry handed out, is read at every check:
	// for the keys it has minted since, and for whether it is report-only.
	registry *Registry
	kms      *KMSKeys
	resolved map[string]*ecdsa.PublicKey
	now      func() time.Time
	// maxAge bounds how long ago a signature may have been made: the
	// package's maxAge, unless MaxAge set another.
	maxAge time.Duration
}

// VerifierOption adjusts a verifier NewVerifier or Registry.Verifier returns.
type VerifierOption func(*Verifier)

// MaxAge has a verifier accept a signature made up to age ago, in place of the
// default day. It is for a consumer whose events can wait on its queue longer
// than a day, or be replayed from the bus's archive, and that is harmless to
// act on late: an idempotent one, to which a replay is only a repeat. A
// signature from the future is held to the default day however long age is,
// since no event waits there. Zero or less keeps the default.
func MaxAge(age time.Duration) VerifierOption {
	return func(verifier *Verifier) {
		if age > 0 {
			verifier.maxAge = age
		}
	}
}

// Report is told of each event a report-only verifier let through although
// its signature did not check out: the source it claimed, its detail type,
// its ID, and why it failed.
type Report func(source, detailType, eventID string, err error)

// NewVerifier returns a verifier that trusts the local keys given and, when
// a source's key is not among them, the KMS keys named. Either may be empty.
func NewVerifier(local map[string]*ecdsa.PublicKey, kmsKeys *KMSKeys, now func() time.Time, options ...VerifierOption) *Verifier {
	if now == nil {
		now = time.Now
	}
	verifier := &Verifier{local: local, kms: kmsKeys, resolved: map[string]*ecdsa.PublicKey{}, now: now, maxAge: maxAge}
	for _, option := range options {
		option(verifier)
	}
	return verifier
}

// Verify checks one event's signature against the key its source resolves
// to. It answers ErrUnverified — and nothing else about the event is used —
// when the signature is absent, malformed, stale, or not the source's own.
// A verifier from a report-only registry tells the registry's Report instead
// and answers nil, so the event is handled as it was before events were
// signed.
func (verifier *Verifier) Verify(source, detailType, eventID string, version int, occurredAt string, data []byte, sig string) error {
	err := verifier.check(source, detailType, eventID, version, occurredAt, data, sig)
	if err == nil {
		return nil
	}
	if verifier.registry != nil {
		if report := verifier.registry.reporter(); report != nil {
			report(source, detailType, eventID, err)
			return nil
		}
	}
	return err
}

func (verifier *Verifier) check(source, detailType, eventID string, version int, occurredAt string, data []byte, sig string) error {
	if IsExternal(source) {
		return nil
	}
	service, ok := strings.CutPrefix(source, "oryxa.")
	if !ok {
		return fmt.Errorf("%w: %q is not an Oryxa source", ErrUnverified, source)
	}
	parts := strings.SplitN(sig, ".", 3)
	if len(parts) != 3 || parts[0] != SignatureVersion {
		return fmt.Errorf("%w: the signature is missing or of another version", ErrUnverified)
	}
	seconds, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("%w: the signature's time is not a number", ErrUnverified)
	}
	signed := time.Unix(seconds, 0)
	if age := verifier.now().Sub(signed); age > verifier.maxAge || age < -maxAge {
		return fmt.Errorf("%w: the signature is too old", ErrUnverified)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: the signature is not encoded properly", ErrUnverified)
	}
	r, s, err := joseRS(signature)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnverified, err)
	}
	key, err := verifier.key(service)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnverified, err)
	}
	// seconds is the time the age was bounded by above; signing over it too
	// means a rewritten timestamp fails here rather than buying a fresh window.
	digest := sha256.Sum256(canonical(source, detailType, eventID, version, occurredAt, seconds, data))
	if !ecdsa.Verify(key, digest[:], r, s) {
		return fmt.Errorf("%w: the signature does not match", ErrUnverified)
	}
	return nil
}

// key resolves the public key a service signs with. Local keys first — the
// ones this verifier was built with, then any its registry has minted,
// however long after this verifier was handed out — then KMS, cached per
// service so a hot consumer path never waits on KMS after its first event.
func (verifier *Verifier) key(service string) (*ecdsa.PublicKey, error) {
	verifier.mu.RLock()
	key, ok := verifier.local[service]
	verifier.mu.RUnlock()
	if ok {
		return key, nil
	}
	if verifier.registry != nil {
		if key, ok := verifier.registry.localKey(service); ok {
			return key, nil
		}
	}
	verifier.mu.RLock()
	key, ok = verifier.resolved[service]
	verifier.mu.RUnlock()
	if ok {
		return key, nil
	}
	if verifier.kms == nil {
		return nil, fmt.Errorf("no key for %s", service)
	}
	keyID, ok := verifier.kms.KeyIDs[service]
	if !ok {
		return nil, fmt.Errorf("no key configured for %s", service)
	}
	output, err := verifier.kms.Client.GetPublicKey(context.Background(), &kms.GetPublicKeyInput{KeyId: &keyID})
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(output.PublicKey)
	if err != nil {
		return nil, err
	}
	ecdsaKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || ecdsaKey.Curve != elliptic.P256() {
		return nil, errors.New("the key is not P-256 ECDSA")
	}
	verifier.mu.Lock()
	verifier.resolved[service] = ecdsaKey
	verifier.mu.Unlock()
	return ecdsaKey, nil
}

// Registry is the process's signing identities. The host builds one before
// any service mounts, hands each its signer, and hands every consumer the
// verifier that trusts them all — so a deployment that runs every service in
// one process needs no configuration for events to be signed and checked.
type Registry struct {
	mu      sync.Mutex
	local   map[string]*ecdsa.PublicKey
	signers map[string]*Signer
	kms     KMSResolver
	now     func() time.Time
	// report, when set, makes the registry report-only: see ReportOnly.
	report Report
}

// KMSResolver is how a split deployment names each service's signing key and
// the client that reaches it. A nil resolver means the process has no KMS
// keys and mints local ones instead.
type KMSResolver interface {
	// Signer returns the signer for a service, or nil when the service has
	// no KMS key configured.
	Signer(service string) (*Signer, bool, error)
	// Keys is what a verifier resolves those services' public keys with.
	Keys() *KMSKeys
}

// NewRegistry returns an empty registry. kms, when set, is how every service
// signs and is verified in a deployment that splits services across
// processes; nil, each service this process mounts gets a local key.
func NewRegistry(kms KMSResolver, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{
		local:   map[string]*ecdsa.PublicKey{},
		signers: map[string]*Signer{},
		kms:     kms,
		now:     now,
	}
}

// Signer returns the signing identity for a service, creating it once: the
// service's KMS key when one is configured, else a local P-256 key this
// process minted, which the registry's verifier trusts directly. A nil
// registry answers that no identities exist, rather than panicking in
// whichever service asked.
func (registry *Registry) Signer(service string) (*Signer, error) {
	if registry == nil {
		return nil, fmt.Errorf("eventauth: no signing identities are configured: %s cannot sign its events", service)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if signer, ok := registry.signers[service]; ok {
		return signer, nil
	}
	if registry.kms != nil {
		signer, configured, err := registry.kms.Signer(service)
		if err != nil {
			return nil, err
		}
		if configured {
			registry.signers[service] = signer
			return signer, nil
		}
	}
	signer, publicKey, err := NewLocalSigner(service, registry.now)
	if err != nil {
		return nil, err
	}
	registry.signers[service] = signer
	registry.local[service] = publicKey
	return signer, nil
}

// Verifier returns the verifier for everything this registry knows: every
// local key it has minted or will mint, and the KMS keys of a split
// deployment. Services mount one after another, each taking a verifier as it
// goes, so a verifier that knew only the keys minted before it would refuse
// every service mounted later; this one reads the registry at each check. A
// nil registry answers with a verifier that refuses everything, so a consumer
// built without identities fails closed.
func (registry *Registry) Verifier(options ...VerifierOption) *Verifier {
	if registry == nil {
		return NewVerifier(nil, nil, time.Now, options...)
	}
	var keys *KMSKeys
	if registry.kms != nil {
		keys = registry.kms.Keys()
	}
	verifier := NewVerifier(nil, keys, registry.now, options...)
	verifier.registry = registry
	return verifier
}

// ReportOnly makes the registry's verifiers — those handed out already, and
// those to come — let an event through when its signature does not check out,
// telling report instead of refusing it. It is how signing is rolled out, and
// what a process needs while any of its services signs with a key minted at
// boot: every event is still signed and checked, and what enforcing would
// refuse is visible before anything is refused.
func (registry *Registry) ReportOnly(report Report) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.report = report
}

// Minted names, in order, the services this registry has minted a local key
// for: those that took a signer and have no KMS key. A service that only
// consumes takes no signer, so it is never among them.
func (registry *Registry) Minted() []string {
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return slices.Sorted(maps.Keys(registry.local))
}

// localKey returns the public half of the key this registry minted for
// service, if it has minted one.
func (registry *Registry) localKey(service string) (*ecdsa.PublicKey, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	key, ok := registry.local[service]
	return key, ok
}

// reporter returns the registry's Report, or nil when it enforces.
func (registry *Registry) reporter() Report {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.report
}
