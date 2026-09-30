package workflow_test

import (
	"net/http"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
)

// TestTargetExposureBoundaries: 1 and 100 are accepted; 0 and values above 100
// are invalid. A rejected plan creation must not move the specimen or leave a
// plan behind.
func TestTargetExposureBoundaries(t *testing.T) {
	t.Parallel()

	for _, exposure := range []int{1, 100} {
		t.Run("accepted/"+itoa(exposure), func(t *testing.T) {
			t.Parallel()
			c := newServer(t)
			specimen := registerSpecimen(c, "PPS-BND-"+itoa(exposure))
			plan := createPlan(c, specimen.ID, exposure)
			if plan.Status != "draft" || plan.Version != 1 || plan.TargetExposure != exposure {
				t.Fatalf("plan with exposure %d should be stored as draft v1, got %+v", exposure, plan)
			}
			stored := getSpecimen(c, specimen.ID)
			if stored.Status != "planned" || stored.Version != specimen.Version+1 {
				t.Fatalf("specimen should become planned (version %d) on plan creation, got status=%q version=%d",
					specimen.Version+1, stored.Status, stored.Version)
			}
		})
	}

	rejected := []struct {
		name     string
		exposure int
	}{
		{"zero", 0},
		{"negative", -1},
		{"above one hundred", 101},
	}
	for _, tc := range rejected {
		t.Run("rejected/"+tc.name, func(t *testing.T) {
			t.Parallel()
			c := newServer(t)
			specimen := registerSpecimen(c, "PPS-BNDX-"+itoa(tc.exposure))
			body := contracts.CreatePlanRequest{
				Objective:      "暴露主要关节面",
				Method:         "手工显微剥离",
				TargetExposure: tc.exposure,
			}
			path := "/specimens/" + specimen.ID + "/plans"
			c.expectError(http.StatusBadRequest, "invalid_target_exposure", http.MethodPost, path, body)

			stored := getSpecimen(c, specimen.ID)
			if stored.Status != "registered" || stored.Version != 1 {
				t.Fatalf("rejected plan creation must leave specimen registered v1, got status=%q version=%d",
					stored.Status, stored.Version)
			}
			if plans := listPlans(c, specimen.ID); len(plans) != 0 {
				t.Fatalf("rejected plan creation must leave no plans, got %d", len(plans))
			}
		})
	}
}

func TestPlanCreationRequiresValidPlanFields(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-PLANV-1")

	body := contracts.CreatePlanRequest{Objective: "  ", Method: "手工显微剥离", TargetExposure: 50}
	path := "/specimens/" + specimen.ID + "/plans"
	c.expectError(http.StatusBadRequest, "required_field", http.MethodPost, path, body)

	stored := getSpecimen(c, specimen.ID)
	if stored.Status != "registered" || stored.Version != 1 {
		t.Fatalf("invalid plan fields must leave the specimen untouched, got status=%q version=%d",
			stored.Status, stored.Version)
	}
}

// TestOpenPlanMutualExclusion: while one plan is open (draft/approved/active),
// creating a second plan is rejected in every open state, and the rejection
// never alters the existing plan, the specimen or the plan collection.
func TestOpenPlanMutualExclusion(t *testing.T) {
	t.Parallel()

	openStates := []struct {
		name string
		open func(*apiClient, specimenDTO, planDTO)
	}{
		{
			name: "draft",
			open: func(c *apiClient, _ specimenDTO, _ planDTO) {},
		},
		{
			name: "approved",
			open: func(c *apiClient, _ specimenDTO, p planDTO) {
				approvePlan(c, p.ID, "curator-lin")
			},
		},
		{
			name: "active",
			open: func(c *apiClient, _ specimenDTO, p planDTO) {
				approvePlan(c, p.ID, "curator-lin")
				logProgress(c, p.ID, 40, "矩阵剥离")
			},
		},
	}
	for _, state := range openStates {
		t.Run(state.name, func(t *testing.T) {
			t.Parallel()
			c := newServer(t)
			specimen := registerSpecimen(c, "PPS-MUTEX-"+state.name)
			first := createPlan(c, specimen.ID, 80)
			state.open(c, specimen, first)

			beforeSpecimen := getSpecimen(c, specimen.ID)
			beforePlan := getPlanSnapshot(c, first.ID).Plan
			beforeProgress := getPlanSnapshot(c, first.ID).Progress

			body := contracts.CreatePlanRequest{
				Objective:      "第二套目标",
				Method:         "机械清理",
				TargetExposure: 60,
			}
			path := "/specimens/" + specimen.ID + "/plans"
			c.expectError(http.StatusConflict, "open_plan_exists", http.MethodPost, path, body)

			afterSpecimen := getSpecimen(c, specimen.ID)
			if afterSpecimen != beforeSpecimen {
				t.Fatalf("rejected second plan must not change specimen: before=%+v after=%+v",
					beforeSpecimen, afterSpecimen)
			}
			snapshot := getPlanSnapshot(c, first.ID)
			if snapshot.Plan != beforePlan {
				t.Fatalf("rejected second plan must not change the open plan: before=%+v after=%+v",
					beforePlan, snapshot.Plan)
			}
			if len(snapshot.Progress) != len(beforeProgress) {
				t.Fatalf("rejected second plan must not add progress, got %d want %d",
					len(snapshot.Progress), len(beforeProgress))
			}
			if plans := listPlans(c, specimen.ID); len(plans) != 1 || plans[0].ID != first.ID {
				t.Fatalf("rejected second plan must leave exactly one plan, got %+v", plans)
			}
		})
	}
}

// TestPlanCreationOnUnplannableSpecimen: once preparation moves beyond the
// planned stage the specimen cannot host a fresh plan.
func TestPlanCreationOnUnplannableSpecimen(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen, plan := stabilizeSpecimen(c, "PPS-STABLE-PLAN")

	body := contracts.CreatePlanRequest{Objective: "新增目标", Method: "机械清理", TargetExposure: 50}
	path := "/specimens/" + specimen.ID + "/plans"
	c.expectError(http.StatusConflict, "specimen_not_plannable", http.MethodPost, path, body)

	snapshot := getPlanSnapshot(c, plan.ID)
	if snapshot.Plan.Status != "completed" {
		t.Fatalf("rejected plan creation must not alter the completed plan, got %q", snapshot.Plan.Status)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "stabilized" {
		t.Fatalf("rejected plan creation must not change specimen status, got %q", stored.Status)
	}
}

func TestPlanCreationForUnknownSpecimenReturns404(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	body := contracts.CreatePlanRequest{Objective: "目标", Method: "方法", TargetExposure: 50}
	c.expectError(http.StatusNotFound, "specimen_not_found",
		http.MethodPost, "/specimens/missing/plans", body)
}

// TestOnlyDraftCanBeApproved: approving an approved, active or completed plan
// must be rejected, with approver and timestamp written exactly once.
func TestOnlyDraftCanBeApproved(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-APPROVE-1")
	plan := createPlan(c, specimen.ID, 80)
	approved := approvePlan(c, plan.ID, "curator-lin")

	if approved.Status != "approved" || approved.Version != 2 {
		t.Fatalf("approval should move draft v1 to approved v2, got status=%q version=%d",
			approved.Status, approved.Version)
	}
	if approved.ApprovedBy != "curator-lin" || approved.ApprovedAt == "" {
		t.Fatalf("approval must record approver and approval time together, got %+v", approved)
	}
	storedSpecimen := getSpecimen(c, specimen.ID)
	if storedSpecimen.Status != "preparing" || storedSpecimen.Version != 3 {
		t.Fatalf("approval should move specimen to preparing v3, got status=%q version=%d",
			storedSpecimen.Status, storedSpecimen.Version)
	}

	// Re-approving the already approved plan is a conflict and changes nothing.
	repeat := contracts.ApprovalRequest{ApprovedBy: "curator-wang"}
	c.expectError(http.StatusConflict, "plan_not_approvable",
		http.MethodPost, "/plans/"+plan.ID+"/approval", repeat)
	again := getPlanSnapshot(c, plan.ID).Plan
	if again.ApprovedBy != "curator-lin" || again.Version != 2 {
		t.Fatalf("rejected re-approval must not overwrite approver or bump version, got %+v", again)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" || stored.Version != 3 {
		t.Fatalf("rejected re-approval must leave specimen preparing v3, got %+v", stored)
	}

	// Approval remains forbidden after progress starts.
	logProgress(c, plan.ID, 30, "矩阵剥离")
	c.expectError(http.StatusConflict, "plan_not_approvable",
		http.MethodPost, "/plans/"+plan.ID+"/approval", repeat)
	active := getPlanSnapshot(c, plan.ID).Plan
	if active.Status != "active" {
		t.Fatalf("expected active plan, got %q", active.Status)
	}

	// Approval remains forbidden after completion.
	logProgress(c, plan.ID, 100, "表面加固")
	c.expectError(http.StatusConflict, "plan_not_approvable",
		http.MethodPost, "/plans/"+plan.ID+"/approval", repeat)
	completed := getPlanSnapshot(c, plan.ID).Plan
	if completed.Status != "completed" || completed.Version != 4 {
		t.Fatalf("plan should end completed v4, got status=%q version=%d", completed.Status, completed.Version)
	}
}

func TestApprovalRequiresApproverAndHasNoSideEffects(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-APPROVE-2")
	plan := createPlan(c, specimen.ID, 80)

	c.expectError(http.StatusBadRequest, "required_field",
		http.MethodPost, "/plans/"+plan.ID+"/approval", contracts.ApprovalRequest{ApprovedBy: "   "})

	got := getPlanSnapshot(c, plan.ID)
	if got.Plan.Status != "draft" || got.Plan.Version != 1 || got.Plan.ApprovedBy != "" || got.Plan.ApprovedAt != "" {
		t.Fatalf("rejected approval must leave the draft untouched, got %+v", got.Plan)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "planned" || stored.Version != 2 {
		t.Fatalf("rejected approval must leave specimen planned v2, got %+v", stored)
	}
}

func TestApprovalOfUnknownPlanReturns404(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	c.expectError(http.StatusNotFound, "plan_not_found",
		http.MethodPost, "/plans/missing/approval", contracts.ApprovalRequest{ApprovedBy: "curator-lin"})
}
