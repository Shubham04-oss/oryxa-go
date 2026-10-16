package teamspaces

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const taskColumns = `id, teamspace_id, organization_id, title, owner_user_id, done, position,
	due_date, created_by_user_id, created_at, updated_at, done_at`

// ListTasks is a teamspace's checklist in order: by position, then by when
// each task was added.
func (store *Store) ListTasks(ctx context.Context, organizationID, teamspaceID string) ([]Task, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT `+taskColumns+`
		FROM agents_primary.teamspace_tasks
		WHERE organization_id=$1 AND teamspace_id=$2
		ORDER BY position, created_at, id`, organizationID, teamspaceID)
	if err != nil {
		return nil, fmt.Errorf("list teamspace tasks: %w", err)
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		var task Task
		if err := scanTask(rows, &task); err != nil {
			return nil, fmt.Errorf("scan teamspace task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (store *Store) GetTask(ctx context.Context, organizationID, teamspaceID, taskID string) (Task, error) {
	var task Task
	err := scanTask(store.database.Pool.QueryRow(ctx, `SELECT `+taskColumns+`
		FROM agents_primary.teamspace_tasks
		WHERE organization_id=$1 AND teamspace_id=$2 AND id=$3`, organizationID, teamspaceID, taskID), &task)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("get teamspace task: %w", err)
	}
	return task, nil
}

// CreateTask stores task. A Position below 0 puts it after every task the
// list holds; two added at once may share a position, and then the one added
// first comes first.
func (store *Store) CreateTask(ctx context.Context, task Task) (Task, error) {
	dueDate, err := dateOf(task.DueDate)
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	err = scanTask(store.database.Pool.QueryRow(ctx, `INSERT INTO agents_primary.teamspace_tasks
		(id, teamspace_id, organization_id, title, owner_user_id, done, position, due_date,
		 created_by_user_id, created_at, updated_at)
		SELECT $1, space.id, space.organization_id, $4, $5, false,
			CASE WHEN $6::integer >= 0 THEN $6::integer ELSE (
				SELECT COALESCE(MAX(position), -1) + 1 FROM agents_primary.teamspace_tasks WHERE teamspace_id=space.id
			) END, $7, $8, $9, $9
		FROM agents_primary.teamspaces space WHERE space.organization_id=$3 AND space.id=$2
		RETURNING `+taskColumns,
		task.ID, task.TeamspaceID, task.OrganizationID, task.Title, task.OwnerUserID, task.Position,
		dueDate, task.CreatedByUserID, now), &task)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("create teamspace task: %w", err)
	}
	return task, nil
}

func (store *Store) UpdateTask(ctx context.Context, organizationID, teamspaceID, taskID string, input TaskUpdate) (Task, error) {
	var result Task
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var current Task
		err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+`
			FROM agents_primary.teamspace_tasks
			WHERE organization_id=$1 AND teamspace_id=$2 AND id=$3 FOR UPDATE`, organizationID, teamspaceID, taskID), &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		current = applyTaskUpdate(current, input, time.Now().UTC())
		dueDate, err := dateOf(current.DueDate)
		if err != nil {
			return err
		}
		return scanTask(tx.QueryRow(ctx, `UPDATE agents_primary.teamspace_tasks SET
			title=$4, owner_user_id=$5, done=$6, position=$7, due_date=$8, updated_at=$9, done_at=$10
			WHERE organization_id=$1 AND teamspace_id=$2 AND id=$3 RETURNING `+taskColumns,
			organizationID, teamspaceID, taskID, current.Title, current.OwnerUserID, current.Done,
			current.Position, dueDate, current.UpdatedAt, current.DoneAt), &result)
	})
	if errors.Is(err, ErrNotFound) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("update teamspace task: %w", err)
	}
	return result, nil
}

func (store *Store) DeleteTask(ctx context.Context, organizationID, teamspaceID, taskID string) error {
	command, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspace_tasks
		WHERE organization_id=$1 AND teamspace_id=$2 AND id=$3`, organizationID, teamspaceID, taskID)
	if err != nil {
		return fmt.Errorf("delete teamspace task: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// forgetTaskOwner leaves, in tx, every task userID owns in the organization's
// teamspaces, or only in teamspaceID's when it is named, with no owner. The
// tasks stay: they are the team's.
func forgetTaskOwner(ctx context.Context, tx pgx.Tx, organizationID, teamspaceID, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE agents_primary.teamspace_tasks SET owner_user_id=NULL, updated_at=now()
		WHERE organization_id=$1 AND ($2='' OR teamspace_id=$2) AND owner_user_id=$3`, organizationID, teamspaceID, userID)
	return err
}

func scanTask(row scanner, task *Task) error {
	var dueDate pgtype.Date
	if err := row.Scan(&task.ID, &task.TeamspaceID, &task.OrganizationID, &task.Title, &task.OwnerUserID,
		&task.Done, &task.Position, &dueDate, &task.CreatedByUserID, &task.CreatedAt, &task.UpdatedAt, &task.DoneAt); err != nil {
		return err
	}
	task.DueDate = nil
	if dueDate.Valid {
		value := dueDate.Time.Format(time.DateOnly)
		task.DueDate = &value
	}
	return nil
}
