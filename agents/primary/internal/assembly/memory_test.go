package assembly

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"cloud.google.com/go/auth"
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

type staticToken struct{}

func (staticToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: "test"}, nil
}

func memoryOptions(t *testing.T, values map[string]string) (serviceconfig.Config, Options) {
	t.Helper()
	settings, err := serviceconfig.FromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	return settings, Options{
		AWS:    aws.Config{Region: "ap-south-1"},
		Google: auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: staticToken{}}),
	}
}

// Production today sets the engine and not the backend: that has to build
// Memory Bank alone, exactly as before the choice existed, and no engine has
// to stay no memory at all.
func TestMemoryIsMemoryBankUnlessSaidOtherwise(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	settings, options := memoryOptions(t, map[string]string{})
	krude := &Krude{}
	backend, err := krude.memoryBackend(context.Background(), settings, options, logger)
	if err != nil || backend != nil || len(krude.closers) != 0 {
		t.Fatalf("no engine: backend %T, %d closers, %v; want no memory", backend, len(krude.closers), err)
	}
	if !strings.Contains(logged.String(), "Krude remembers nothing between threads") {
		t.Fatalf("logged %q", logged.String())
	}

	for _, values := range []map[string]string{
		{"GOOGLE_CLOUD_PROJECT": "oryxa", "AGENTS_PRIMARY_MEMORY_LOCATION": "us-central1", "AGENTS_PRIMARY_MEMORY_AGENT_ENGINE": "123"},
		{"GOOGLE_CLOUD_PROJECT": "oryxa", "AGENTS_PRIMARY_MEMORY_LOCATION": "us-central1", "AGENTS_PRIMARY_MEMORY_AGENT_ENGINE": "123", "AGENTS_PRIMARY_MEMORY_BACKEND": "vertex", "AGENTS_PRIMARY_MEMORY_ID": "ignored"},
	} {
		settings, options := memoryOptions(t, values)
		krude := &Krude{}
		backend, err := krude.memoryBackend(context.Background(), settings, options, logger)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := backend.(*adkmemory.MemoryBank); !ok || len(krude.closers) != 1 {
			t.Fatalf("%v: backend %T with %d closers, want Memory Bank alone", values, backend, len(krude.closers))
		}
		if err := krude.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMemoryOnAgentCoreAndInEitherShadow(t *testing.T) {
	base := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "oryxa", "AGENTS_PRIMARY_MEMORY_LOCATION": "us-central1", "AGENTS_PRIMARY_MEMORY_AGENT_ENGINE": "123",
		"AGENTS_PRIMARY_MEMORY_ID": "krude_memory-abc",
	}
	for backend, check := range map[string]func(adkmemory.Backend) bool{
		serviceconfig.MemoryBackendAgentCore: func(built adkmemory.Backend) bool {
			_, ok := built.(*adkmemory.AgentCoreMemory)
			return ok
		},
		serviceconfig.MemoryBackendVertexShadowAgentCore: isShadow,
		serviceconfig.MemoryBackendAgentCoreShadowVertex: isShadow,
	} {
		values := map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": backend}
		for key, value := range base {
			values[key] = value
		}
		settings, options := memoryOptions(t, values)
		krude := &Krude{}
		built, err := krude.memoryBackend(context.Background(), settings, options, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		if !check(built) {
			t.Errorf("%s: built %T", backend, built)
		}
		if err := krude.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// isShadow reports whether built is neither backend alone.
func isShadow(built adkmemory.Backend) bool {
	switch built.(type) {
	case nil, *adkmemory.MemoryBank, *adkmemory.AgentCoreMemory:
		return false
	}
	return true
}
