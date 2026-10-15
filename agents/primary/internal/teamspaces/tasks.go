package teamspaces

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

const maxTaskTitleRunes = 200

// Task is one item on a teamspace's checklist. Any member of the team may
// add one, change it or tick it; only whoever added it, or a manager of the
// team, may remove it.
type Task struct {
	ID             string `json:"id"`
	TeamspaceID    string `json:"teamspace_id"`
	OrganizationID string `json:"organization_id"`
	Title          string `json:"title"`
	// OwnerUserID is the member who has it, null when nobody does.
	OwnerUserID *string `json:"owner_user_id"`
	Done        bool    `json:"done"`
	Position    int     `json:"position"`
	// DueDate is the day it is due, as YYYY-MM-DD, or null.
	DueDate   *string   `json:"due_date"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// DoneAt is when it was ticked, null while it is not done.
	DoneAt *time.Time `json:"done_at"`
}

// TaskCounts is how much of a teamspace's checklist is done.
type TaskCounts struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

type TaskInput struct {
	Title       string  `json:"title"`
	OwnerUserID *string `json:"owner_user_id,omitempty"`
	DueDate     *string `json:"due_date,omitempty"`
	// Position, left out, puts the task last.
	Position *int `json:"position,omitempty"`
}

type TaskUpdate struct {
	Title    *string `json:"title,omitempty"`
	Done     *bool   `json:"done,omitempty"`
	Position *int    `json:"position,omitempty"`
	// OwnerUserID and DueDate are left alone when left out, and cleared when
	// sent as null.
	OwnerUserID Nullable `json:"owner_user_id,omitzero"`
	DueDate     Nullable `json:"due_date,omitzero"`
}

// ListTasks is the teamspace's checklist, in order, for a member of its team.
// A paused teamspace's checklist is still read.
func (service *Service) ListTasks(ctx context.Context, actor Actor, id string) ([]Task, error) {
	space, _, err := service.taskSpace(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	tasks, err := service.repository.ListTasks(ctx, actor.OrganizationID, space.ID)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []Task{}
	}
	return tasks, nil
}

func (service *Service) CreateTask(ctx context.Context, actor Actor, id string, input TaskInput) (Task, error) {
	space, directory, err := service.taskSpace(ctx, actor, id)
	if err != nil {
		return Task{}, err
	}
	if space.Status != StatusActive {
		return Task{}, ErrPaused
	}
	input = cleanTaskInput(input)
	if err := validateTaskTitle(input.Title); err != nil {
		return Task{}, err
	}
	if err := validateTaskPlacement(input.DueDate, input.Position); err != nil {
		return Task{}, err
	}
	if err := checkTaskOwner(directory, space.TeamID, input.OwnerUserID); err != nil {
		return Task{}, err
	}
	taskID, err := newID("tst_")
	if err != nil {
		return Task{}, err
	}
	position := -1
	if input.Position != nil {
		position = *input.Position
	}
	return service.repository.CreateTask(ctx, Task{
		ID: taskID, TeamspaceID: space.ID, OrganizationID: actor.OrganizationID, Title: input.Title,
		OwnerUserID: input.OwnerUserID, Position: position, DueDate: input.DueDate, CreatedBy: actor.UserID,
	})
}

func (service *Service) UpdateTask(ctx context.Context, actor Actor, id, taskID string, input TaskUpdate) (Task, error) {
	if input.Title == nil && input.Done == nil && input.Position == nil && !input.OwnerUserID.Set && !input.DueDate.Set {
		return Task{}, fmt.Errorf("%w: send at least one task field to change", ErrInvalid)
	}
	space, directory, err := service.taskSpace(ctx, actor, id)
	if err != nil {
		return Task{}, err
	}
	if space.Status != StatusActive {
		return Task{}, ErrPaused
	}
	input = cleanTaskUpdate(input)
	if input.Title != nil {
		if err := validateTaskTitle(*input.Title); err != nil {
			return Task{}, err
		}
	}
	if err := validateTaskPlacement(input.DueDate.Value, input.Position); err != nil {
		return Task{}, err
	}
	if err := checkTaskOwner(directory, space.TeamID, input.OwnerUserID.Value); err != nil {
		return Task{}, err
	}
	return service.repository.UpdateTask(ctx, actor.OrganizationID, space.ID, strings.TrimSpace(taskID), input)
}

func (service *Service) DeleteTask(ctx context.Context, actor Actor, id, taskID string) error {
	space, _, err := service.taskSpace(ctx, actor, id)
	if err != nil {
		return err
	}
	if space.Status != StatusActive {
		return ErrPaused
	}
	task, err := service.repository.GetTask(ctx, actor.OrganizationID, space.ID, strings.TrimSpace(taskID))
	if err != nil {
		return err
	}
	if task.CreatedBy != actor.UserID && !space.CanManage {
		return fmt.Errorf("%w: only whoever added a task, or a manager of the team, can remove it", ErrForbidden)
	}
	return service.repository.DeleteTask(ctx, actor.OrganizationID, space.ID, task.ID)
}

// taskSpace is the teamspace a checklist call is about, for a member of its
// team, and the directory it was resolved from, so an owner can be checked
// against the same membership without loading it again.
func (service *Service) taskSpace(ctx context.Context, actor Actor, id string) (Space, primaryagents.OrganizationDirectory, error) {
	if err := validateActor(actor); err != nil {
		return Space{}, primaryagents.OrganizationDirectory{}, err
	}
	space, err := service.repository.Get(ctx, actor.OrganizationID, strings.TrimSpace(id))
	if err != nil {
		return Space{}, primaryagents.OrganizationDirectory{}, err
	}
	directory, err := service.directory.Load(ctx, actor.OrganizationID, actor.AccessToken)
	if err != nil {
		return Space{}, primaryagents.OrganizationDirectory{}, fmt.Errorf("load organization directory: %w", err)
	}
	access, err := accessIn(directory, actor.UserID, space.TeamID)
	if err != nil {
		return Space{}, primaryagents.OrganizationDirectory{}, err
	}
	if !access.isMember && !access.canManage {
		return Space{}, primaryagents.OrganizationDirectory{}, ErrNotFound
	}
	if !access.isMember {
		return Space{}, primaryagents.OrganizationDirectory{}, fmt.Errorf("%w: only a current team member can use the checklist", ErrForbidden)
	}
	return decorate(space, access), directory, nil
}

// checkTaskOwner refuses an owner who is not on the team today.
func checkTaskOwner(directory primaryagents.OrganizationDirectory, teamID string, ownerUserID *string) error {
	if ownerUserID == nil {
		return nil
	}
	for _, person := range directory.People {
		if person.UserID != *ownerUserID {
			continue
		}
		for _, team := range person.Teams {
			if team.ID == teamID {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: owner_user_id must be a member of the team", ErrInvalid)
}

func cleanTaskInput(input TaskInput) TaskInput {
	input.Title = cleanLine(input.Title)
	if input.OwnerUserID != nil {
		input.OwnerUserID = nonEmpty(strings.TrimSpace(*input.OwnerUserID))
	}
	if input.DueDate != nil {
		input.DueDate = nonEmpty(strings.TrimSpace(*input.DueDate))
	}
	return input
}

func cleanTaskUpdate(input TaskUpdate) TaskUpdate {
	if input.Title != nil {
		value := cleanLine(*input.Title)
		input.Title = &value
	}
	if input.OwnerUserID.Value != nil {
		input.OwnerUserID.Value = nonEmpty(strings.TrimSpace(*input.OwnerUserID.Value))
	}
	if input.DueDate.Value != nil {
		input.DueDate.Value = nonEmpty(strings.TrimSpace(*input.DueDate.Value))
	}
	return input
}

func validateTaskTitle(title string) error {
	switch {
	case title == "":
		return fmt.Errorf("%w: title is required", ErrInvalid)
	case utf8.RuneCountInString(title) > maxTaskTitleRunes:
		return fmt.Errorf("%w: title holds at most %d characters", ErrInvalid, maxTaskTitleRunes)
	}
	return nil
}

// validateTaskPlacement checks where a task sits: its place in the list and
// the day it is due.
func validateTaskPlacement(dueDate *string, position *int) error {
	if position != nil && *position < 0 {
		return fmt.Errorf("%w: position must be zero or more", ErrInvalid)
	}
	if dueDate != nil {
		if _, err := time.Parse(time.DateOnly, *dueDate); err != nil {
			return fmt.Errorf("%w: due_date must be a date written YYYY-MM-DD", ErrInvalid)
		}
	}
	return nil
}

// applyTaskUpdate is task with input's changes, made at at. Ticking a task
// records when; unticking clears it, and ticking a done task again keeps the
// first time.
func applyTaskUpdate(task Task, input TaskUpdate, at time.Time) Task {
	if input.Title != nil {
		task.Title = *input.Title
	}
	if input.Position != nil {
		task.Position = *input.Position
	}
	if input.OwnerUserID.Set {
		task.OwnerUserID = input.OwnerUserID.Value
	}
	if input.DueDate.Set {
		task.DueDate = input.DueDate.Value
	}
	if input.Done != nil && *input.Done != task.Done {
		task.Done = *input.Done
		task.DoneAt = nil
		if task.Done {
			task.DoneAt = &at
		}
	}
	task.UpdatedAt = at
	return task
}
