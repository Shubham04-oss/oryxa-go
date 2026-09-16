package figma

// The client is generated from Figma's official OpenAPI description,
// github.com/figma/rest-api-spec (MIT licensed), at the pinned commit. To move
// to a newer description, change the commit and run go generate.
//
// OAuth access tokens are sent as Authorization bearer tokens; personal access
// tokens go in X-Figma-Token. A token connections issues says which with its
// placement.

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml -package figma -o client.gen.go https://raw.githubusercontent.com/figma/rest-api-spec/04fbbc719706e986fc79f3050d3e068e118275d9/openapi/openapi.yaml

// Server is Figma's REST API.
const Server = "https://api.figma.com"
