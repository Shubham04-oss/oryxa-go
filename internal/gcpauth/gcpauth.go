// Package gcpauth decides how the process authenticates to Google Cloud.
//
// Where Google's own discovery finds credentials — `gcloud auth
// application-default login` on a laptop — nothing here is used: every client
// falls back to Application Default Credentials exactly as it did before this
// package existed. On AWS there is no such login and no service account key
// worth holding, so the process federates the AWS identity it already has
// instead. Its role signs an STS GetCallerIdentity request; Google's Security
// Token Service has AWS check that signature and, through a workload identity
// pool whose provider admits the role, returns a federated token; and that
// token impersonates a service account, which holds the Google Cloud roles.
//
// Google's own external_account configuration for AWS cannot do this on ECS
// Fargate. It reads AWS credentials from AWS_ACCESS_KEY_ID and its kin or from
// EC2's instance metadata service, and a Fargate task has neither: its
// credentials come from the container credentials endpoint. So the AWS half
// here is the AWS SDK's own credential chain, which knows that endpoint — the
// same chain every AWS client in the process already signs with.
package gcpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials/externalaccount"
	"cloud.google.com/go/auth/httptransport"
	"github.com/aws/aws-sdk-go-v2/aws"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// The variables that turn federation on. Both are needed on AWS, and neither
// anywhere Application Default Credentials can be found.
const (
	// ProviderVariable holds the workload identity pool provider's audience,
	// such as //iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/oryxa/providers/aws.
	ProviderVariable = "GOOGLE_WORKLOAD_IDENTITY_PROVIDER"
	// ServiceAccountVariable holds the email of the service account the
	// process acts as, such as oryxa@internalbeta.iam.gserviceaccount.com.
	ServiceAccountVariable = "GOOGLE_SERVICE_ACCOUNT"
)

// Scope is what every Google client here asks for. Vertex AI, Memory Bank and
// the Ranking API all accept cloud-platform; what narrows it is the roles the
// service account holds.
const Scope = "https://www.googleapis.com/auth/cloud-platform"

// awsSubjectTokenType tells Google's STS that the subject token is a signed
// AWS GetCallerIdentity request.
const awsSubjectTokenType = "urn:ietf:params:aws:token-type:aws4_request"

// impersonationURL is where the federated token is exchanged for the service
// account's own. The "-" is IAM Credentials' wildcard project: the email alone
// names the account.
const impersonationURL = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:generateAccessToken"

var (
	// providerPattern is a workload identity pool provider's audience. STS
	// knows the project by its number, so a project ID there is refused here
	// rather than by STS at the first call.
	providerPattern = regexp.MustCompile(`^//iam\.googleapis\.com/projects/[0-9]+/locations/global/workloadIdentityPools/[^/\s]+/providers/[^/\s]+$`)
	// serviceAccountPattern is a service account's email. It also goes into
	// the impersonation URL's path, so it may hold no slash.
	serviceAccountPattern = regexp.MustCompile(`^[^@/\s]+@[^@/\s]+\.gserviceaccount\.com$`)
)

// Config says which workload identity pool provider vouches for the process's
// AWS role, and which service account that role becomes. The zero Config
// federates nothing.
type Config struct {
	Provider       string
	ServiceAccount string
}

// ConfigFromEnv reads the two variables. With neither set it returns the zero
// Config, and every client keeps to Application Default Credentials exactly
// as it would without this package. One without the other is an error naming
// both: falling back to Application Default Credentials there would start a
// process that on Fargate has no Google credentials at all, and would say so
// only at its first call to Google.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	config := Config{
		Provider:       strings.TrimSpace(getenv(ProviderVariable)),
		ServiceAccount: strings.TrimSpace(getenv(ServiceAccountVariable)),
	}
	if config == (Config{}) {
		return Config{}, nil
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) validate() error {
	switch {
	case config.Provider == "" || config.ServiceAccount == "":
		missing := ProviderVariable
		if config.ServiceAccount == "" {
			missing = ServiceAccountVariable
		}
		return fmt.Errorf("%s is not set: %s and %s reach Google Cloud from AWS together, "+
			"and with neither set the process uses Application Default Credentials", missing, ProviderVariable, ServiceAccountVariable)
	case !providerPattern.MatchString(config.Provider):
		return fmt.Errorf("%s must be a workload identity pool provider's audience, "+
			"//iam.googleapis.com/projects/<project number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>", ProviderVariable)
	case !serviceAccountPattern.MatchString(config.ServiceAccount):
		return fmt.Errorf("%s must be a service account's email, such as oryxa@<project>.iam.gserviceaccount.com", ServiceAccountVariable)
	}
	return nil
}

// Credentials returns credentials that reach Google Cloud as config's service
// account, signed in with the AWS identity awsConfig carries. For the zero
// Config it returns nil, which every Google client takes to mean Application
// Default Credentials.
//
// It contacts nothing. The AWS credentials are retrieved, and exchanged, when
// a client first asks for a token; the token is held until shortly before it
// expires, so the exchange runs about once an hour however many clients
// share the credentials.
func Credentials(config Config, awsConfig aws.Config) (*auth.Credentials, error) {
	return newCredentials(config, awsConfig, nil)
}

// newCredentials is Credentials with the client Google's endpoints are called
// through, which a test stands in for. Nil is the library's own client.
func newCredentials(config Config, awsConfig aws.Config, client *http.Client) (*auth.Credentials, error) {
	if config == (Config{}) {
		return nil, nil
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	if awsConfig.Credentials == nil {
		return nil, errors.New("reaching Google Cloud from AWS needs AWS credentials to sign in with, and the AWS configuration has none")
	}
	if strings.TrimSpace(awsConfig.Region) == "" {
		return nil, errors.New("reaching Google Cloud from AWS needs the AWS region whose STS vouches for the process, and the AWS configuration names none")
	}
	credentials, err := externalaccount.NewCredentials(&externalaccount.Options{
		Audience:                       config.Provider,
		SubjectTokenType:               awsSubjectTokenType,
		ServiceAccountImpersonationURL: fmt.Sprintf(impersonationURL, config.ServiceAccount),
		Scopes:                         []string{Scope},
		AwsSecurityCredentialsProvider: newAWSCredentials(awsConfig),
		Client:                         client,
	})
	if err != nil {
		return nil, fmt.Errorf("build workload identity federation credentials: %w", err)
	}
	return credentials, nil
}

// HTTPClient returns a client whose requests carry an access token: from
// credentials when there are any, and otherwise from Application Default
// Credentials for scope, found exactly as google.DefaultTokenSource finds
// them — which is how a client calling Google's REST APIs itself
// authenticated before federation. Credentials carry the scope they were
// built with.
func HTTPClient(ctx context.Context, credentials *auth.Credentials, scope string) (*http.Client, error) {
	if credentials == nil {
		source, err := google.DefaultTokenSource(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("find Google application default credentials: %w", err)
		}
		return oauth2.NewClient(ctx, source), nil
	}
	// The token is asked for with each request's own context, so a request's
	// deadline also bounds the exchange a stale token sets off.
	client := &http.Client{}
	if err := httptransport.AddAuthorizationMiddleware(client, credentials); err != nil {
		return nil, err
	}
	return client, nil
}

// awsCredentials hands Google's token exchange the AWS SDK's credentials and
// region, so the GetCallerIdentity request it signs is signed as the process's
// own role, wherever the SDK found it: on Fargate, the container credentials
// endpoint.
type awsCredentials struct {
	provider aws.CredentialsProvider
	region   string
}

var _ externalaccount.AwsSecurityCredentialsProvider = (*awsCredentials)(nil)

func newAWSCredentials(config aws.Config) *awsCredentials {
	provider := config.Credentials
	// The exchange keeps nothing it is handed and asks again for every token
	// it mints. The SDK's own configuration is cached already; anything else
	// is cached here the way the SDK caches it for its own clients, so the
	// container endpoint is asked once per rotation of the role's keys.
	if _, cached := provider.(*aws.CredentialsCache); !cached {
		provider = aws.NewCredentialsCache(provider)
	}
	return &awsCredentials{provider: provider, region: config.Region}
}

// AwsRegion implements externalaccount.AwsSecurityCredentialsProvider. The
// region picks the regional AWS STS endpoint the signed request names, which
// Google's STS calls to learn who signed it.
func (source *awsCredentials) AwsRegion(context.Context, *externalaccount.RequestOptions) (string, error) {
	return source.region, nil
}

// AwsSecurityCredentials implements
// externalaccount.AwsSecurityCredentialsProvider. The secret key only signs:
// what Google receives is the signature.
func (source *awsCredentials) AwsSecurityCredentials(ctx context.Context, _ *externalaccount.RequestOptions) (*externalaccount.AwsSecurityCredentials, error) {
	retrieved, err := source.provider.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve the AWS credentials that sign in to Google Cloud: %w", err)
	}
	if !retrieved.HasKeys() {
		return nil, errors.New("the AWS credentials that sign in to Google Cloud have no keys")
	}
	return &externalaccount.AwsSecurityCredentials{
		AccessKeyID:     retrieved.AccessKeyID,
		SecretAccessKey: retrieved.SecretAccessKey,
		SessionToken:    retrieved.SessionToken,
	}, nil
}
