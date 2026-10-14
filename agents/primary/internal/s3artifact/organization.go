package s3artifact

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"google.golang.org/adk/v2/artifact"
)

// ErrNoOrganization reports an artifact request whose context names no
// organization (WithOrganization). It is always wrapped.
var ErrNoOrganization = errors.New("artifact request has no organization")

type organizationKey struct{}

// WithOrganization names the organization the artifacts read and written
// under ctx belong to. adk-go's artifact requests carry an app, a member and
// a session but no organization, and a member can belong to several
// (live-layer L6), so it rides on the context, as a run's does.
func WithOrganization(ctx context.Context, organizationID string) context.Context {
	return context.WithValue(ctx, organizationKey{}, organizationID)
}

// ByOrganization keys every artifact in store under the organization on the
// request's context, by putting it before the app:
//
//	orgs/{organizationID}/{appName}/{userID}/{sessionID}/{fileName}/{version}
//	orgs/{organizationID}/{appName}/{userID}/user/{fileName}/{version}
//
// A request with no organization is refused. Artifacts saved before this,
// under keys with no organization, are read as follows:
//
//   - a session's files stay readable and their new versions follow on: a
//     session belongs to one organization, so its files cannot cross;
//   - a member's own "user:" files are not served at all: nothing records
//     which organization they were saved in, and serving them to every
//     organization is the leak. Copying them under orgs/{organizationID}/
//     brings them back.
//
// Moving the old member files, once, by hand: for each member with "user:"
// files ({prefix}{app}/{user}/user/...) who belongs to exactly one
// organization, copy each object to {prefix}orgs/{org}/{app}/{user}/user/...
// with the same key after it, then delete the old one. A member in several
// organizations is asked which one the files are for; until then they stay
// unread. The prefix is "artifacts/" on R2 and none on S3; do both while
// both are read.
//
// ponytail: the legacy read costs an extra listing per request; drop the
// legacySessions half of the chain once the old session files have expired.
func ByOrganization(store artifact.Service) artifact.Service {
	return &Chain{current: organized{store}, legacy: legacySessions{store}}
}

// organizationApp is the app name an artifact is keyed by in ctx's
// organization.
func organizationApp(ctx context.Context, appName string) (string, error) {
	organizationID, _ := ctx.Value(organizationKey{}).(string)
	if strings.TrimSpace(organizationID) == "" || strings.ContainsAny(organizationID, `/\`) {
		return "", fmt.Errorf("artifacts of %q: %w", appName, ErrNoOrganization)
	}
	return "orgs/" + organizationID + "/" + appName, nil
}

// organized is a store seen through the organization on each request.
type organized struct{ store artifact.Service }

func (view organized) Save(ctx context.Context, request *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	app, err := organizationApp(ctx, request.AppName)
	if err != nil {
		return nil, err
	}
	scoped := *request
	scoped.AppName = app
	return view.store.Save(ctx, &scoped)
}

func (view organized) Load(ctx context.Context, request *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	app, err := organizationApp(ctx, request.AppName)
	if err != nil {
		return nil, err
	}
	scoped := *request
	scoped.AppName = app
	return view.store.Load(ctx, &scoped)
}

func (view organized) Delete(ctx context.Context, request *artifact.DeleteRequest) error {
	app, err := organizationApp(ctx, request.AppName)
	if err != nil {
		return err
	}
	scoped := *request
	scoped.AppName = app
	return view.store.Delete(ctx, &scoped)
}

func (view organized) List(ctx context.Context, request *artifact.ListRequest) (*artifact.ListResponse, error) {
	app, err := organizationApp(ctx, request.AppName)
	if err != nil {
		return nil, err
	}
	scoped := *request
	scoped.AppName = app
	return view.store.List(ctx, &scoped)
}

func (view organized) Versions(ctx context.Context, request *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	app, err := organizationApp(ctx, request.AppName)
	if err != nil {
		return nil, err
	}
	scoped := *request
	scoped.AppName = app
	return view.store.Versions(ctx, &scoped)
}

func (view organized) GetArtifactVersion(ctx context.Context, request *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	app, err := organizationApp(ctx, request.AppName)
	if err != nil {
		return nil, err
	}
	scoped := *request
	scoped.AppName = app
	return view.store.GetArtifactVersion(ctx, &scoped)
}

// legacySessions is a store's keys from before organizations, sessions'
// files only, read-only.
type legacySessions struct{ store artifact.Service }

func notThere(fileName string) error {
	return fmt.Errorf("artifact %q not found: %w", fileName, fs.ErrNotExist)
}

func (legacySessions) Save(context.Context, *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	return nil, errors.New("s3artifact: keys without an organization are read-only")
}

func (view legacySessions) Load(ctx context.Context, request *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	if userNamespaced(request.FileName) {
		return nil, notThere(request.FileName)
	}
	return view.store.Load(ctx, request)
}

func (view legacySessions) Delete(ctx context.Context, request *artifact.DeleteRequest) error {
	if userNamespaced(request.FileName) {
		return nil
	}
	return view.store.Delete(ctx, request)
}

func (view legacySessions) List(ctx context.Context, request *artifact.ListRequest) (*artifact.ListResponse, error) {
	listed, err := view.store.List(ctx, request)
	if err != nil {
		return nil, err
	}
	return &artifact.ListResponse{FileNames: slices.DeleteFunc(listed.FileNames, userNamespaced)}, nil
}

func (view legacySessions) Versions(ctx context.Context, request *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	if userNamespaced(request.FileName) {
		return nil, notThere(request.FileName)
	}
	return view.store.Versions(ctx, request)
}

func (view legacySessions) GetArtifactVersion(ctx context.Context, request *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	if userNamespaced(request.FileName) {
		return nil, notThere(request.FileName)
	}
	return view.store.GetArtifactVersion(ctx, request)
}
