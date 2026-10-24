package agentworkflows

import (
	"context"
	"errors"
	"testing"
)

// failingUpdates fails every row update after the schedule was changed.
type failingUpdates struct{ *fakeRepository }

func (failingUpdates) Update(context.Context, Scope, string, UpdateInput) (Workflow, error) {
	return Workflow{}, errors.New("database unavailable")
}

// TestAFailedUpdateUndoesOnlyTheScheduleChangeItMade: when the row update
// fails, the external schedule is put back as it was, and left alone when
// the update never touched it.
func TestAFailedUpdateUndoesOnlyTheScheduleChangeItMade(t *testing.T) {
	weekly := &Schedule{Expression: "cron(0 9 ? * MON *)", Timezone: "Asia/Kolkata"}
	daily := &Schedule{Expression: "cron(0 9 * * ? *)", Timezone: "Asia/Kolkata"}
	renamed, disabled := "Renamed", StatusDisabled
	for name, test := range map[string]struct {
		existing         *Schedule
		input            UpdateInput
		upserts, deletes int
		restoredWith     *Schedule
	}{
		"rename, no schedule":       {input: UpdateInput{Name: &renamed}},
		"rename, with schedule":     {existing: weekly, input: UpdateInput{Name: &renamed}},
		"disable, no schedule":      {input: UpdateInput{Status: &disabled}},
		"add a schedule":            {input: UpdateInput{Schedule: daily}, upserts: 1, deletes: 1},
		"replace the schedule":      {existing: weekly, input: UpdateInput{Schedule: daily}, upserts: 2, restoredWith: weekly},
		"clear the schedule":        {existing: weekly, input: UpdateInput{ClearSchedule: true}, upserts: 1, deletes: 1, restoredWith: weekly},
		"disable with the schedule": {existing: weekly, input: UpdateInput{Status: &disabled}, upserts: 2, restoredWith: weekly},
	} {
		t.Run(name, func(t *testing.T) {
			service, repo, _, schedules, _ := workflowHarness(t)
			scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
			var existing *Schedule
			if test.existing != nil {
				copied := *test.existing
				existing = &copied
			}
			workflow, err := service.Create(context.Background(), scope, CreateInput{
				Name: "Pipeline", TeamID: "atm_1", Assignments: []Assignment{{AgentID: "agt_sales", Task: "Review"}}, Schedule: existing,
			})
			if err != nil {
				t.Fatal(err)
			}
			schedules.upserts, schedules.deletes = nil, nil
			service.repository = failingUpdates{repo}
			input := test.input
			if input.Schedule != nil {
				copied := *input.Schedule
				input.Schedule = &copied
			}
			if _, err := service.Update(context.Background(), scope, workflow.ID, input); err == nil {
				t.Fatal("the failed row update was not reported")
			}
			if len(schedules.upserts) != test.upserts || len(schedules.deletes) != test.deletes {
				t.Fatalf("upserts=%d deletes=%d, want %d %d", len(schedules.upserts), len(schedules.deletes), test.upserts, test.deletes)
			}
			if test.restoredWith != nil {
				last := schedules.upserts[len(schedules.upserts)-1]
				if last.Schedule == nil || last.Schedule.Expression != test.restoredWith.Expression || last.Status != StatusActive {
					t.Fatalf("restored with %+v", last)
				}
			}
		})
	}
}
