// Package connectorv1 is the Go code for the connector contract, the gRPC
// service the connector host calls and every connector serves.
//
// Source: oryxa-platform, proto/oryxa/connector/v1/connector.proto, copied
// unchanged to integrations/proto/oryxa/connector/v1/, and its generated code
// from connectors/gosdk/pb (only the package name differs). Changes to the
// contract are additive; a breaking change is a new package.
//
// To regenerate, run buf with protoc-gen-go v1.36.12 and protoc-gen-go-grpc
// v1.6.2 over integrations/proto, with the Go package set to this one.
package connectorv1
