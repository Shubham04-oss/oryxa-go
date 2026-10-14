package v1

// ErrorCode is the machine-readable reason an edge call failed.
type ErrorCode string

const (
	// CodeUnauthorized: no token, or one that doesn't verify (401).
	CodeUnauthorized ErrorCode = "unauthorized"
	// CodeForbidden: a valid token that doesn't cover this room, child or
	// scope, or a replayed jti (403).
	CodeForbidden ErrorCode = "forbidden"
	// CodeNotFound: no such route, file or child (404).
	CodeNotFound ErrorCode = "not_found"
	// CodeInvalidRequest: the body or path is malformed (400).
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeTooLarge: a body or file over its cap (413).
	CodeTooLarge ErrorCode = "too_large"
	// CodeConflict: the child is busy, e.g. a browser in a member's hands (409).
	CodeConflict ErrorCode = "conflict"
	// CodeLimit: the room is at its child limit (429).
	CodeLimit ErrorCode = "limit"
	// CodeTimeout: the operation ran past its limit (504).
	CodeTimeout ErrorCode = "timeout"
	// CodeUnavailable: a container or browser couldn't be reached; retry (503).
	CodeUnavailable ErrorCode = "unavailable"
	// CodeInternal: an edge bug (500).
	CodeInternal ErrorCode = "internal"
)

// ErrorBody is every non-2xx response's body.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail says what failed. Retryable means the same call may succeed
// with a fresh token.
type ErrorDetail struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable,omitempty"`
}
