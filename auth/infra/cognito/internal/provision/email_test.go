package provision

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

func TestEmailConfiguration(t *testing.T) {
	if got := emailConfiguration(Config{}); got.EmailSendingAccount != types.EmailSendingAccountTypeCognitoDefault || got.SourceArn != nil {
		t.Fatalf("no SES identity = %+v, want Cognito's own sender", got)
	}

	const arn = "arn:aws:ses:us-west-2:123456789012:identity/example.com"
	got := emailConfiguration(Config{EmailSourceARN: arn, EmailFrom: "Oryxa <no-reply@example.com>"})
	if got.EmailSendingAccount != types.EmailSendingAccountTypeDeveloper || aws.ToString(got.SourceArn) != arn || aws.ToString(got.From) != "Oryxa <no-reply@example.com>" {
		t.Fatalf("SES identity = %+v, want DEVELOPER from %s", got, arn)
	}
	if got := emailConfiguration(Config{EmailSourceARN: arn}); got.From != nil {
		t.Fatalf("no From = %q, want Cognito to use the identity's address", aws.ToString(got.From))
	}
}

func TestFromEnvEmail(t *testing.T) {
	setBase := func(t *testing.T) {
		t.Setenv("COGNITO_DOMAIN_PREFIX", "oryxa-test")
		t.Setenv("COGNITO_CALLBACK_URLS", "http://localhost:8080/v1/auth/callback")
		t.Setenv("COGNITO_DEMO_PASSWORD_ONLY", "true")
		t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
		t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	}
	for name, test := range map[string]struct {
		arn, from string
		wantErr   bool
	}{
		"neither":          {},
		"both":             {arn: "arn:aws:ses:us-west-2:123456789012:identity/example.com", from: "Oryxa <no-reply@example.com>"},
		"from without arn": {from: "Oryxa <no-reply@example.com>", wantErr: true},
		"not an SES arn":   {arn: "arn:aws:sns:us-west-2:123456789012:topic", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			setBase(t)
			t.Setenv("COGNITO_EMAIL_SOURCE_ARN", test.arn)
			t.Setenv("COGNITO_EMAIL_FROM", test.from)
			config, err := FromEnv()
			if (err != nil) != test.wantErr {
				t.Fatalf("FromEnv() error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && (config.EmailSourceARN != test.arn || config.EmailFrom != test.from) {
				t.Fatalf("FromEnv() email = %q %q", config.EmailSourceARN, config.EmailFrom)
			}
		})
	}
}
