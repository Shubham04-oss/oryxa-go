package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

type comparison struct {
	ask     access.Ask
	allowed bool
}

type comparisons []comparison

func (seen *comparisons) Compare(_ context.Context, ask access.Ask, allowed bool) {
	*seen = append(*seen, comparison{ask, allowed})
}

// The shadow is told each allow and each deny canSee and canDelete make, and
// the caller gets the code's answer whatever SpiceDB would say.
func TestTheShadowSeesAllowsAndDeniesAndChangesNothing(t *testing.T) {
	var seen comparisons
	service := testService(newFakeStore(), blob.NewMemory()).WithShadow(&seen)
	ctx := context.Background()
	created, err := service.Create(ctx, creds("user-owner"), "org_1", given(CreateInput{Filename: "a.txt", ContentType: "text/plain", Content: []byte("a")}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, creds("user-member"), "org_1", created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member get error = %v, want not found", err)
	}
	if _, err := service.Get(ctx, creds("user-owner"), "org_1", created.ID); err != nil {
		t.Fatal(err)
	}
	if listed, err := service.List(ctx, creds("user-member"), "org_1", ListFilter{}); err != nil || len(listed) != 0 {
		t.Fatalf("member list = %+v, %v", listed, err)
	}
	if err := service.Delete(ctx, creds("user-owner"), "org_1", created.ID); err != nil {
		t.Fatal(err)
	}
	want := []comparison{
		{access.Ask{Action: access.ActionRead, UserID: "user-member"}, false},
		{access.Ask{Action: access.ActionRead, UserID: "user-owner"}, true},
		{access.Ask{Action: access.ActionRead, UserID: "user-owner"}, true},
		{access.Ask{Action: access.ActionDelete, UserID: "user-owner"}, true},
	}
	if len(seen) != len(want) {
		t.Fatalf("compared %+v", seen)
	}
	for i, got := range seen {
		if got.ask.Action != want[i].ask.Action || got.ask.UserID != want[i].ask.UserID || got.allowed != want[i].allowed ||
			got.ask.Service != "files" || got.ask.ResourceKind != "file" || got.ask.ResourceID != created.ID || got.ask.OrganizationID != "org_1" {
			t.Errorf("comparison %d = %+v, want %+v", i, got, want[i])
		}
	}
}
