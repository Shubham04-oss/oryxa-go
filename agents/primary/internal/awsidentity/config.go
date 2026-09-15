// Package awsidentity resolves the AWS principal used by the agent's native
// AWS tools. It deliberately remains separate from AgentCore workload access
// tokens: Browser and Code Interpreter require SigV4 credentials.
package awsidentity

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const defaultRoleSessionName = "oryxa-agents-primary"

var (
	roleSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]{2,64}$`)
	externalIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_+=,.@:/-]+$`)
)

type Options struct {
	Region          string
	RoleARN         string
	RoleSessionName string
	ExternalID      string
}

// Load uses the standard AWS credential chain. When RoleARN is configured it
// exchanges those base credentials for a short-lived role session and verifies
// that exchange immediately so a misconfigured deployment cannot start.
func Load(ctx context.Context, options Options) (aws.Config, error) {
	options = normalized(options)
	if options.Region == "" {
		return aws.Config{}, fmt.Errorf("AWS region is required")
	}
	if err := validate(options); err != nil {
		return aws.Config{}, err
	}

	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(options.Region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("load default AWS configuration: %w", err)
	}
	if options.RoleARN == "" {
		return config, nil
	}

	provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(config), options.RoleARN, func(assume *stscreds.AssumeRoleOptions) {
		assume.RoleSessionName = options.RoleSessionName
		if options.ExternalID != "" {
			assume.ExternalID = aws.String(options.ExternalID)
		}
	})
	config.Credentials = aws.NewCredentialsCache(provider)
	if _, err := config.Credentials.Retrieve(ctx); err != nil {
		return aws.Config{}, fmt.Errorf("assume configured AWS role: %w", err)
	}
	return config, nil
}

func normalized(options Options) Options {
	options.Region = strings.TrimSpace(options.Region)
	options.RoleARN = strings.TrimSpace(options.RoleARN)
	options.RoleSessionName = strings.TrimSpace(options.RoleSessionName)
	options.ExternalID = strings.TrimSpace(options.ExternalID)
	if options.RoleARN != "" && options.RoleSessionName == "" {
		options.RoleSessionName = defaultRoleSessionName
	}
	return options
}

func validate(options Options) error {
	if options.RoleARN == "" {
		if options.ExternalID != "" {
			return fmt.Errorf("AWS external ID requires an AWS role ARN")
		}
		return nil
	}
	parsed, err := arn.Parse(options.RoleARN)
	if err != nil || parsed.Service != "iam" || parsed.Region != "" || parsed.AccountID == "" || !strings.HasPrefix(parsed.Resource, "role/") || len(strings.TrimPrefix(parsed.Resource, "role/")) == 0 {
		return fmt.Errorf("AWS role ARN must identify an IAM role")
	}
	if !roleSessionNamePattern.MatchString(options.RoleSessionName) {
		return fmt.Errorf("AWS role session name must be 2-64 IAM-safe characters")
	}
	if options.ExternalID != "" && (len(options.ExternalID) < 2 || len(options.ExternalID) > 1224 || !externalIDPattern.MatchString(options.ExternalID)) {
		return fmt.Errorf("AWS external ID must be 2-1224 IAM-safe characters")
	}
	return nil
}
