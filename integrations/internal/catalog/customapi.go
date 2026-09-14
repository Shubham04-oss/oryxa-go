package catalog

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
)

var customAPIMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}

func validateCustomAPICall(hosts ...string) func(json.RawMessage) error {
	allowed := append([]string(nil), hosts...)
	return func(raw json.RawMessage) error {
		var props struct {
			URL      string          `json:"url"`
			Method   string          `json:"method"`
			Headers  map[string]any  `json:"headers"`
			Query    map[string]any  `json:"query"`
			BodyType string          `json:"body_type"`
			Body     json.RawMessage `json:"body"`
		}
		if err := decodeStrict(raw, &props); err != nil || !customAPIURL(props.URL, allowed...) ||
			!oneOf(strings.ToUpper(props.Method), customAPIMethods...) ||
			!customAPIHeaders(props.Headers) || !customAPIObject(props.Query) ||
			(props.BodyType != "" && !oneOf(props.BodyType, "none", "json", "form_data", "raw")) ||
			(len(props.Body) > 64*1024) {
			return errors.New("invalid custom API call properties")
		}
		if (props.BodyType == "" || props.BodyType == "none") && len(props.Body) > 0 && string(props.Body) != "null" {
			return errors.New("invalid custom API call properties")
		}
		return nil
	}
}

func prepareCustomAPICall(raw json.RawMessage) (json.RawMessage, error) {
	var props struct {
		URL      string          `json:"url"`
		Method   string          `json:"method"`
		Headers  map[string]any  `json:"headers"`
		Query    map[string]any  `json:"query"`
		BodyType string          `json:"body_type"`
		Body     json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	if props.Headers == nil {
		props.Headers = map[string]any{}
	}
	if props.Query == nil {
		props.Query = map[string]any{}
	}
	bodyType := props.BodyType
	if bodyType == "" {
		bodyType = "none"
	}
	prepared := map[string]any{
		"url":         map[string]any{"url": props.URL},
		"method":      strings.ToUpper(props.Method),
		"headers":     props.Headers,
		"queryParams": props.Query,
		"body_type":   bodyType,
	}
	if bodyType != "none" && len(props.Body) > 0 && string(props.Body) != "null" {
		var body any
		if json.Unmarshal(props.Body, &body) != nil {
			return nil, errors.New("invalid custom API call body")
		}
		prepared["body"] = map[string]any{"data": body}
	}
	return json.Marshal(prepared)
}

func customAPIURL(value string, hosts ...string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return !strings.Contains(value, "..")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range hosts {
		if host == strings.ToLower(allowed) {
			return true
		}
	}
	return false
}

func customAPIHeaders(headers map[string]any) bool {
	if headers == nil {
		return true
	}
	if len(headers) > 32 {
		return false
	}
	for key, value := range headers {
		if key == "" || len(key) > 128 || strings.EqualFold(key, "authorization") ||
			strings.EqualFold(key, "proxy-authorization") || strings.ContainsAny(key, "\r\n") {
			return false
		}
		text, ok := value.(string)
		if !ok || len(text) > 4096 || strings.ContainsAny(text, "\r\n") {
			return false
		}
	}
	return true
}

func customAPIObject(values map[string]any) bool {
	if values == nil {
		return true
	}
	if len(values) > 32 {
		return false
	}
	for key, value := range values {
		if key == "" || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
			return false
		}
		switch typed := value.(type) {
		case string:
			if len(typed) > 2048 || strings.ContainsAny(typed, "\r\n") {
				return false
			}
		case float64, bool, nil:
		default:
			return false
		}
	}
	return true
}

func publicHTTPSURL(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".corp") ||
		host == "metadata.google.internal" || strings.Contains(host, "..") {
		return false
	}
	return net.ParseIP(host) == nil
}
