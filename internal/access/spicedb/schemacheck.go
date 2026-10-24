package spicedb

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/authzed/authzed-go/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

// Schema is schema.zed, the schema this binary's checks and writes expect.
//
//go:embed schema.zed
var Schema string

// Services are the services ACCESS_ENGINE_<SERVICE> can switch to SpiceDB.
var Services = []string{"files", "rag", "knowledge", "apps", "teams"}

// schemaCheckTimeout bounds the check at boot, so a SpiceDB that doesn't
// answer delays the start by no more than this.
const schemaCheckTimeout = 10 * time.Second

// SchemaDiff is how SpiceDB's schema differs from Schema, one line per
// difference; none means they match. SpiceDB compares them itself
// (DiffSchema), by definition, relation, permission and caveat; layout never
// counts, and the doc comments it reports changed are left out.
func SchemaDiff(ctx context.Context, client *authzed.Client) ([]string, error) {
	response, err := client.DiffSchema(ctx, &v1.DiffSchemaRequest{
		Consistency:      &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}},
		ComparisonSchema: Schema,
	})
	if status.Code(err) == codes.NotFound {
		return []string{"SpiceDB has no schema"}, nil
	}
	if err != nil {
		return nil, err
	}
	var diffs []string
	for _, diff := range response.GetDiffs() {
		message := diff.ProtoReflect()
		if kind := message.WhichOneof(message.Descriptor().Oneofs().ByName("diff")); kind != nil && strings.HasSuffix(string(kind.Name()), "_doc_comment_changed") {
			continue
		}
		diffs = append(diffs, prototext.MarshalOptions{}.Format(diff))
	}
	return diffs, nil
}

// WriteSchema writes Schema to SpiceDB.
func WriteSchema(ctx context.Context, client *authzed.Client) error {
	_, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: Schema})
	return err
}

// CheckSchema compares SpiceDB's schema with Schema at boot. A mismatch
// while any service decides from SpiceDB is an error the process must not
// start with: missing relations refuse, and refusals look like an outage. With
// only the shadow and projector on, a mismatch produces disagreements and
// failed writes, so it is logged and the process starts. A SpiceDB that
// can't be reached is logged too: switched services already fail closed.
func CheckSchema(ctx context.Context, config Config, getenv func(string) string, logger *slog.Logger) error {
	if !config.Enabled() {
		return nil
	}
	var switched []string
	for _, service := range Services {
		// A malformed value is refused when the service mounts.
		if engine, _ := access.Engine(getenv, service); engine == access.EngineSpiceDB {
			switched = append(switched, service)
		}
	}
	client, err := Dial(config)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, schemaCheckTimeout)
	defer cancel()
	diffs, err := SchemaDiff(ctx, client)
	switch {
	case err != nil:
		logger.Error("spicedb schema not checked: SpiceDB did not answer", "error", err, "switched", switched)
	case len(diffs) == 0:
		logger.Info("spicedb schema matches this binary's", "switched", switched)
	case len(switched) > 0:
		return fmt.Errorf("SpiceDB's schema differs from this binary's while %s decide from it; write it with `oryxa spicedb-schema-write` or set ACCESS_ENGINE_* back to code. Differences:\n%s",
			strings.Join(switched, ", "), strings.Join(diffs, "\n"))
	default:
		logger.Error("SpiceDB's schema differs from this binary's; the shadow will disagree until `oryxa spicedb-schema-write` runs",
			"differences", diffs)
	}
	return nil
}
