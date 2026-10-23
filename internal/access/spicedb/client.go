// Package spicedb connects internal/access to SpiceDB, which answers in
// shadow until a service flips (docs/design/permissions, step 4).
package spicedb

import (
	"errors"
	"os"

	"github.com/authzed/authzed-go/v1"
	"github.com/authzed/grpcutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config is where SpiceDB is. An empty Endpoint means SpiceDB is off, and
// every caller must work without it.
type Config struct {
	Endpoint     string // SPICEDB_ENDPOINT, host:port
	PresharedKey string // SPICEDB_PRESHARED_KEY
	// Insecure dials without TLS (SPICEDB_INSECURE=true); only for a local
	// SpiceDB, such as the compose profile on 127.0.0.1:50052.
	Insecure bool
}

// FromEnv reads Config from the environment.
func FromEnv() Config {
	return Config{
		Endpoint:     os.Getenv("SPICEDB_ENDPOINT"),
		PresharedKey: os.Getenv("SPICEDB_PRESHARED_KEY"),
		Insecure:     os.Getenv("SPICEDB_INSECURE") == "true",
	}
}

// Enabled reports whether SpiceDB is configured.
func (c Config) Enabled() bool { return c.Endpoint != "" }

// Dial opens a client. It does not reach SpiceDB until the first call.
func Dial(c Config) (*authzed.Client, error) {
	if !c.Enabled() {
		return nil, errors.New("spicedb: SPICEDB_ENDPOINT is not set")
	}
	if c.PresharedKey == "" {
		return nil, errors.New("spicedb: SPICEDB_PRESHARED_KEY is not set")
	}
	if c.Insecure {
		return authzed.NewClient(c.Endpoint,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpcutil.WithInsecureBearerToken(c.PresharedKey))
	}
	tls, err := grpcutil.WithSystemCerts(grpcutil.VerifyCA)
	if err != nil {
		return nil, err
	}
	return authzed.NewClient(c.Endpoint, tls, grpcutil.WithBearerToken(c.PresharedKey))
}
