package application

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// fakeFilesClient answers what the files service would, through the real
// files extractor: every object it holds, to anyone it does not refuse, and
// what it refuses to whom, by the token they read with.
type fakeFilesClient struct {
	mu      sync.Mutex
	objects []filesclient.Object
	bodies  map[string]string
	refused map[string]error
	// listed and fetched are the tokens that listed and the file ids whose
	// bytes were read, in order.
	listed  []string
	fetched []string
	// afterListing runs once a listing has been answered: something
	// happening while a pass is under way.
	afterListing func()
}

func (files *fakeFilesClient) Objects(_ context.Context, _, token string, query filesclient.Query) ([]filesclient.Object, error) {
	listed, err := files.list(token, query)
	if err == nil && files.afterListing != nil {
		files.afterListing()
	}
	return listed, err
}

func (files *fakeFilesClient) list(token string, query filesclient.Query) ([]filesclient.Object, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.listed = append(files.listed, token)
	if err := files.refused[token]; err != nil {
		return nil, err
	}
	var listed []filesclient.Object
	for _, object := range files.objects {
		if object.Scope == query.Scope && (query.TeamID == "" || object.TeamID == query.TeamID) {
			listed = append(listed, object)
		}
	}
	return listed, nil
}

func (files *fakeFilesClient) Content(_ context.Context, _, fileID, token string) ([]byte, string, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.fetched = append(files.fetched, fileID)
	if err := files.refused[token]; err != nil {
		return nil, "", err
	}
	body, ok := files.bodies[fileID]
	if !ok {
		return nil, "", domain.ErrNotFound
	}
	return []byte(body), "text/markdown", nil
}

// realFilesService is knowledge reading files through the real extractor,
// each member with a token of their own.
func realFilesService(t *testing.T, store *fakeStore, files *fakeFilesClient, caller domain.OrgMembership) (*Service, *fakeGrants) {
	t.Helper()
	grants := &fakeGrants{token: "member-token", tokens: map[string]string{
		"user-1": "token-user-1", "user-2": "token-user-2", "user-left-team": "token-user-left-team",
	}}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: caller},
		Grants:     grants,
		Extractors: sources.Registry{"files": sources.NewFiles(files)},
		Clock:      func() time.Time { return testClock },
	}, Config{})
	return service, grants
}

func teamFile(id, teamID, createdBy string) filesclient.Object {
	return filesclient.Object{
		ID: id, OrganizationID: "org_1", Scope: string(domain.ScopeTeam), TeamID: teamID,
		Filename: id + ".md", ContentType: "text/markdown", Purpose: "upload", Access: "members",
		SHA256: "sha-" + id, CreatedBy: createdBy, CreatedAt: testClock.Add(-time.Hour),
	}
}

// A team's files source reads as the member whose upload started it. Once
// they are off the team, Files refuses them the team's files on every pass:
// the source is retired rather than backed off for ever, and the next member
// to upload there takes the placement over.
func TestAPlacementItsSourceCanNoLongerReadGoesToTheNextUploader(t *testing.T) {
	for name, refusal := range map[string]error{"not found": domain.ErrNotFound, "forbidden": domain.ErrForbidden} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			files := &fakeFilesClient{
				objects: []filesclient.Object{teamFile("file_old", "team_eng", "user-left-team")},
				bodies:  map[string]string{"file_old": "# the old notes"},
				refused: map[string]error{"token-user-left-team": refusal},
			}
			service, grants := realFilesService(t, store, files, member())
			stale := domain.Source{
				ID: "src_stale", OrganizationID: "org_1", Provider: "files",
				Target: map[string]string{"team": "team_eng"}, Scope: domain.ScopeTeam, TeamID: "team_eng",
				CreatedBy: "user-left-team", Status: domain.StatusActive, Interval: time.Hour, FullSweepAt: &testClock,
			}
			store.sources[stale.ID] = stale
			store.lease[stale.ID] = "lease-a"
			store.documents["src_stale\x00file_old"] = domain.Document{
				ID: "doc_old", OrganizationID: "org_1", SourceID: "src_stale", ExternalID: "file_old",
				Body: "# the old notes", LastSeenAt: testClock,
			}

			if err := service.Sync(context.Background(), stale, "lease-a"); err != nil {
				t.Fatal(err)
			}
			retired := store.sources[stale.ID]
			if retired.Status != domain.StatusRemoved || retired.Reason != knowledgev1.RemovedReasonNoLongerReadable {
				t.Fatalf("the source Files refused = %s, reason %q; want it retired as no longer readable", retired.Status, retired.Reason)
			}
			if store.failedReason != "" {
				t.Fatalf("the source was backed off with %q, as though trying again could read it", store.failedReason)
			}
			if store.documents["src_stale\x00file_old"].RemovedAt == nil {
				t.Fatal("what the retired source held is still searchable, with nothing left to keep it current")
			}
			if reasons := store.publishedReasons(knowledgev1.DocumentRemovedDetailType); len(reasons) != 1 ||
				reasons[0] != knowledgev1.RemovedReasonNoLongerReadable {
				t.Fatalf("removals = %v, want the old notes, as no longer readable", reasons)
			}

			upload := storedFile("team")
			upload.FileID, upload.TeamID, upload.CreatedBy = "file_new", "team_eng", "user-1"
			if err := service.FileStored(context.Background(), upload); err != nil {
				t.Fatal(err)
			}
			var replacement domain.Source
			for _, source := range store.sources {
				if source.Status == domain.StatusActive {
					replacement = source
				}
			}
			if replacement.ID == "" || replacement.CreatedBy != "user-1" || replacement.TeamID != "team_eng" {
				t.Fatalf("after the next team upload, live source = %+v; want user-1's for team_eng", replacement)
			}
			if got := grants.members[len(grants.members)-1]; got != "org_1/user-1" {
				t.Fatalf("the replacement reads as %s, want the uploader", got)
			}
		})
	}
}

// Only Files refusing the placement retires a source. Files being down, or
// a token it did not take, is tried again with backoff, as before.
func TestAFilesSourceIsNotRetiredForWhatTryingAgainCanFix(t *testing.T) {
	for name, cause := range map[string]error{
		"files is down":         domain.ErrProviderUnavailable,
		"the token was refused": domain.ErrUnauthorized,
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			files := &fakeFilesClient{refused: map[string]error{"token-user-1": cause}}
			service, _ := realFilesService(t, store, files, member())
			source := domain.Source{
				ID: "src_1", OrganizationID: "org_1", Provider: "files", Scope: domain.ScopeOrganization,
				CreatedBy: "user-1", Status: domain.StatusActive, Interval: time.Hour, FullSweepAt: &testClock,
			}
			store.sources[source.ID] = source
			store.lease[source.ID] = "lease-a"
			if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
				t.Fatal(err)
			}
			if store.sources[source.ID].Status != domain.StatusActive || store.failedReason == "" {
				t.Fatalf("source = %s, failed %q; want it active and backed off", store.sources[source.ID].Status, store.failedReason)
			}
		})
	}
}

// A member who cannot read a placement cannot register a source for it:
// every pass would be refused, and while it stood, nobody who can read the
// placement could have it indexed. Files is asked, as the member the source
// would read as, before anything is stored.
func TestAFilesSourceIsRegisteredOnlyForWhatItsMemberCanRead(t *testing.T) {
	store := newFakeStore()
	files := &fakeFilesClient{refused: map[string]error{"token-user-2": domain.ErrNotFound}}
	outsider := member()
	outsider.UserID = "user-2"
	service, _ := realFilesService(t, store, files, outsider)
	team := RegisterInput{Provider: "files", Target: map[string]string{"team": "team_eng"}}

	_, err := service.RegisterSource(context.Background(), authclient.Credentials{Authorization: "Bearer token-user-2"}, "org_1", team)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a member off the team registered its files: %v", err)
	}
	if len(store.sources) != 0 {
		t.Fatalf("sources = %+v, want none stored", store.sources)
	}
	if len(files.listed) != 1 || files.listed[0] != "token-user-2" {
		t.Fatalf("files was asked as %v, want the member registering", files.listed)
	}

	// Their attempt holds nothing, so the team's next upload is indexed as
	// its uploader, who can read it.
	service.directory.(*fakeDirectory).membership = member()
	upload := storedFile("team")
	upload.TeamID, upload.CreatedBy = "team_eng", "user-1"
	if err := service.FileStored(context.Background(), upload); err != nil {
		t.Fatal(err)
	}
	if len(store.sources) != 1 {
		t.Fatalf("sources after the team's upload = %d, want the uploader's", len(store.sources))
	}
	for _, source := range store.sources {
		if source.CreatedBy != "user-1" || source.TeamID != "team_eng" {
			t.Fatalf("source = %+v, want user-1's for team_eng", source)
		}
	}

	// A member signed in with the web app's cookie is asked as the same
	// token the cookie carries.
	files.listed = nil
	if _, err := service.RegisterSource(context.Background(), authclient.Credentials{AccessCookie: "token-user-1"}, "org_1",
		RegisterInput{Provider: "files", Target: map[string]string{"member": "self"}}); err != nil {
		t.Fatalf("RegisterSource(cookie) = %v", err)
	}
	if len(files.listed) != 1 || files.listed[0] != "token-user-1" {
		t.Fatalf("files was asked as %v, want the cookie's token", files.listed)
	}
}

// Every upload brings the placement's source forward, and Files answers its
// whole listing at once: an incremental pass that read every file's bytes
// again would download the whole placement on every upload. A file's id names
// the same bytes for as long as it exists, so a pass reads only what the
// source does not already hold as it is. A full sweep still reads everything.
func TestAPassAfterAnUploadReadsOnlyTheNewFile(t *testing.T) {
	store := newFakeStore()
	files := &fakeFilesClient{
		objects: []filesclient.Object{teamFile("file_1", "team_eng", "user-1"), teamFile("file_2", "team_eng", "user-1")},
		bodies:  map[string]string{"file_1": "# one", "file_2": "# two", "file_3": "# three"},
	}
	service, _ := realFilesService(t, store, files, member())
	source := domain.Source{
		ID: "src_team", OrganizationID: "org_1", Provider: "files",
		Target: map[string]string{"team": "team_eng"}, Scope: domain.ScopeTeam, TeamID: "team_eng",
		CreatedBy: "user-1", Status: domain.StatusActive, Interval: time.Hour,
	}
	store.sources[source.ID] = source
	store.lease[source.ID] = "lease-a"
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if len(files.fetched) != 2 || store.published(knowledgev1.DocumentPublishedDetailType) != 2 {
		t.Fatalf("first pass read %v and published %d, want both files", files.fetched, store.published(knowledgev1.DocumentPublishedDetailType))
	}

	files.objects = append(files.objects, teamFile("file_3", "team_eng", "user-1"))
	files.fetched = nil
	source = store.sources[source.ID]
	swept := testClock
	source.FullSweepAt = &swept
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files.fetched, []string{"file_3"}) {
		t.Fatalf("the pass after an upload read %v, want only the new file", files.fetched)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 3 {
		t.Fatalf("published %d in all, want the new file's alone added", got)
	}
	for _, id := range []string{"file_1", "file_2"} {
		if document := store.documents["src_team\x00"+id]; document.RemovedAt != nil || !document.LastSeenAt.Equal(testClock) {
			t.Fatalf("%s = %+v, want it still live and seen", id, document)
		}
	}

	// A week on, the full sweep reads every file again, as every other
	// source's does.
	files.fetched = nil
	source = store.sources[source.ID]
	source.FullSweepAt = &time.Time{}
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if len(files.fetched) != 3 {
		t.Fatalf("the full sweep read %v, want every file", files.fetched)
	}
}

// An upload that arrives while a pass is under way may land after that pass
// listed the placement. It is kept for the pass to find when it ends, so the
// next pass runs right after rather than a whole interval later.
func TestAnUploadDuringAPassIsReadRightAfterIt(t *testing.T) {
	store := newFakeStore()
	files := &fakeFilesClient{
		objects: []filesclient.Object{teamFile("file_1", "team_eng", "user-1")},
		bodies:  map[string]string{"file_1": "# one"},
	}
	service, _ := realFilesService(t, store, files, member())
	due := testClock.Add(-time.Minute)
	store.sources["src_team"] = domain.Source{
		ID: "src_team", OrganizationID: "org_1", Provider: "files",
		Target: map[string]string{"team": "team_eng"}, Scope: domain.ScopeTeam, TeamID: "team_eng",
		CreatedBy: "user-1", Status: domain.StatusActive, Interval: time.Hour, SyncAt: &due, FullSweepAt: &testClock,
	}
	files.afterListing = func() {
		upload := storedFile("team")
		upload.FileID, upload.TeamID, upload.CreatedBy = "file_2", "team_eng", "user-1"
		if err := service.FileStored(context.Background(), upload); err != nil {
			t.Error(err)
		}
	}

	service.SyncDue(context.Background())
	if len(files.listed) != 1 {
		t.Fatalf("listed %d times, want the one pass", len(files.listed))
	}
	if !store.finishedNext.Equal(testClock) {
		t.Fatalf("the pass scheduled the next for %v, want now: an upload arrived while it ran", store.finishedNext)
	}
	if next := store.sources["src_team"].SyncAt; next == nil || !next.Equal(testClock) {
		t.Fatalf("the source is due at %v, want now", next)
	}
}

// A burst of uploads before the next pass has started costs that pass, not a
// write for every upload: the source is already due.
func TestUploadsToASourceAlreadyDueWriteNothing(t *testing.T) {
	store := newFakeStore()
	service, _ := filesService(t, store)
	due := testClock.Add(-time.Minute)
	store.sources["src_org"] = domain.Source{
		ID: "src_org", OrganizationID: "org_1", Provider: "files", Scope: domain.ScopeOrganization,
		CreatedBy: "user-1", Status: domain.StatusActive, Interval: time.Hour, SyncAt: &due,
	}
	for range 3 {
		if err := service.FileStored(context.Background(), storedFile("organization")); err != nil {
			t.Fatal(err)
		}
	}
	if store.hastenCalls != 0 {
		t.Fatalf("wrote %d times for uploads to a source already due, want none", store.hastenCalls)
	}
	if next := store.sources["src_org"].SyncAt; next == nil || !next.Equal(due) {
		t.Fatalf("the source is due at %v, want it left due at %v", next, due)
	}
}
