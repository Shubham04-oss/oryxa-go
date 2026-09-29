package store

import (
	"context"
	"errors"
	"testing"
)

func TestVersionNumberTracksOnlyRequestedWorkspace(t *testing.T) {
	storage, org := newIntegrationStore(t)
	ctx := context.Background()
	if _, err := storage.VersionNumber(ctx, org); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("missing workspace version: %v", err)
	}
	for i, preset := range []string{"startup", "team"} {
		document := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\npreset: " + preset + "\n")
		if _, err := storage.Save(ctx, org, "api-actor", document, nil); err != nil {
			t.Fatal(err)
		}
		if number, err := storage.VersionNumber(ctx, org); err != nil || number != int64(i+1) {
			t.Fatalf("version metadata=%d: %v", number, err)
		}
	}
	if _, err := storage.VersionNumber(ctx, org+"-other"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("metadata escaped organization boundary: %v", err)
	}
}
