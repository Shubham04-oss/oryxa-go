package runtime

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStatusErrorShowsOnlyWorkspaceValidationDetails(t *testing.T) {
	for _, test := range []struct {
		name, path, body, contentType, want string
		status                              int
	}{
		{"workspace conflict", "/v1/organizations/org1/workspace:plan", `{"error":"external user ID would change its Oryxa binding"}`, "application/json", "external user ID would change its Oryxa binding", http.StatusBadRequest},
		{"other API", "/v1/organizations/org1/apps/connections", `{"error":"provider secret"}`, "application/json", "", http.StatusBadRequest},
		{"server failure", "/v1/organizations/org1/workspace", `{"error":"database detail"}`, "application/json", "", http.StatusInternalServerError},
		{"non JSON", "/v1/organizations/org1/workspace", `{"error":"not trusted"}`, "text/plain", "", http.StatusBadRequest},
		{"multiline", "/v1/organizations/org1/workspace", `{"error":"first\nsecond"}`, "application/json", "", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": []string{test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body))}
			message := statusError(response, http.MethodPost, test.path).Error()
			want := fmt.Sprintf("POST %s: API returned %d", test.path, test.status)
			if test.want != "" {
				want += ": " + test.want
			}
			if message != want {
				t.Fatalf("error = %q, want %q", message, want)
			}
		})
	}
}
