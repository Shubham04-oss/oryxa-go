package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestWorkspaceReadsTolerateFutureStoredFields(t *testing.T) {
	document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata:\n  organization: acme\npreset: team\nfuturePolicy:\n  enabled: true\n"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing authorization")
		}
		fmt.Fprint(w, document)
	}))
	defer api.Close()
	client, err := NewClient(api.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{0, 1} {
		var got spec.Document
		if version == 0 {
			got, err = client.WorkspacePull(context.Background(), "org")
		} else {
			got, err = client.WorkspaceVersion(context.Background(), "org", version)
		}
		if err != nil || got.Metadata.Organization != "acme" {
			t.Fatalf("read version %d = %+v, %v", version, got, err)
		}
	}
	if _, err := spec.Parse([]byte(document)); err == nil || !strings.Contains(err.Error(), "futurePolicy") {
		t.Fatalf("submitted future fields must remain strict: %v", err)
	}
	document = strings.Replace(document, "preset: team", "preset: invalid", 1)
	if historical, err := client.WorkspaceVersion(context.Background(), "org", 1); err != nil || historical.Preset != "invalid" {
		t.Fatalf("historical setting unavailable for repair: %+v, %v", historical, err)
	}
}
