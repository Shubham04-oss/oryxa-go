package piece

import "testing"

func TestStableRunnerErrorKeepsAllowlistedMessagesAndDropsUpstreamText(t *testing.T) {
	t.Parallel()
	got := StableRunnerError("piece_not_allowed", "sensitive provider text")
	if got.Code != "piece_not_allowed" || got.Message != "piece and exact version are not allowed" {
		t.Fatalf("got = %+v", got)
	}
	if got.Error() != "piece runner failed: piece_not_allowed" {
		t.Fatalf("error = %q", got.Error())
	}

	exact := StableRunnerError("file_too_large", "file exceeds the size limit")
	if exact.Code != "file_too_large" || exact.Message != "file exceeds the size limit" {
		t.Fatalf("exact = %+v", exact)
	}

	field := StableRunnerError("invalid_request", "webhookSecret is required")
	if field.Code != "invalid_request" || field.Message != "webhookSecret is required" {
		t.Fatalf("field = %+v", field)
	}

	unknown := StableRunnerError("not_a_real_code", "anything")
	if unknown.Code != "piece_failed" || unknown.Message != "piece execution failed" {
		t.Fatalf("unknown = %+v", unknown)
	}
}
