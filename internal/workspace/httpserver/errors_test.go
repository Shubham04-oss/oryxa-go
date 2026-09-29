package httpserver

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func TestApplyErrorsPreserveDownstreamStatusAndSafeReasons(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
		detail string
	}{
		{&runtime.StatusError{Status: 400, Detail: "invitation email is outside the allowed domains"}, 400, "upstream_rejected", "outside the allowed domains"},
		{&runtime.StatusError{Status: 403, Detail: "only owners may change roles"}, 403, "upstream_rejected", "only owners"},
		{&runtime.StatusError{Status: 409, Detail: "team already exists"}, 409, "upstream_rejected", "already exists"},
		{runtime.ErrApprovalChanged, 409, "stale_plan", "review"},
		{store.ErrApplyBusy, 409, "apply_lock", "in progress"},
		{errors.New("database password=private"), 502, "upstream_unavailable", "could not complete"},
	} {
		writer := httptest.NewRecorder()
		writeApplyError(writer, test.err)
		var body map[string]string
		if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if writer.Code != test.status || body["code"] != test.code || !strings.Contains(body["error"], test.detail) || strings.Contains(writer.Body.String(), "private") {
			t.Fatalf("unsafe or incorrect failure: status=%d body=%s", writer.Code, writer.Body.String())
		}
	}
}
