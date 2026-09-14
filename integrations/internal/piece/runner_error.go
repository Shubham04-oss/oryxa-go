package piece

import "strings"

// RunnerError is a provider-neutral runner failure. Code and Message are
// always from the closed allowlist; upstream piece Error strings never appear
// here even if a Lambda payload tries to smuggle them in.
type RunnerError struct {
	Code    string
	Message string
}

func (e RunnerError) Error() string {
	if e.Code == "" {
		return "piece runner failed"
	}
	return "piece runner failed: " + e.Code
}

func (e RunnerError) RunnerFailure() (string, string) { return e.Code, e.Message }

func StableRunnerError(code, message string) RunnerError {
	defaults, ok := stableRunnerDefaults[code]
	if !ok {
		return RunnerError{Code: "piece_failed", Message: stableRunnerDefaults["piece_failed"]}
	}
	if allowed, ok := stableRunnerMessages[code]; ok {
		if _, ok := allowed[message]; ok {
			return RunnerError{Code: code, Message: message}
		}
	}
	if code == "invalid_request" && requiredFieldMessage(message) {
		return RunnerError{Code: code, Message: message}
	}
	return RunnerError{Code: code, Message: defaults}
}

var stableRunnerDefaults = map[string]string{
	"invalid_request":          "invalid request",
	"piece_not_allowed":        "piece and exact version are not allowed",
	"auth_type_not_allowed":    "connection auth type does not match the piece",
	"operation_not_allowed":    "action or trigger is not allowed",
	"property_not_executable":  "property has no dynamic options function",
	"capability_not_supported": "capability is not enabled",
	"file_read_failed":         "file storage read failed",
	"file_too_large":           "file exceeds the size limit",
	"file_write_failed":        "file storage write failed",
	"piece_failed":             "piece execution failed",
}

var stableRunnerMessages = map[string]map[string]struct{}{
	"invalid_request": {
		"invocationId and organizationId are required": {},
		"pieceName and pieceVersion are required":      {},
		"operation and property names are required":    {},
		"file data is invalid":                         {},
	},
	"piece_not_allowed":       {"piece and exact version are not allowed": {}},
	"auth_type_not_allowed":   {"connection auth type does not match the piece": {}},
	"operation_not_allowed":   {"action or trigger is not allowed": {}, "unsupported trigger operation": {}, "action is not allowed": {}, "trigger is not allowed": {}, "piece event hooks are not allowed": {}},
	"property_not_executable": {"property has no dynamic options function": {}},
	"capability_not_supported": {
		"flow APIs are not enabled":         {},
		"waitpoints are not enabled":        {},
		"polling schedules are not enabled": {},
		"OIDC token minting is not enabled": {},
		"file storage is not enabled":       {},
		"capability is not enabled":         {},
	},
	"file_read_failed":  {"file storage read failed": {}},
	"file_too_large":    {"file exceeds the size limit": {}},
	"file_write_failed": {"file storage write failed": {}},
	"piece_failed":      {"piece execution failed": {}},
}

func requiredFieldMessage(message string) bool {
	const suffix = " is required"
	if !strings.HasSuffix(message, suffix) {
		return false
	}
	name := strings.TrimSuffix(message, suffix)
	if name == "" || len(name) > 40 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && ((r >= '0' && r <= '9') || r == '_'):
		default:
			return false
		}
	}
	return true
}
