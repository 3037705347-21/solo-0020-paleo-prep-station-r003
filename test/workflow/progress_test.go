package workflow_test

import (
	"net/http"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
)

// approvedPlan registers a specimen, creates and approves a plan, and returns
// the specimen and approved plan for progress tests.
func approvedPlan(t *testing.T, catalog string) (*apiClient, specimenDTO, planDTO) {
	t.Helper()
	c := newServer(t)
	specimen := registerSpecimen(c, catalog)
	plan := createPlan(c, specimen.ID, 100)
	approvePlan(c, plan.ID, "curator-lin")
	return c, specimen, plan
}

// TestProgressRequiresRecordablePlan: draft and completed plans must reject
// progress writes.
func TestProgressRequiresRecordablePlan(t *testing.T) {
	t.Parallel()

	t.Run("draft plan rejects progress", func(t *testing.T) {
		t.Parallel()
		c := newServer(t)
		specimen := registerSpecimen(c, "PPS-PROG-DRAFT")
		plan := createPlan(c, specimen.ID, 80)

		c.expectError(http.StatusConflict, "plan_not_recordable",
			http.MethodPost, "/plans/"+plan.ID+"/progress",
			contracts.ProgressRequest{Stage: "清理", Percent: 10, RecordedBy: "preparator-chen"})

		snapshot := getPlanSnapshot(c, plan.ID)
		if snapshot.Plan.Status != "draft" || snapshot.Plan.Version != 1 {
			t.Fatalf("rejected progress must leave plan draft v1, got %+v", snapshot.Plan)
		}
		if len(snapshot.Progress) != 0 {
			t.Fatalf("rejected progress must not append an entry, got %d", len(snapshot.Progress))
		}
		if stored := getSpecimen(c, specimen.ID); stored.Status != "planned" || stored.Version != 2 {
			t.Fatalf("rejected progress must leave specimen planned v2, got %+v", stored)
		}
	})

	t.Run("completed plan rejects progress", func(t *testing.T) {
		t.Parallel()
		c, specimen, plan := approvedPlan(t, "PPS-PROG-DONE")
		logProgress(c, plan.ID, 100, "表面加固")

		c.expectError(http.StatusConflict, "plan_not_recordable",
			http.MethodPost, "/plans/"+plan.ID+"/progress",
			contracts.ProgressRequest{Stage: "多余清理", Percent: 100, RecordedBy: "preparator-chen"})

		snapshot := getPlanSnapshot(c, plan.ID)
		if len(snapshot.Progress) != 1 || snapshot.Plan.Status != "completed" {
			t.Fatalf("completed plan must not accept more entries, got %+v", snapshot)
		}
		if stored := getSpecimen(c, specimen.ID); stored.Status != "stabilized" {
			t.Fatalf("stabilized specimen must not change on rejected progress, got %q", stored.Status)
		}
	})
}

// TestProgressPercentBoundaries: 0 and 100 are valid, negatives and values
// above 100 are invalid. The first entry transitions approved -> active.
func TestProgressPercentBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("zero percent is valid and activates the plan", func(t *testing.T) {
		t.Parallel()
		c, specimen, plan := approvedPlan(t, "PPS-PCT-ZERO")
		entry := logProgress(c, plan.ID, 0, "初始评估")
		if entry.Sequence != 1 || entry.Percent != 0 {
			t.Fatalf("first entry should be sequence 1 at 0%%, got %+v", entry)
		}
		snapshot := getPlanSnapshot(c, plan.ID)
		if snapshot.Plan.Status != "active" || snapshot.Plan.Version != 3 {
			t.Fatalf("first entry should move approved v2 to active v3, got status=%q version=%d",
				snapshot.Plan.Status, snapshot.Plan.Version)
		}
		if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" {
			t.Fatalf("0%% entry keeps the specimen preparing, got %q", stored.Status)
		}
	})

	t.Run("one hundred is valid; out-of-range values are rejected", func(t *testing.T) {
		t.Parallel()
		for _, percent := range []int{-1, 101} {
			c, specimen, plan := approvedPlan(t, "PPS-PCT-"+itoa(percent))
			body := contracts.ProgressRequest{
				Stage:      "非法比例",
				Percent:    percent,
				RecordedBy: "preparator-chen",
			}
			c.expectError(http.StatusBadRequest, "invalid_percent",
				http.MethodPost, "/plans/"+plan.ID+"/progress", body)

			snapshot := getPlanSnapshot(c, plan.ID)
			if len(snapshot.Progress) != 0 {
				t.Fatalf("rejected percent %d must not append an entry, got %+v", percent, snapshot.Progress)
			}
			if snapshot.Plan.Status != "approved" || snapshot.Plan.Version != 2 {
				t.Fatalf("rejected percent %d must leave plan approved v2, got status=%q version=%d",
					percent, snapshot.Plan.Status, snapshot.Plan.Version)
			}
			if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" || stored.Version != 3 {
				t.Fatalf("rejected percent %d must leave specimen preparing v3, got %+v", percent, stored)
			}
		}
	})
}

// TestProgressNeverRegresses: a percentage below the previous entry is rejected
// (equal is allowed), and neither sequence numbers nor versions move.
func TestProgressNeverRegresses(t *testing.T) {
	t.Parallel()
	c, specimen, plan := approvedPlan(t, "PPS-REGRESS-1")
	logProgress(c, plan.ID, 20, "初次清理")
	logProgress(c, plan.ID, 60, "主体暴露")

	before := getPlanSnapshot(c, plan.ID)

	c.expectError(http.StatusConflict, "progress_regressed",
		http.MethodPost, "/plans/"+plan.ID+"/progress",
		contracts.ProgressRequest{Stage: "回退操作", Percent: 59, RecordedBy: "preparator-chen"})

	after := getPlanSnapshot(c, plan.ID)
	if len(after.Progress) != len(before.Progress) {
		t.Fatalf("rejected regressed entry must not be appended, got %d want %d",
			len(after.Progress), len(before.Progress))
	}
	if after.Plan.Version != before.Plan.Version || after.Plan.Status != before.Plan.Status {
		t.Fatalf("rejected regressed entry must not change the plan, before=%+v after=%+v",
			before.Plan, after.Plan)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Version != specimen.Version+2 {
		t.Fatalf("rejected regressed entry must not change the specimen version, got %d", stored.Version)
	}

	// Equal percentage is permitted and continues the sequence.
	equal := logProgress(c, plan.ID, 60, "精细修整")
	if equal.Sequence != 3 || equal.Percent != 60 {
		t.Fatalf("equal percentage should be accepted as sequence 3, got %+v", equal)
	}
}

// TestProgressSequenceIsContinuous: accepted entries carry 1-based consecutive
// sequence numbers in submission order.
func TestProgressSequenceIsContinuous(t *testing.T) {
	t.Parallel()
	c, _, plan := approvedPlan(t, "PPS-SEQ-1")

	percents := []int{0, 10, 25, 25, 80}
	for i, percent := range percents {
		entry := logProgress(c, plan.ID, percent, "阶段-"+itoa(i+1))
		if entry.Sequence != i+1 {
			t.Fatalf("entry %d should carry sequence %d, got %d", i+1, i+1, entry.Sequence)
		}
		if entry.PlanID != plan.ID {
			t.Fatalf("entry must reference its plan %s, got %s", plan.ID, entry.PlanID)
		}
	}
	snapshot := getPlanSnapshot(c, plan.ID)
	for i, entry := range snapshot.Progress {
		if entry.Sequence != i+1 {
			t.Fatalf("stored progress sequence must be consecutive, position %d got %d", i, entry.Sequence)
		}
		if entry.Percent != percents[i] {
			t.Fatalf("stored percent at position %d = %d, want %d", i, entry.Percent, percents[i])
		}
	}
}

// TestProgressRequiresRequiredFields: missing stage or recorder is a 400 with
// no state change, and does not consume a sequence number.
func TestProgressRequiresRequiredFields(t *testing.T) {
	t.Parallel()
	c, specimen, plan := approvedPlan(t, "PPS-PROG-FIELDS")
	logProgress(c, plan.ID, 30, "初次清理")

	invalid := []struct {
		name string
		body contracts.ProgressRequest
		code string
	}{
		{
			name: "missing stage",
			body: contracts.ProgressRequest{Stage: "  ", Percent: 40, RecordedBy: "preparator-chen"},
			code: "required_field",
		},
		{
			name: "missing recorder",
			body: contracts.ProgressRequest{Stage: "补充清理", Percent: 40, RecordedBy: ""},
			code: "required_field",
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			c.expectError(http.StatusBadRequest, tc.code,
				http.MethodPost, "/plans/"+plan.ID+"/progress", tc.body)
			snapshot := getPlanSnapshot(c, plan.ID)
			if len(snapshot.Progress) != 1 || snapshot.Progress[0].Sequence != 1 {
				t.Fatalf("rejected entry must not consume a sequence, got %+v", snapshot.Progress)
			}
			if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" {
				t.Fatalf("rejected entry must leave specimen preparing, got %q", stored.Status)
			}
		})
	}
}

// TestOneHundredPercentCompletesPlanAndStabilizesSpecimenAtomically: the last
// progress entry at 100% is returned, the plan is completed and the specimen is
// stabilized in one commit; afterwards no further progress is accepted.
func TestOneHundredPercentCompletesPlanAndStabilizesSpecimenAtomically(t *testing.T) {
	t.Parallel()

	t.Run("after earlier partial entries", func(t *testing.T) {
		t.Parallel()
		c, specimen, plan := approvedPlan(t, "PPS-FULL-1")
		logProgress(c, plan.ID, 35, "矩阵剥离")
		final := logProgress(c, plan.ID, 100, "表面加固")
		if final.Sequence != 2 || final.Percent != 100 {
			t.Fatalf("final entry should be sequence 2 at 100%%, got %+v", final)
		}

		snapshot := getPlanSnapshot(c, plan.ID)
		if snapshot.Plan.Status != "completed" {
			t.Fatalf("plan should be completed at 100%%, got %q", snapshot.Plan.Status)
		}
		if len(snapshot.Progress) != 2 || snapshot.Progress[1].Percent != 100 {
			t.Fatalf("completed plan must keep exactly two entries ending at 100, got %+v", snapshot.Progress)
		}
		stored := getSpecimen(c, specimen.ID)
		if stored.Status != "stabilized" {
			t.Fatalf("specimen should be stabilized together with plan completion, got %q", stored.Status)
		}
		// A plan completed immediately from approved with a single 100 entry.
	})

	t.Run("directly from approved with a single entry", func(t *testing.T) {
		t.Parallel()
		c, specimen, plan := approvedPlan(t, "PPS-FULL-2")
		final := logProgress(c, plan.ID, 100, "一次性完成")
		if final.Sequence != 1 {
			t.Fatalf("single 100%% entry should be sequence 1, got %+v", final)
		}
		snapshot := getPlanSnapshot(c, plan.ID)
		if snapshot.Plan.Status != "completed" || snapshot.Plan.Version != 4 {
			t.Fatalf("approved v2 should pass active then complete in one commit, ending completed v4: got status=%q version=%d",
				snapshot.Plan.Status, snapshot.Plan.Version)
		}
		stored := getSpecimen(c, specimen.ID)
		if stored.Status != "stabilized" {
			t.Fatalf("specimen should be stabilized on single 100%% entry, got %q", stored.Status)
		}
	})
}

// TestInvalidFinalEntryLeavesNeitherSideTerminal: when a 100% entry is itself
// invalid, neither the plan nor the specimen may reach a terminal state.
func TestInvalidFinalEntryLeavesNeitherSideTerminal(t *testing.T) {
	t.Parallel()
	c, specimen, plan := approvedPlan(t, "PPS-FULL-INVALID")
	logProgress(c, plan.ID, 80, "主体暴露")

	// 100% but recorder missing: validation fails before completion.
	body := contracts.ProgressRequest{Stage: "表面加固", Percent: 100, RecordedBy: "  "}
	c.expectError(http.StatusBadRequest, "required_field",
		http.MethodPost, "/plans/"+plan.ID+"/progress", body)

	snapshot := getPlanSnapshot(c, plan.ID)
	if snapshot.Plan.Status != "active" {
		t.Fatalf("invalid final entry must not complete the plan, got %q", snapshot.Plan.Status)
	}
	if len(snapshot.Progress) != 1 {
		t.Fatalf("invalid final entry must not append, got %d entries", len(snapshot.Progress))
	}
	stored := getSpecimen(c, specimen.ID)
	if stored.Status != "preparing" {
		t.Fatalf("invalid final entry must not stabilize the specimen, got %q", stored.Status)
	}

	// A valid completion is still possible afterwards, proving no half-state.
	logProgress(c, plan.ID, 100, "表面加固")
	if got := getPlanSnapshot(c, plan.ID); got.Plan.Status != "completed" {
		t.Fatalf("plan should still be completable, got %q", got.Plan.Status)
	}
	if got := getSpecimen(c, specimen.ID); got.Status != "stabilized" {
		t.Fatalf("specimen should become stabilized after valid completion, got %q", got.Status)
	}
}

func TestProgressForUnknownPlanReturns404(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	c.expectError(http.StatusNotFound, "plan_not_found",
		http.MethodPost, "/plans/missing/progress",
		contracts.ProgressRequest{Stage: "清理", Percent: 1, RecordedBy: "preparator-chen"})
}
