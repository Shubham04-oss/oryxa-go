package secrets

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const plaintext = "sk-live-not-for-logs"

// holder is how a Secret usually travels: an exported field.
type holder struct {
	Name  string
	Token Secret `json:"token" xml:"token"`
}

func TestSecretReveal(t *testing.T) {
	t.Parallel()
	if got := Secret(plaintext).Reveal(); got != plaintext {
		t.Fatalf("Reveal() = %q, want %q", got, plaintext)
	}
}

func TestSecretRedactsInFmt(t *testing.T) {
	t.Parallel()
	secret := Secret(plaintext)
	for _, format := range []string{"%s", "%v", "%+v", "%#v", "%q", "%x", "%X", "%d", "%t", "%12s", "%-12v"} {
		assertRedacted(t, format, fmt.Sprintf(format, secret))
		assertRedacted(t, format+" of a pointer", fmt.Sprintf(format, &secret))
	}
	assertRedacted(t, "Sprint", fmt.Sprint(secret))
	assertRedacted(t, "Sprintln", fmt.Sprintln("token", secret))
	assertRedacted(t, "an error", fmt.Errorf("refused %v", secret).Error())
	// fmt complains about an argument the format has no verb for, and the
	// complaint must redact it too.
	extra := "token"
	assertRedacted(t, "an extra argument", fmt.Sprintf(extra, secret))
	value := holder{Name: "github", Token: secret}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%d"} {
		assertRedacted(t, format+" of a struct", fmt.Sprintf(format, value))
		assertRedacted(t, format+" of a struct pointer", fmt.Sprintf(format, &value))
	}
	assertRedacted(t, "a slice", fmt.Sprintf("%v", []Secret{secret, secret}))
	assertRedacted(t, "a map", fmt.Sprintf("%v", map[string]Secret{"token": secret}))
}

func TestSecretRedactsInSlog(t *testing.T) {
	t.Parallel()
	secret := Secret(plaintext)
	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(out *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(out, nil) },
		"text": func(out *bytes.Buffer) slog.Handler { return slog.NewTextHandler(out, nil) },
	} {
		var out bytes.Buffer
		logger := slog.New(handler(&out))
		logger.Info("connect", "token", secret)
		logger.Info("connect", slog.Any("token", &secret))
		logger.Info("connect", slog.Group("credential", "token", secret))
		logger.Info("connect", "credential", holder{Name: "github", Token: secret})
		logger.With("token", secret).Info("connect")
		if strings.Count(out.String(), redacted) != 5 {
			t.Fatalf("%s handler redacted %d of 5 values:\n%s", name, strings.Count(out.String(), redacted), out.String())
		}
		assertRedacted(t, name+" handler", out.String())
	}
}

func TestSecretRedactsInJSONAndXML(t *testing.T) {
	t.Parallel()
	secret := Secret(plaintext)
	for name, value := range map[string]any{
		"a value":   secret,
		"a pointer": &secret,
		"a struct":  holder{Name: "github", Token: secret},
		"a map":     map[string]Secret{"token": secret},
		"a slice":   []Secret{secret},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json %s: %v", name, err)
		}
		assertRedacted(t, "json "+name, string(encoded))
	}
	if encoded, _ := json.Marshal(struct {
		Token Secret `json:"token,omitempty"`
	}{}); string(encoded) != `{}` {
		t.Fatalf("an empty omitempty Secret encoded as %s", encoded)
	}

	encoded, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"credential"`
		Token   Secret   `xml:"token"`
		Key     Secret   `xml:"key,attr"`
	}{Token: secret, Key: secret})
	if err != nil {
		t.Fatalf("xml: %v", err)
	}
	if strings.Count(string(encoded), redacted) != 2 {
		t.Fatalf("xml = %s, want both values redacted", encoded)
	}
	assertRedacted(t, "xml", string(encoded))
}

// TestSecretDecodes shows the input side: a request body decodes into a
// Secret as into any string, and it is redacted from then on.
func TestSecretDecodes(t *testing.T) {
	t.Parallel()
	var body holder
	if err := json.Unmarshal([]byte(`{"token":"`+plaintext+`"}`), &body); err != nil {
		t.Fatal(err)
	}
	if body.Token.Reveal() != plaintext {
		t.Fatalf("decoded %q, want %q", body.Token.Reveal(), plaintext)
	}
	assertRedacted(t, "a decoded body", fmt.Sprintf("%+v", body))
}

func assertRedacted(t *testing.T, name, output string) {
	t.Helper()
	if strings.Contains(output, plaintext) || strings.Contains(output, fmt.Sprintf("%x", plaintext)) {
		t.Fatalf("%s leaked the secret: %s", name, output)
	}
	if !strings.Contains(output, redacted) && !strings.Contains(output, fmt.Sprintf("%x", redacted)) && !strings.Contains(output, fmt.Sprintf("%X", redacted)) {
		t.Fatalf("%s shows no redaction: %s", name, output)
	}
}
