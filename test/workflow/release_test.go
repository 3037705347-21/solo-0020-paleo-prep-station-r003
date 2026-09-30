package workflow_test

import (
	"net/http"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
)

func releaseRequest() contracts.ReleaseRequest {
	return contracts.ReleaseRequest{
		Purpose:    "比较解剖研究",
		Recipient:  "地层研究组",
		ApprovedBy: "keeper-zhou",
	}
}

// TestReleaseRequiresStableSpecimenWithoutOpenPlan: release is rejected while
// the specimen is registered, planned or preparing (which also always has an
// open plan), and the rejection leaves no release record.
func TestReleaseRequiresStableSpecimenWithoutOpenPlan(t *testing.T) {
	t.Parallel()

	t.Run("registered specimen without any plan", func(t *testing.T) {
		t.Parallel()
		c := newServer(t)
		specimen := registerSpecimen(c, "PPS-REL-REGISTERED")
		c.expectError(http.StatusConflict, "specimen_not_releasable",
			http.MethodPost, "/specimens/"+specimen.ID+"/release", releaseRequest())
		if stored := getSpecimen(c, specimen.ID); stored.Status != "registered" || stored.Version != 1 {
			t.Fatalf("rejected release must leave registered v1, got %+v", stored)
		}
	})

	t.Run("planned specimen with draft plan", func(t *testing.T) {
		t.Parallel()
		c := newServer(t)
		specimen := registerSpecimen(c, "PPS-REL-PLANNED")
		plan := createPlan(c, specimen.ID, 80)
		c.expectError(http.StatusConflict, "open_plan_blocks_release",
			http.MethodPost, "/specimens/"+specimen.ID+"/release", releaseRequest())
		snapshot := getPlanSnapshot(c, plan.ID)
		if snapshot.Plan.Status != "draft" || snapshot.Plan.Version != 1 {
			t.Fatalf("rejected release must leave the draft untouched, got %+v", snapshot.Plan)
		}
		if stored := getSpecimen(c, specimen.ID); stored.Status != "planned" {
			t.Fatalf("rejected release must leave specimen planned, got %q", stored.Status)
		}
	})

	t.Run("preparing specimen with active plan", func(t *testing.T) {
		t.Parallel()
		c := newServer(t)
		specimen := registerSpecimen(c, "PPS-REL-PREPARING")
		plan := createPlan(c, specimen.ID, 80)
		approvePlan(c, plan.ID, "curator-lin")
		logProgress(c, plan.ID, 40, "矩阵剥离")

		c.expectError(http.StatusConflict, "open_plan_blocks_release",
			http.MethodPost, "/specimens/"+specimen.ID+"/release", releaseRequest())
		snapshot := getPlanSnapshot(c, plan.ID)
		if snapshot.Plan.Status != "active" {
			t.Fatalf("rejected release must leave the plan active, got %q", snapshot.Plan.Status)
		}
		if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" {
			t.Fatalf("rejected release must leave specimen preparing, got %q", stored.Status)
		}
	})
}

// TestStableSpecimenCanBeReleasedExactlyOnce: release succeeds only on a
// stabilized specimen with no open plan, marks it available, persists one
// permanent record, and every later attempt is rejected.
func TestStableSpecimenCanBeReleasedExactlyOnce(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen, plan := stabilizeSpecimen(c, "PPS-REL-OK")

	release := releaseSpecimen(c, specimen.ID)
	if release.SpecimenID != specimen.ID || release.ID == "" {
		t.Fatalf("release must reference the specimen and carry an id, got %+v", release)
	}
	if release.ApprovedBy != "keeper-zhou" || release.Purpose != "比较解剖研究" || release.Recipient != "地层研究组" {
		t.Fatalf("release must persist its payload, got %+v", release)
	}
	stored := getSpecimen(c, specimen.ID)
	if stored.Status != "available" {
		t.Fatalf("released specimen should be available, got %q", stored.Status)
	}
	completedPlan := getPlanSnapshot(c, plan.ID)
	if completedPlan.Plan.Status != "completed" {
		t.Fatalf("release must not reopen the completed plan, got %q", completedPlan.Plan.Status)
	}

	// The record is permanent and cannot be overwritten by a second request.
	c.expectError(http.StatusConflict, "release_exists",
		http.MethodPost, "/specimens/"+specimen.ID+"/release",
		contracts.ReleaseRequest{Purpose: "新目的", Recipient: "新接收方", ApprovedBy: "other-keeper"})

	again := getSpecimen(c, specimen.ID)
	if again.Status != "available" {
		t.Fatalf("duplicate release must not change the specimen, got %q", again.Status)
	}
	// The available terminal state never returns to preparation.
	c.expectError(http.StatusConflict, "specimen_not_plannable",
		http.MethodPost, "/specimens/"+specimen.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "目标", Method: "方法", TargetExposure: 50})
	c.expectError(http.StatusConflict, "plan_not_recordable",
		http.MethodPost, "/plans/"+plan.ID+"/progress",
		contracts.ProgressRequest{Stage: "追加", Percent: 100, RecordedBy: "preparator-chen"})
}

// TestInvalidReleasePayloadHasNoSideEffects: invalid fields must not create a
// release record nor mark the specimen available, and a valid release still
// succeeds afterwards.
func TestInvalidReleasePayloadHasNoSideEffects(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen, _ := stabilizeSpecimen(c, "PPS-REL-INVALID")

	invalid := []struct {
		name string
		body contracts.ReleaseRequest
		code string
	}{
		{"missing purpose", contracts.ReleaseRequest{Purpose: " ", Recipient: "地层研究组", ApprovedBy: "keeper-zhou"}, "required_field"},
		{"missing recipient", contracts.ReleaseRequest{Purpose: "比较解剖研究", Recipient: "", ApprovedBy: "keeper-zhou"}, "required_field"},
		{"missing approver", contracts.ReleaseRequest{Purpose: "比较解剖研究", Recipient: "地层研究组", ApprovedBy: "   "}, "required_field"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			c.expectError(http.StatusBadRequest, tc.code,
				http.MethodPost, "/specimens/"+specimen.ID+"/release", tc.body)
			if stored := getSpecimen(c, specimen.ID); stored.Status != "stabilized" {
				t.Fatalf("invalid release must keep specimen stabilized, got %q", stored.Status)
			}
		})
	}

	releaseSpecimen(c, specimen.ID)
	if stored := getSpecimen(c, specimen.ID); stored.Status != "available" {
		t.Fatalf("valid release should still succeed after rejected attempts, got %q", stored.Status)
	}
}

func TestReleaseUnknownSpecimenReturns404(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	c.expectError(http.StatusNotFound, "specimen_not_found",
		http.MethodPost, "/specimens/missing/release", releaseRequest())
}
