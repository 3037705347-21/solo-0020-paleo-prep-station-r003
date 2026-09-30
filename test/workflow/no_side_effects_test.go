package workflow_test

import (
	"net/http"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
)

// repoSnapshot is the complete externally observable state through the public
// read endpoints. It is compared before and after rejected requests to prove
// the repository commit boundary rolled everything back (no half-state).
type repoSnapshot struct {
	specimens map[string]specimenDTO
	plans     map[string]planSnapshotDTO
}

func snapshotRepository(t *testing.T, c *apiClient) repoSnapshot {
	t.Helper()
	shot := repoSnapshot{
		specimens: map[string]specimenDTO{},
		plans:     map[string]planSnapshotDTO{},
	}
	for _, specimen := range listSpecimens(c) {
		shot.specimens[specimen.ID] = getSpecimen(c, specimen.ID)
		for _, plan := range listPlans(c, specimen.ID) {
			shot.plans[plan.ID] = getPlanSnapshot(c, plan.ID)
		}
	}
	return shot
}

// TestRejectedRequestsLeaveNoHalfState exercises every mutating boundary with
// requests that must be rejected at different stages of a populated workflow,
// and asserts that no entity, collection member, timestamp or version changes.
func TestRejectedRequestsLeaveNoHalfState(t *testing.T) {
	t.Parallel()
	c := newServer(t)

	// Populate two specimens: one preparing with an active plan and history,
	// one stabilized with a completed plan.
	first := registerSpecimen(c, "PPS-SIDE-PREP")
	firstPlan := createPlan(c, first.ID, 80)
	approvePlan(c, firstPlan.ID, "curator-lin")
	logProgress(c, firstPlan.ID, 10, "初次清理")
	logProgress(c, firstPlan.ID, 45, "主体暴露")

	second, secondPlan := stabilizeSpecimen(c, "PPS-SIDE-STABLE")

	// A fresh specimen with no plan is the only state where plan field
	// validation (400) is reachable before any state guard fires.
	third := registerSpecimen(c, "PPS-SIDE-FRESH")

	before := snapshotRepository(t, c)

	// Registration boundary: invalid, duplicate, malformed.
	c.expectError(http.StatusBadRequest, "required_field", http.MethodPost, "/specimens",
		contracts.RegisterSpecimenRequest{CatalogNumber: "", FieldLabel: "x", MatrixKind: "粉砂岩"})
	c.expectError(http.StatusConflict, "duplicate_catalog_number", http.MethodPost, "/specimens",
		contracts.RegisterSpecimenRequest{CatalogNumber: "pps-side-prep", FieldLabel: "x", MatrixKind: "粉砂岩"})
	c.expectError(http.StatusBadRequest, "invalid_catalog_number", http.MethodPost, "/specimens",
		contracts.RegisterSpecimenRequest{CatalogNumber: "bad/catalog", FieldLabel: "x", MatrixKind: "粉砂岩"})
	status, _ := c.sendRaw(http.MethodPost, "/specimens", `{not-json`)
	if status != http.StatusBadRequest {
		t.Fatalf("malformed registration should be 400, got %d", status)
	}

	// Plan creation boundary: open-plan mutex, illegal exposure, bad fields,
	// unplannable specimen, unknown specimen.
	c.expectError(http.StatusConflict, "open_plan_exists",
		http.MethodPost, "/specimens/"+first.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "o", Method: "m", TargetExposure: 50})
	c.expectError(http.StatusBadRequest, "invalid_target_exposure",
		http.MethodPost, "/specimens/"+third.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "o", Method: "m", TargetExposure: 0})
	c.expectError(http.StatusBadRequest, "required_field",
		http.MethodPost, "/specimens/"+third.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "", Method: "m", TargetExposure: 50})
	c.expectError(http.StatusConflict, "specimen_not_plannable",
		http.MethodPost, "/specimens/"+second.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "o", Method: "m", TargetExposure: 50})
	c.expectError(http.StatusNotFound, "specimen_not_found",
		http.MethodPost, "/specimens/ghost/plans",
		contracts.CreatePlanRequest{Objective: "o", Method: "m", TargetExposure: 50})

	// Approval boundary: non-draft plan, missing approver, unknown plan.
	c.expectError(http.StatusConflict, "plan_not_approvable",
		http.MethodPost, "/plans/"+firstPlan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-wang"})
	c.expectError(http.StatusConflict, "plan_not_approvable",
		http.MethodPost, "/plans/"+secondPlan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-wang"})
	c.expectError(http.StatusNotFound, "plan_not_found",
		http.MethodPost, "/plans/ghost/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-wang"})

	// Progress boundary: regression, out of range, bad fields, closed plan,
	// unknown plan.
	c.expectError(http.StatusConflict, "progress_regressed",
		http.MethodPost, "/plans/"+firstPlan.ID+"/progress",
		contracts.ProgressRequest{Stage: "回退", Percent: 44, RecordedBy: "preparator-chen"})
	c.expectError(http.StatusBadRequest, "invalid_percent",
		http.MethodPost, "/plans/"+firstPlan.ID+"/progress",
		contracts.ProgressRequest{Stage: "越界", Percent: 101, RecordedBy: "preparator-chen"})
	c.expectError(http.StatusBadRequest, "required_field",
		http.MethodPost, "/plans/"+firstPlan.ID+"/progress",
		contracts.ProgressRequest{Stage: "  ", Percent: 46, RecordedBy: "preparator-chen"})
	c.expectError(http.StatusConflict, "plan_not_recordable",
		http.MethodPost, "/plans/"+secondPlan.ID+"/progress",
		contracts.ProgressRequest{Stage: "追加", Percent: 100, RecordedBy: "preparator-chen"})
	c.expectError(http.StatusNotFound, "plan_not_found",
		http.MethodPost, "/plans/ghost/progress",
		contracts.ProgressRequest{Stage: "追加", Percent: 1, RecordedBy: "preparator-chen"})

	// Release boundary: open plan, invalid payload, unknown specimen.
	c.expectError(http.StatusConflict, "open_plan_blocks_release",
		http.MethodPost, "/specimens/"+first.ID+"/release",
		contracts.ReleaseRequest{Purpose: "p", Recipient: "r", ApprovedBy: "a"})
	c.expectError(http.StatusBadRequest, "required_field",
		http.MethodPost, "/specimens/"+second.ID+"/release",
		contracts.ReleaseRequest{Purpose: " ", Recipient: "r", ApprovedBy: "a"})
	c.expectError(http.StatusNotFound, "specimen_not_found",
		http.MethodPost, "/specimens/ghost/release",
		contracts.ReleaseRequest{Purpose: "p", Recipient: "r", ApprovedBy: "a"})

	after := snapshotRepository(t, c)
	assertSnapshotsEqual(t, before, after)

	// The workflow must remain fully usable: the active plan can continue and
	// complete, and the stabilized specimen can then be released.
	logProgress(c, firstPlan.ID, 100, "表面加固")
	releaseSpecimen(c, second.ID)
}

func assertSnapshotsEqual(t *testing.T, before, after repoSnapshot) {
	t.Helper()
	if len(before.specimens) != len(after.specimens) {
		t.Fatalf("specimen count changed: before=%d after=%d", len(before.specimens), len(after.specimens))
	}
	if len(before.plans) != len(after.plans) {
		t.Fatalf("plan count changed: before=%d after=%d", len(before.plans), len(after.plans))
	}
	for id, want := range before.specimens {
		got, ok := after.specimens[id]
		if !ok {
			t.Fatalf("specimen %s vanished after rejected requests", id)
		}
		if got != want {
			t.Fatalf("specimen %s changed after rejected requests:\n before=%+v\n after =%+v", id, want, got)
		}
	}
	for id, want := range after.plans {
		got, ok := before.plans[id]
		if !ok {
			t.Fatalf("plan %s appeared after rejected requests", id)
		}
		if got.Plan != want.Plan {
			t.Fatalf("plan %s changed after rejected requests:\n before=%+v\n after =%+v", id, want.Plan, got.Plan)
		}
		if len(got.Progress) != len(want.Progress) {
			t.Fatalf("plan %s progress length changed: before=%d after=%d",
				id, len(want.Progress), len(got.Progress))
		}
		for i := range want.Progress {
			if got.Progress[i] != want.Progress[i] {
				t.Fatalf("plan %s progress[%d] changed:\n before=%+v\n after =%+v",
					id, i, want.Progress[i], got.Progress[i])
			}
		}
	}
}
