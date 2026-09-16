// Package github holds clients for GitHub's REST API, one Go package per API
// area: actions for GitHub Actions, repos for repositories, orgs for
// organizations, and so on. Each is generated from the part of GitHub's
// OpenAPI description tagged with that area.
//
// go generate fetches the pinned description, slices it by tag into
// <area>/openapi.json with cmd/openapi-slice, and runs oapi-codegen on each
// slice into <area>/client.gen.go with the shared ../oapi-codegen.yaml. To add
// an area, add its tag to -tags and an oapi-codegen line for it; to move to a
// newer description, change the commit in the URL.
//
// Every client is built the same way:
//
//	client, err := repos.NewClientWithResponses(github.Server,
//		repos.WithHTTPClient(httpClient),
//		repos.WithRequestEditorFn(github.Versioned))
//
// A decoded response is not a validated one: a field GitHub leaves out or
// sends as null decodes to its zero value, and an enum value the description
// does not list decodes as sent. A value of another type does not decode.
package github

import (
	"context"
	"net/http"
)

//go:generate go run ../cmd/openapi-slice -spec https://raw.githubusercontent.com/github/rest-api-description/5c01d68e9b6f06803a2c377855b310f7f2279def/descriptions/api.github.com/api.github.com.2022-11-28.json -tags actions,activity,apps,checks,gists,git,issues,orgs,pulls,repos,search,users -out .
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package actions -o actions/client.gen.go actions/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package activity -o activity/client.gen.go activity/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package apps -o apps/client.gen.go apps/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package checks -o checks/client.gen.go checks/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package gists -o gists/client.gen.go gists/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package git -o git/client.gen.go git/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package issues -o issues/client.gen.go issues/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package orgs -o orgs/client.gen.go orgs/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package pulls -o pulls/client.gen.go pulls/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package repos -o repos/client.gen.go repos/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package search -o search/client.gen.go search/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package users -o users/client.gen.go users/openapi.json

const (
	// Server is the REST API of github.com.
	Server = "https://api.github.com"
	// APIVersion is the REST API version of the description the clients were
	// generated from.
	APIVersion = "2022-11-28"
)

// Versioned asks GitHub for JSON in the API version the clients were generated
// from. Pass it to a client with WithRequestEditorFn. Editors passed to a
// single call run after it, so a call can still ask for another media type.
func Versioned(_ context.Context, request *http.Request) error {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	return nil
}
