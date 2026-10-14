package s3artifact

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sort"

	"google.golang.org/adk/v2/artifact"
)

// Chain writes artifacts to one store and reads from it, falling back to an
// older one for what is only there: the move from S3 to R2 (live-layer A7),
// during which artifacts saved before it stay readable until they are
// copied over. New versions are numbered after the highest in either store,
// so an old version is never shadowed by a new one with its number.
type Chain struct {
	current artifact.Service
	legacy  artifact.Service
}

// NewChain returns a Chain writing to current and also reading legacy.
func NewChain(current, legacy artifact.Service) (*Chain, error) {
	if current == nil || legacy == nil {
		return nil, errors.New("s3artifact: a chain needs both stores")
	}
	return &Chain{current: current, legacy: legacy}, nil
}

// Save implements artifact.Service.
func (chain *Chain) Save(ctx context.Context, request *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	if request.Version != 0 {
		return chain.current.Save(ctx, request)
	}
	legacy, err := versionsOf(ctx, chain.legacy, request.AppName, request.UserID, request.SessionID, request.FileName)
	if err != nil {
		return nil, err
	}
	if len(legacy) == 0 {
		return chain.current.Save(ctx, request)
	}
	current, err := versionsOf(ctx, chain.current, request.AppName, request.UserID, request.SessionID, request.FileName)
	if err != nil {
		return nil, err
	}
	next := *request
	next.Version = slices.Max(append(legacy, current...)) + 1
	return chain.current.Save(ctx, &next)
}

// Load implements artifact.Service: the latest version across both stores
// when none is named, and otherwise wherever that version is.
func (chain *Chain) Load(ctx context.Context, request *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	if request.Version == 0 {
		latest, store, err := chain.latest(ctx, request.AppName, request.UserID, request.SessionID, request.FileName)
		if err != nil {
			return nil, err
		}
		named := *request
		named.Version = latest
		return store.Load(ctx, &named)
	}
	loaded, err := chain.current.Load(ctx, request)
	if errors.Is(err, fs.ErrNotExist) {
		return chain.legacy.Load(ctx, request)
	}
	return loaded, err
}

// Delete implements artifact.Service, in both stores.
func (chain *Chain) Delete(ctx context.Context, request *artifact.DeleteRequest) error {
	return errors.Join(chain.current.Delete(ctx, request), chain.legacy.Delete(ctx, request))
}

// List implements artifact.Service: the names in either store.
func (chain *Chain) List(ctx context.Context, request *artifact.ListRequest) (*artifact.ListResponse, error) {
	names := map[string]bool{}
	for _, store := range []artifact.Service{chain.current, chain.legacy} {
		listed, err := store.List(ctx, request)
		if err != nil {
			return nil, err
		}
		for _, name := range listed.FileNames {
			names[name] = true
		}
	}
	all := slices.Collect(maps.Keys(names))
	sort.Strings(all)
	return &artifact.ListResponse{FileNames: all}, nil
}

// Versions implements artifact.Service: the versions in either store.
func (chain *Chain) Versions(ctx context.Context, request *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	current, err := versionsOf(ctx, chain.current, request.AppName, request.UserID, request.SessionID, request.FileName)
	if err != nil {
		return nil, err
	}
	legacy, err := versionsOf(ctx, chain.legacy, request.AppName, request.UserID, request.SessionID, request.FileName)
	if err != nil {
		return nil, err
	}
	all := append(current, legacy...)
	if len(all) == 0 {
		return nil, fmt.Errorf("artifact %q not found: %w", request.FileName, fs.ErrNotExist)
	}
	slices.Sort(all)
	return &artifact.VersionsResponse{Versions: slices.Compact(all)}, nil
}

// GetArtifactVersion implements artifact.Service.
func (chain *Chain) GetArtifactVersion(ctx context.Context, request *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	if request.Version == 0 {
		latest, store, err := chain.latest(ctx, request.AppName, request.UserID, request.SessionID, request.FileName)
		if err != nil {
			return nil, err
		}
		named := *request
		named.Version = latest
		return store.GetArtifactVersion(ctx, &named)
	}
	found, err := chain.current.GetArtifactVersion(ctx, request)
	if errors.Is(err, fs.ErrNotExist) {
		return chain.legacy.GetArtifactVersion(ctx, request)
	}
	return found, err
}

// latest is the highest version of an artifact and the store it is in.
func (chain *Chain) latest(ctx context.Context, appName, userID, sessionID, fileName string) (int64, artifact.Service, error) {
	current, err := versionsOf(ctx, chain.current, appName, userID, sessionID, fileName)
	if err != nil {
		return 0, nil, err
	}
	legacy, err := versionsOf(ctx, chain.legacy, appName, userID, sessionID, fileName)
	if err != nil {
		return 0, nil, err
	}
	switch {
	case len(current) == 0 && len(legacy) == 0:
		return 0, nil, fmt.Errorf("artifact %q not found: %w", fileName, fs.ErrNotExist)
	case len(legacy) == 0 || (len(current) > 0 && slices.Max(current) >= slices.Max(legacy)):
		return slices.Max(current), chain.current, nil
	default:
		return slices.Max(legacy), chain.legacy, nil
	}
}

// versionsOf lists an artifact's versions in one store, none when it has
// none.
func versionsOf(ctx context.Context, store artifact.Service, appName, userID, sessionID, fileName string) ([]int64, error) {
	response, err := store.Versions(ctx, &artifact.VersionsRequest{AppName: appName, UserID: userID, SessionID: sessionID, FileName: fileName})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return response.Versions, nil
}
