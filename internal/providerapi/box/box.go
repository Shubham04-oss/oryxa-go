// Package box holds clients for Box's API, one Go package per API area: files
// for files with their versions, trash and shared links, folders for folders,
// uploads for sending file content, and so on. Box tags its operations finely,
// so each area is generated from the operations of several tags in Box's
// OpenAPI description (github.com/box/box-openapi, Apache 2.0 licensed).
//
// go generate fetches the pinned description, slices it into
// <area>/openapi.json with cmd/openapi-slice, each -tags entry naming an area
// and its tags, and runs oapi-codegen on each slice into <area>/client.gen.go
// with the shared ../oapi-codegen.yaml. To add an area, add an entry to -tags
// and an oapi-codegen line for it; to move to a newer description, change the
// commit in the URL. The description is of Box's API before Box versioned it;
// the APIs added in versions 2025.0 and later are described in separate files
// and are not generated here.
//
// Every client but uploads is built on Server:
//
//	client, err := files.NewClientWithResponses(box.Server,
//		files.WithHTTPClient(httpClient))
//
// uploads is built on UploadServer, where Box takes file content.
//
// A typed request body leaves out a field it holds no value for, so it cannot
// send the null Box reads as "clear this", as in removing a shared link,
// unlocking a file or restoring a file version. Send such a body as JSON with
// the operation's WithBody variant:
//
//	response, err := client.PutFilesIDRemoveSharedLinkWithBodyWithResponse(ctx, fileID,
//		&files.PutFilesIDRemoveSharedLinkParams{Fields: "shared_link"},
//		"application/json", strings.NewReader(`{"shared_link":null}`))
//
// A decoded response is not a validated one: a field Box leaves out or sends as
// null decodes to its zero value, and an enum value the description does not
// list decodes as sent, which the enum's Valid method reports. A value of
// another type does not decode. The items of a folder decode as a union, read
// with AsFileFull, AsFolderMini or AsWebLink: each item decodes as any of them,
// so check its Type with Valid.
package box

//go:generate go run ../cmd/openapi-slice -spec https://raw.githubusercontent.com/box/box-openapi/933ec4c5d9f4dbdaaa823efa3a083cf177aecbde/openapi/openapi.json -out . -tags "ai=AI|AI Studio,collaborations=Collaborations|Collaborations (List),collections=Collections|Recent items,comments=Comments,events=Events,files=Files|File versions|Trashed files|Downloads|Shared links (Files),folders=Folders|Trashed folders|Trashed items|Shared links (Folders)|Folder Locks,groups=Groups|Group memberships,metadata=Metadata instances (Files)|Metadata instances (Folders)|Metadata templates|Metadata cascade policies,search=Search,sign=Box Sign requests|Box Sign templates,tasks=Tasks|Task assignments,uploads=Uploads|Uploads (Chunked),users=Users,webhooks=Webhooks,weblinks=Web links|Trashed web links|Shared links (Web Links)"
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package ai -o ai/client.gen.go ai/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package collaborations -o collaborations/client.gen.go collaborations/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package collections -o collections/client.gen.go collections/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package comments -o comments/client.gen.go comments/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package events -o events/client.gen.go events/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package files -o files/client.gen.go files/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package folders -o folders/client.gen.go folders/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package groups -o groups/client.gen.go groups/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package metadata -o metadata/client.gen.go metadata/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package search -o search/client.gen.go search/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package sign -o sign/client.gen.go sign/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package tasks -o tasks/client.gen.go tasks/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package uploads -o uploads/client.gen.go uploads/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package users -o users/client.gen.go users/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package webhooks -o webhooks/client.gen.go webhooks/openapi.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config ../oapi-codegen.yaml -package weblinks -o weblinks/client.gen.go weblinks/openapi.json

const (
	// Server is Box's API.
	Server = "https://api.box.com/2.0"
	// UploadServer is where Box takes file content, for the uploads client.
	UploadServer = "https://upload.box.com/api/2.0"
)
