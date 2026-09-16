// Package intercom is a client for Intercom's REST API, generated from
// Intercom's official OpenAPI description of API version 2.16,
// github.com/intercom/Intercom-OpenAPI (MIT licensed), at the pinned commit.
//
// Intercom tags operations finely and often twice, so the client is one
// package rather than one per area. go generate slices the description with
// cmd/openapi-slice into openapi.json, keeping every tag but two the
// description cannot generate from: Reporting Data Export, whose download path
// names a parameter it never declares, and Data Events, whose filter parameter
// is a choice of objects. To move to a newer description, change the commit in
// the URL and APIVersion.
//
// A client is built on Server, which routes each request to its workspace's
// region:
//
//	client, err := intercom.NewClientWithResponses(intercom.Server,
//		intercom.WithHTTPClient(httpClient),
//		intercom.WithRequestEditorFn(intercom.Versioned))
//
// A decoded response is not a validated one: a field Intercom leaves out or
// sends as null decodes to its zero value, and an enum value the description
// does not list decodes as sent. A value of another type does not decode.
package intercom

import (
	"context"
	"net/http"
)

//go:generate go run ../cmd/openapi-slice -spec https://raw.githubusercontent.com/intercom/Intercom-OpenAPI/8a997450f03917f79ea2c5f177d6b0348ec2cf19/descriptions/2.16/api.intercom.io.yaml -out .. -tags "intercom=Admins|AI Content|Articles|Audiences|Away Status Reasons|Banners|Brands|Calls|Companies|Contacts|Content|Content Snippets|Conversations|Conversations Attributes|Custom Object Instances|Data Attributes|Data Connectors|Data Export|Emails|Fin Agent|Help Center|Internal Articles|IP Allowlist|Jobs|Macros|Messages|News|Notes|Office Hours|Segments|Subscription Types|Switch|Tags|Teams|Ticket States|Ticket Type Attributes|Ticket Types|Tickets|Visitors|WhatsApp|Workflows"
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package intercom -o client.gen.go openapi.json

const (
	// Server is Intercom's REST API. It routes a request to the region of the
	// workspace its token belongs to.
	Server = "https://api.intercom.io"
	// APIVersion is the Intercom-Version the client was generated from.
	APIVersion = "2.16"
)

// Versioned sends the Intercom-Version the client was generated from, unless
// the call already names one. Without it, Intercom answers in the version set
// on the app.
func Versioned(_ context.Context, request *http.Request) error {
	if request.Header.Get("Intercom-Version") == "" {
		request.Header.Set("Intercom-Version", APIVersion)
	}
	return nil
}
