package notion

import (
	"context"
	"net/http"
)

// The client is generated from openapi.json: Notion's OpenAPI description of
// API version 2025-09-03, MIT licensed. Notion publishes no stable address for
// it, so the file is kept here. To move to a newer version, replace the file,
// change APIVersion, and run go generate.

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package notion -o client.gen.go openapi.json

const (
	// Server is Notion's REST API.
	Server = "https://api.notion.com"
	// APIVersion is the Notion-Version the client was generated from.
	APIVersion = "2025-09-03"
)

// Versioned sends the Notion-Version the client was generated from, unless the
// call already names one. Pass it to a client with WithRequestEditorFn:
//
//	client, err := notion.NewClientWithResponses(notion.Server,
//		notion.WithHTTPClient(httpClient),
//		notion.WithRequestEditorFn(notion.Versioned))
func Versioned(_ context.Context, request *http.Request) error {
	if request.Header.Get("Notion-Version") == "" {
		request.Header.Set("Notion-Version", APIVersion)
	}
	return nil
}
