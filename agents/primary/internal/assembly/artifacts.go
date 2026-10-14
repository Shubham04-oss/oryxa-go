package assembly

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"google.golang.org/adk/v2/artifact"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/s3artifact"
)

// artifactStore is where artifacts are kept: R2 when the settings name it
// (live-layer A7), still reading the S3 bucket for what was saved there
// before; S3 alone; or, with neither, memory, which goes with the process.
func artifactStore(ctx context.Context, options Options, logger *slog.Logger) (artifact.Service, error) {
	settings := options.Settings
	var legacy artifact.Service
	if settings.ArtifactBucket != "" {
		artifactConfig := options.AWS
		if settings.S3Endpoint != "" {
			// The emulator takes any signature, so the client carries dummy
			// credentials instead of asking for real ones.
			artifactConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
		}
		stored, err := s3artifact.NewFromConfig(artifactConfig, settings.S3Endpoint, settings.ArtifactBucket)
		if err != nil {
			return nil, err
		}
		legacy = stored
	}
	if settings.ArtifactR2.Endpoint == "" {
		if legacy == nil {
			logger.InfoContext(ctx, "artifacts are held in memory and go with this process; set AGENTS_PRIMARY_ARTIFACT_BUCKET to keep them")
			return artifact.InMemoryService(), nil
		}
		logger.InfoContext(ctx, "artifacts stored in S3", "bucket", settings.ArtifactBucket)
		return legacy, nil
	}

	r2, err := s3artifact.NewR2(options.AWS, s3artifact.R2{
		Endpoint:        settings.ArtifactR2.Endpoint,
		AccessKeyID:     settings.ArtifactR2.AccessKeyID,
		SecretAccessKey: settings.ArtifactR2.SecretAccessKey,
		Bucket:          settings.ArtifactR2.Bucket,
		Prefix:          "artifacts/",
	})
	if err != nil {
		return nil, err
	}
	if legacy == nil {
		logger.InfoContext(ctx, "artifacts stored in R2", "bucket", settings.ArtifactR2.Bucket)
		return r2, nil
	}
	logger.InfoContext(ctx, "artifacts stored in R2, older ones still read from S3", "bucket", settings.ArtifactR2.Bucket, "legacy", settings.ArtifactBucket)
	return s3artifact.NewChain(r2, legacy)
}
