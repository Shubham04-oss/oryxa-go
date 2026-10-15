package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// fakeKMS wraps a data key as a handle and unwraps it only for exactly the
// encryption context it was generated for, as KMS does.
type fakeKMS struct {
	n    int
	keys map[string]fakeWrapped
}

type fakeWrapped struct {
	key     []byte
	context map[string]string
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, _ string, ec map[string]string) ([]byte, []byte, error) {
	f.n++
	if f.keys == nil {
		f.keys = map[string]fakeWrapped{}
	}
	key := sha256.Sum256([]byte(fmt.Sprint("data key ", f.n)))
	handle := fmt.Sprint("wrapped-", f.n)
	f.keys[handle] = fakeWrapped{key: key[:], context: maps.Clone(ec)}
	return append([]byte(nil), key[:]...), []byte(handle), nil
}

func (f *fakeKMS) DecryptDataKey(_ context.Context, encrypted []byte, ec map[string]string) ([]byte, error) {
	w, ok := f.keys[string(encrypted)]
	if !ok || !maps.Equal(w.context, ec) {
		return nil, errors.New("kms context mismatch")
	}
	return append([]byte(nil), w.key...), nil
}

func newSealer(t *testing.T) Sealer {
	t.Helper()
	s, err := New(&fakeKMS{}, "alias/oryxa-integrations")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const token = "ghp_distinctive_token_value"

var home = Where{Table: "credentials", Org: "org_a", Row: "conn_1/refresh"}

func TestRoundTrip(t *testing.T) {
	s := newSealer(t)
	sealed, err := s.Seal(context.Background(), home, []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(sealed)
	if bytes.Contains(stored, []byte(token)) {
		t.Fatal("the stored form holds the plaintext")
	}
	got, err := s.Open(context.Background(), home, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reveal() != token {
		t.Fatalf("opened %q", got.Reveal())
	}
	if fmt.Sprint(got) == token {
		t.Fatal("the opened secret prints in the clear")
	}
}

func TestEachSealUsesAFreshKey(t *testing.T) {
	s := newSealer(t)
	a, _ := s.Seal(context.Background(), home, []byte(token))
	b, _ := s.Seal(context.Background(), home, []byte(token))
	if bytes.Equal(a.EncryptedDataKey, b.EncryptedDataKey) || bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Fatal("two seals share a key or ciphertext")
	}
}

func TestACopyElsewhereDoesNotOpen(t *testing.T) {
	s := newSealer(t)
	sealed, err := s.Seal(context.Background(), home, []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	for name, where := range map[string]Where{
		"another org":    {Table: home.Table, Org: "org_b", Row: home.Row},
		"another table":  {Table: "org_apps", Org: home.Org, Row: home.Row},
		"another row":    {Table: home.Table, Org: home.Org, Row: "conn_2/refresh"},
		"no org":         {Table: home.Table, Org: "", Row: home.Row},
		"org-shifted id": {Table: home.Table, Org: "org_a/conn_1", Row: "refresh"},
	} {
		if _, err := s.Open(context.Background(), where, sealed); err == nil {
			t.Errorf("%s: a copy opened", name)
		}
	}
}

func TestTamperedCiphertextDoesNotOpen(t *testing.T) {
	s := newSealer(t)
	sealed, _ := s.Seal(context.Background(), home, []byte(token))
	sealed.Ciphertext[0] ^= 1
	if _, err := s.Open(context.Background(), home, sealed); !errors.Is(err, secrets.ErrInvalidEnvelope) {
		t.Fatalf("err = %v", err)
	}
}

func TestIncompleteBindingIsRefused(t *testing.T) {
	s := newSealer(t)
	for _, where := range []Where{{Row: "r"}, {Table: "t"}, {Table: "t", Org: "a/b", Row: "r"}} {
		if _, err := s.Seal(context.Background(), where, []byte(token)); !errors.Is(err, secrets.ErrInvalidEnvelope) {
			t.Errorf("%+v: err = %v", where, err)
		}
	}
}

func TestNewNeedsKeys(t *testing.T) {
	if _, err := New(nil, "k"); err == nil {
		t.Error("no keys accepted")
	}
	if _, err := New(&fakeKMS{}, ""); err == nil {
		t.Error("no key id accepted")
	}
}
