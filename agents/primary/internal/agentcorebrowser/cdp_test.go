package agentcorebrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/gorilla/websocket"
)

type scriptedWebSocket struct {
	responses []string
	writes    []map[string]any
	closed    bool
}

func (s *scriptedWebSocket) WriteJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return err
	}
	s.writes = append(s.writes, decoded)
	return nil
}

func (s *scriptedWebSocket) ReadJSON(value any) error {
	if len(s.responses) == 0 {
		return errors.New("no scripted response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return json.Unmarshal([]byte(response), value)
}

func (*scriptedWebSocket) SetWriteDeadline(time.Time) error { return nil }
func (*scriptedWebSocket) SetReadDeadline(time.Time) error  { return nil }
func (s *scriptedWebSocket) Close() error {
	s.closed = true
	return nil
}

func TestCDPClientInitializesPageAndReadsSemanticSnapshot(t *testing.T) {
	longText := strings.Repeat("x", maxSnapshotTextBytes+10)
	snapshot, err := json.Marshal(map[string]any{
		"title": "Example",
		"url":   "https://example.test/",
		"text":  longText,
		"elements": []map[string]any{{
			"selector": "#search",
			"tag":      "input",
			"name":     "Search",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := &scriptedWebSocket{responses: []string{
		`{"id":1,"result":{"targetInfos":[{"targetId":"page-1","type":"page","url":"about:blank"}]}}`,
		`{"id":2,"result":{"sessionId":"cdp-session"}}`,
		`{"id":3,"result":{}}`,
		`{"id":4,"result":{}}`,
		`{"method":"Runtime.consoleAPICalled","params":{}}`,
		`{"id":5,"result":{"result":{"type":"object","value":` + string(snapshot) + `}}}`,
	}}
	client := &cdpClient{connection: connection, commandTimeout: time.Second}
	if err := client.initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := client.invoke(t.Context(), Request{Action: Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if client.sessionID != "cdp-session" || len(connection.writes) != 5 {
		t.Fatalf("session = %q, writes = %#v", client.sessionID, connection.writes)
	}
	if connection.writes[2]["sessionId"] != "cdp-session" || connection.writes[4]["method"] != "Runtime.evaluate" {
		t.Fatalf("writes = %#v", connection.writes)
	}
	if result.Page.Title != "Example" || len(result.Page.Elements) != 1 || result.Page.Elements[0].Selector != "#search" {
		t.Fatalf("snapshot = %+v", result.Page)
	}
	if !result.Page.TextTruncated || len(result.Page.Text) != maxSnapshotTextBytes {
		t.Fatalf("snapshot text length = %d, truncated = %v", len(result.Page.Text), result.Page.TextTruncated)
	}
}

func TestCDPClientDecodesScreenshotAndSurfacesProtocolErrors(t *testing.T) {
	connection := &scriptedWebSocket{responses: []string{
		`{"id":1,"error":{"code":-32000,"message":"page closed"}}`,
	}}
	client := &cdpClient{connection: connection, commandTimeout: time.Second, sessionID: "session"}
	if _, err := client.invoke(t.Context(), Request{Action: Screenshot}); err == nil || !strings.Contains(err.Error(), "page closed") {
		t.Fatalf("screenshot error = %v", err)
	}

	connection = &scriptedWebSocket{responses: []string{
		`{"id":1,"result":{"data":"cG5n"}}`,
	}}
	client = &cdpClient{connection: connection, commandTimeout: time.Second, sessionID: "session"}
	result, err := client.invoke(t.Context(), Request{Action: Screenshot})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.ScreenshotPNG) != "png" {
		t.Fatalf("screenshot = %q", result.ScreenshotPNG)
	}
}

func TestNormalizeSnapshotBoundsElementsAndUTF8(t *testing.T) {
	page := PageSnapshot{
		Text:     strings.Repeat("🙂", maxSnapshotTextBytes),
		Elements: make([]Element, maxSnapshotElements+1),
	}
	normalizeSnapshot(&page)
	if !page.TextTruncated || !page.ElementsTruncated || len(page.Elements) != maxSnapshotElements {
		t.Fatalf("normalized snapshot = text:%d elements:%d text-truncated:%v elements-truncated:%v", len(page.Text), len(page.Elements), page.TextTruncated, page.ElementsTruncated)
	}
	if !json.Valid([]byte(`"` + page.Text + `"`)) {
		t.Fatal("truncated snapshot text is not valid UTF-8 JSON")
	}
}

func TestSigV4ConnectorSignsAndInitializesAutomationWebSocket(t *testing.T) {
	headers := make(chan http.Header, 1)
	serverErrors := make(chan error, 1)
	var attempts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(writer, "Required resources not found for session test-session", http.StatusNotFound)
			return
		}
		connection, err := (&websocket.Upgrader{}).Upgrade(writer, request, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer connection.Close()
		headers <- request.Header.Clone()
		for range 4 {
			var command map[string]any
			if err := connection.ReadJSON(&command); err != nil {
				serverErrors <- err
				return
			}
			result := map[string]any{}
			switch command["method"] {
			case "Target.getTargets":
				result["targetInfos"] = []map[string]any{{"targetId": "page-1", "type": "page", "url": "about:blank"}}
			case "Target.attachToTarget":
				result["sessionId"] = "cdp-session"
			}
			if err := connection.WriteJSON(map[string]any{"id": command["id"], "result": result}); err != nil {
				serverErrors <- err
				return
			}
		}
		serverErrors <- nil
	}))
	defer server.Close()

	connector, err := newSigV4Connector(aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET", SessionToken: "TOKEN"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.now = func() time.Time { return time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC) }
	connector.retryDelay = time.Millisecond
	connector.dialer.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	endpoint := strings.Replace(server.URL, "https://", "wss://", 1) + "/browser-streams/browser/sessions/session/automation"
	client, err := connector.connect(t.Context(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("automation test server did not finish")
	}
	requestHeaders := <-headers
	if !strings.Contains(requestHeaders.Get("Authorization"), "Credential=AKID/") || requestHeaders.Get("X-Amz-Security-Token") != "TOKEN" || requestHeaders.Get("X-Amz-Date") == "" {
		t.Fatalf("signed headers = %#v", requestHeaders)
	}
	if attempts.Load() != 2 {
		t.Fatalf("automation connection attempts = %d, want 2", attempts.Load())
	}
}
