// Package zendesk holds clients for Zendesk's Support API, one Go package per
// API area: tickets for tickets with their comments, audits, metrics,
// attachments and tags, users, organizations, and so on. Zendesk tags its
// operations finely, so each area is generated from the operations of several
// tags in Zendesk's OpenAPI description.
//
// Zendesk serves its description at an address that always has the latest,
// https://developer.zendesk.com/zendesk/oas.yaml, so the copy the clients were
// generated from is kept here as openapi.yaml; it was last modified on
// 2026-09-14. go generate slices it into <area>/openapi.json with
// cmd/openapi-slice, each -tags entry naming an area and its tags, and runs
// oapi-codegen on each slice into <area>/client.gen.go with the shared
// ../oapi-codegen.yaml. To add an area, add an entry to -tags and an
// oapi-codegen line for it; to move to a newer description, replace
// openapi.yaml.
//
// A client is built on the server of the account, named by the subdomain an
// app gives as its domain:
//
//	client, err := tickets.NewClientWithResponses(zendesk.Server("acme"),
//		tickets.WithHTTPClient(httpClient))
//
// Lists page with a cursor, sent as page[size] and page[after]; the
// description's older page number cannot be sent in the same parameter. Most
// list responses in the description leave out the meta and links that carry
// the next cursor, so read those from the response's Body.
//
// A decoded response is not a validated one: a field Zendesk leaves out or
// sends as null decodes to its zero value, and an enum value the description
// does not list decodes as sent. A value of another type does not decode.
package zendesk

//go:generate go run ../cmd/openapi-slice -spec openapi.yaml -out . -tags "automations=Automations,brands=Brands,customobjects=Custom Objects|Custom Object Fields|Custom Object Records|Custom Object Record Attachments|Lookup Relationships,groups=Groups|Group Memberships,incremental=Incremental Export,macros=Macros,organizations=Organizations|Organization Fields|Organization Memberships|Organization Subscriptions,requests=Requests,satisfaction=Satisfaction Ratings|Satisfaction Reasons,search=Search,slas=SLA Policies|Group SLA Policies,ticketfields=Ticket Fields|Ticket Forms|Ticket Form Statuses|Custom Ticket Statuses,tickets=Tickets|Ticket Comments|Ticket Audits|Ticket Metrics|Ticket Metric Events|Ticket Import|Ticket Skips|Ticket Content Pins|Suspended Tickets|Attachments|Tags|Job Statuses,triggers=Triggers|Trigger Categories,users=Users|User Identities|User Fields,views=Views"
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package automations -o automations/client.gen.go automations/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package brands -o brands/client.gen.go brands/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package customobjects -o customobjects/client.gen.go customobjects/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package groups -o groups/client.gen.go groups/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package incremental -o incremental/client.gen.go incremental/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package macros -o macros/client.gen.go macros/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package organizations -o organizations/client.gen.go organizations/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package requests -o requests/client.gen.go requests/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package satisfaction -o satisfaction/client.gen.go satisfaction/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package search -o search/client.gen.go search/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package slas -o slas/client.gen.go slas/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package ticketfields -o ticketfields/client.gen.go ticketfields/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package tickets -o tickets/client.gen.go tickets/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package triggers -o triggers/client.gen.go triggers/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package users -o users/client.gen.go users/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package views -o views/client.gen.go views/openapi.json

// Server is the API of the Zendesk account with a subdomain, such as acme for
// acme.zendesk.com.
func Server(subdomain string) string {
	return "https://" + subdomain + ".zendesk.com"
}
