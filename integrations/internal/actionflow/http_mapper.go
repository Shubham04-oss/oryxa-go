package actionflow

import (
	"encoding/json"
	"strings"
)

func mapProviderHTTPCall(request MapRequest) (MappedResult, error) {
	status, body := splitHTTPEnvelope(request.Output)
	if jsonHasSecretKeys(body) {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if len(body) == 0 {
		body = json.RawMessage("null")
	}
	result, err := json.Marshal(struct {
		Status int             `json:"status,omitempty"`
		Body   json.RawMessage `json:"body"`
	}{Status: status, Body: body})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func unwrapHTTPBody(raw json.RawMessage) json.RawMessage {
	_, body := splitHTTPEnvelope(raw)
	return body
}

func splitHTTPEnvelope(raw json.RawMessage) (int, json.RawMessage) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return 0, raw
	}
	statusRaw, hasStatus := fields["status"]
	body, hasBody := fields["body"]
	if !hasStatus || !hasBody || len(body) == 0 {
		return 0, raw
	}
	var status int
	if json.Unmarshal(statusRaw, &status) != nil {
		return 0, raw
	}
	trimmed := bytesTrim(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[' && trimmed[0] != '"' && trimmed[0] != 'n' &&
		trimmed[0] != 't' && trimmed[0] != 'f' && (trimmed[0] < '0' || trimmed[0] > '9') && trimmed[0] != '-') {
		return 0, raw
	}
	return status, body
}

func jsonHasSecretKeys(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return walkSecretKeys(value)
}

func walkSecretKeys(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "access_token", "refresh_token", "authorization", "client_secret", "id_token", "proxy-authorization":
				return true
			}
			if walkSecretKeys(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if walkSecretKeys(child) {
				return true
			}
		}
	}
	return false
}
