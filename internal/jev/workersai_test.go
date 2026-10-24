package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cloudflare's Clef serves the System One API at a Workers AI run endpoint:
// the base URL is that endpoint, the model defaults to its name, and the
// answer comes back inside Cloudflare's {"result": …} envelope.
func TestAskThroughWorkersAI(t *testing.T) {
	const runPath = "/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef-flash"
	var gotPath, gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Model string `json:"model"`
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		gotModel = body.Model
		_, _ = io.WriteString(w, `{"result":{"model":"clef-flash","answers":{"q":{"type":"noul","noul":0.9}},"usage":{"input_tokens":12}},"success":true,"errors":[],"messages":[]}`)
	}))
	defer server.Close()

	client, err := New(Config{APIKey: "cf-token", BaseURL: server.URL + runPath})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Ask(context.Background(), Request{State: "s", Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "yes?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != runPath || gotModel != "clef-flash" {
		t.Fatalf("asked %s with model %q, want %s with clef-flash", gotPath, gotModel, runPath)
	}
	if result.Model != "clef-flash" || result.Answers["q"].Noul != 0.9 || result.InputTokens != 12 {
		t.Fatalf("result = %+v", result)
	}
}

func TestWorkersAIRefusalIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{},"success":false,"errors":[{"message":"Request body failed validation"}]}`)
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "k", BaseURL: server.URL + "/ai/run/@cf/cloudflare/clef"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Ask(context.Background(), Request{State: "s", Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "yes?"}}})
	if err == nil || !strings.Contains(err.Error(), "failed validation") {
		t.Fatalf("err = %v, want Cloudflare's message", err)
	}
}

func TestEndpointAndModelFor(t *testing.T) {
	for base, want := range map[string][2]string{
		"https://api.typesafe.ai": {"https://api.typesafe.ai/v1/systemone", DefaultModel},
		"https://api.cloudflare.com/client/v4/accounts/a/ai/run/@cf/cloudflare/clef-flash/": {
			"https://api.cloudflare.com/client/v4/accounts/a/ai/run/@cf/cloudflare/clef-flash", "clef-flash"},
	} {
		if got := Endpoint(base); got != want[0] {
			t.Errorf("Endpoint(%s) = %s", base, got)
		}
		if got := ModelFor(base); got != want[1] {
			t.Errorf("ModelFor(%s) = %s", base, got)
		}
	}
}
