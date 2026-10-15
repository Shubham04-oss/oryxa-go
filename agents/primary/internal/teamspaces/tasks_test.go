package teamspaces

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func (repo *fakeRepository) ListTasks(_ context.Context, _, teamspaceID string) ([]Task, error) {
	tasks := []Task{}
	for _, task := range repo.tasks {
		if task.TeamspaceID == teamspaceID {
			tasks = append(tasks, task)
		}
	}
	slices.SortStableFunc(tasks, func(left, right Task) int { return left.Position - right.Position })
	return tasks, nil
}

func (repo *fakeRepository) GetTask(_ context.Context, _, teamspaceID, taskID string) (Task, error) {
	for _, task := range repo.tasks {
		if task.TeamspaceID == teamspaceID && task.ID == taskID {
			return task, nil
		}
	}
	return Task{}, ErrNotFound
}

func (repo *fakeRepository) CreateTask(_ context.Context, task Task) (Task, error) {
	if task.Position < 0 {
		task.Position = 0
		for _, existing := range repo.tasks {
			if existing.TeamspaceID == task.TeamspaceID && existing.Position >= task.Position {
				task.Position = existing.Position + 1
			}
		}
	}
	task.CreatedAt, task.UpdatedAt = time.Unix(4, 0).UTC(), time.Unix(4, 0).UTC()
	repo.tasks = append(repo.tasks, task)
	return task, nil
}

func (repo *fakeRepository) UpdateTask(_ context.Context, _, teamspaceID, taskID string, input TaskUpdate) (Task, error) {
	for index, task := range repo.tasks {
		if task.TeamspaceID == teamspaceID && task.ID == taskID {
			repo.tasks[index] = applyTaskUpdate(task, input, time.Unix(5, 0).UTC())
			return repo.tasks[index], nil
		}
	}
	return Task{}, ErrNotFound
}

func (repo *fakeRepository) DeleteTask(_ context.Context, _, teamspaceID, taskID string) error {
	for index, task := range repo.tasks {
		if task.TeamspaceID == teamspaceID && task.ID == taskID {
			repo.tasks = slices.Delete(repo.tasks, index, index+1)
			return nil
		}
	}
	return ErrNotFound
}

func taskSpace(status Status) *fakeRepository {
	return &fakeRepository{spaces: []Space{{ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering", Status: status}}}
}

func teammate() Actor { return Actor{OrganizationID: "org_1", UserID: "user_2", AccessToken: "token"} }

func TestMembersKeepATeamspaceChecklistInOrder(t *testing.T) {
	repo := taskSpace(StatusActive)
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	first, err := service.CreateTask(context.Background(), actor(), "tsp_1", TaskInput{
		Title: "  Draft the   launch post ", OwnerUserID: ptr("user_2"), DueDate: ptr("2026-10-30"),
	})
	if err != nil || first.Title != "Draft the launch post" || *first.OwnerUserID != "user_2" || first.CreatedBy != "user_1" || first.Position != 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := service.CreateTask(context.Background(), teammate(), "tsp_1", TaskInput{Title: "Book the venue"})
	if err != nil || second.Position != 1 || second.OwnerUserID != nil {
		t.Fatalf("second = %+v, %v", second, err)
	}

	ticked, err := service.UpdateTask(context.Background(), teammate(), "tsp_1", first.ID, TaskUpdate{Done: ptr(true), OwnerUserID: Nullable{Set: true}})
	if err != nil || !ticked.Done || ticked.DoneAt == nil || ticked.OwnerUserID != nil || ticked.DueDate == nil {
		t.Fatalf("ticked = %+v, %v", ticked, err)
	}
	unticked, err := service.UpdateTask(context.Background(), actor(), "tsp_1", first.ID, TaskUpdate{Done: ptr(false), Position: ptr(5)})
	if err != nil || unticked.Done || unticked.DoneAt != nil || unticked.Position != 5 {
		t.Fatalf("unticked = %+v, %v", unticked, err)
	}
	tasks, err := service.ListTasks(context.Background(), teammate(), "tsp_1")
	if err != nil || len(tasks) != 2 || tasks[0].ID != second.ID || tasks[1].ID != first.ID {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}
}

func TestATaskIsRefusedWhatItCannotHold(t *testing.T) {
	service, _ := New(taskSpace(StatusActive), fakeDirectory{directory: teamDirectory(false, true)})
	for name, input := range map[string]TaskInput{
		"no title":              {Title: "   "},
		"a title past 200":      {Title: strings.Repeat("a", 201)},
		"an owner off the team": {Title: "Ship", OwnerUserID: ptr("user_elsewhere")},
		"a due date that isn't": {Title: "Ship", DueDate: ptr("next week")},
		"a negative position":   {Title: "Ship", Position: ptr(-1)},
	} {
		if _, err := service.CreateTask(context.Background(), actor(), "tsp_1", input); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	created, _ := service.CreateTask(context.Background(), actor(), "tsp_1", TaskInput{Title: "Ship"})
	if _, err := service.UpdateTask(context.Background(), actor(), "tsp_1", created.ID, TaskUpdate{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty update: %v, want ErrInvalid", err)
	}
	if _, err := service.UpdateTask(context.Background(), actor(), "tsp_1", created.ID, TaskUpdate{Title: ptr(" ")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an emptied title: %v, want ErrInvalid", err)
	}
}

func TestOnlyItsCreatorOrAManagerRemovesATask(t *testing.T) {
	repo := taskSpace(StatusActive)
	members, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	created, err := members.CreateTask(context.Background(), actor(), "tsp_1", TaskInput{Title: "Ship"})
	if err != nil {
		t.Fatal(err)
	}
	if err := members.DeleteTask(context.Background(), teammate(), "tsp_1", created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another member's delete: %v, want ErrForbidden", err)
	}
	gracesTask, _ := members.CreateTask(context.Background(), teammate(), "tsp_1", TaskInput{Title: "Grace's"})
	managers, _ := New(repo, fakeDirectory{directory: teamDirectory(true, true)})
	if err := managers.DeleteTask(context.Background(), actor(), "tsp_1", gracesTask.ID); err != nil {
		t.Fatalf("a manager's delete: %v", err)
	}
	if err := members.DeleteTask(context.Background(), actor(), "tsp_1", created.ID); err != nil {
		t.Fatalf("the creator's delete: %v", err)
	}
	if len(repo.tasks) != 0 {
		t.Fatalf("tasks left = %+v", repo.tasks)
	}
}

func TestAPausedChecklistIsReadOnlyAndOnlyMembersUseIt(t *testing.T) {
	repo := taskSpace(StatusDisabled)
	repo.tasks = []Task{{ID: "tst_1", TeamspaceID: "tsp_1", Title: "Ship", CreatedBy: "user_1"}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(true, true)})
	if tasks, err := service.ListTasks(context.Background(), actor(), "tsp_1"); err != nil || len(tasks) != 1 {
		t.Fatalf("paused list = %+v, %v", tasks, err)
	}
	if _, err := service.CreateTask(context.Background(), actor(), "tsp_1", TaskInput{Title: "More"}); !errors.Is(err, ErrPaused) {
		t.Errorf("paused create: %v, want ErrPaused", err)
	}
	if _, err := service.UpdateTask(context.Background(), actor(), "tsp_1", "tst_1", TaskUpdate{Done: ptr(true)}); !errors.Is(err, ErrPaused) {
		t.Errorf("paused tick: %v, want ErrPaused", err)
	}
	if err := service.DeleteTask(context.Background(), actor(), "tsp_1", "tst_1"); !errors.Is(err, ErrPaused) {
		t.Errorf("paused delete: %v, want ErrPaused", err)
	}

	managerOnly, _ := New(taskSpace(StatusActive), fakeDirectory{directory: teamDirectory(true, false)})
	if _, err := managerOnly.ListTasks(context.Background(), actor(), "tsp_1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a manager off the team: %v, want ErrForbidden", err)
	}
	outsider, _ := New(taskSpace(StatusActive), fakeDirectory{directory: teamDirectory(false, false)})
	if _, err := outsider.ListTasks(context.Background(), actor(), "tsp_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("somebody off the team: %v, want ErrNotFound", err)
	}
}

func TestAChangeToATeamspaceKeepsItsTaskCounts(t *testing.T) {
	repo := taskSpace(StatusActive)
	repo.spaces[0].TaskCounts = TaskCounts{Done: 1, Total: 3}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(true, true)})
	updated, err := service.Update(context.Background(), actor(), "tsp_1", UpdateInput{Name: ptr("Platform")})
	if err != nil || updated.TaskCounts != (TaskCounts{Done: 1, Total: 3}) {
		t.Fatalf("updated = %+v, %v", updated.TaskCounts, err)
	}
}
