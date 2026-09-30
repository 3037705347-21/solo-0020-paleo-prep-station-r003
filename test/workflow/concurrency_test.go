package workflow_test

import (
	"net/http"
	"sync"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
)

// concurrentResult captures one racing response.
type concurrentResult struct {
	status int
	code   string
}

func errorCode(body any) string {
	if obj, ok := body.(map[string]any); ok {
		code, _ := obj["code"].(string)
		return code
	}
	return ""
}

// race fires `n` goroutines that all block until start is closed, so the
// requests genuinely contend on the repository mutex rather than running in
// submission order.
func race(n int, fn func(i int) (int, string)) []concurrentResult {
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]concurrentResult, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			status, code := fn(i)
			results[i] = concurrentResult{status: status, code: code}
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}

func assertExactlyOneSuccess(t *testing.T, results []concurrentResult, wantStatus int, wantCode string) {
	t.Helper()
	successes, conflicts := 0, 0
	for _, result := range results {
		switch {
		case result.status == wantStatus:
			successes++
		case result.status == http.StatusConflict && result.code == wantCode:
			conflicts++
		default:
			t.Fatalf("unexpected racing response: status=%d code=%q", result.status, result.code)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one success, got %d (conflicts=%d, results=%+v)", successes, conflicts, results)
	}
	if conflicts != len(results)-1 {
		t.Fatalf("expected %d conflicts, got %d", len(results)-1, conflicts)
	}
}

// TestConcurrentDuplicateRegistration: identical concurrent registrations must
// produce exactly one specimen.
func TestConcurrentDuplicateRegistration(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	body := contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-RACE-REG",
		FieldLabel:    "北坡-并发",
		MatrixKind:    "粉砂岩",
	}
	results := race(8, func(i int) (int, string) {
		status, got := c.send(http.MethodPost, "/specimens", body)
		return status, errorCode(got)
	})
	assertExactlyOneSuccess(t, results, http.StatusCreated, "duplicate_catalog_number")
	if items := listSpecimens(c); len(items) != 1 {
		t.Fatalf("racing registrations must leave one specimen, got %d", len(items))
	}
}

// TestConcurrentDuplicatePlanCreation: concurrent plan creation on the same
// specimen must leave exactly one draft.
func TestConcurrentDuplicatePlanCreation(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-RACE-PLAN")
	body := contracts.CreatePlanRequest{Objective: "暴露目标", Method: "显微剥离", TargetExposure: 70}
	path := "/specimens/" + specimen.ID + "/plans"

	results := race(8, func(i int) (int, string) {
		status, got := c.send(http.MethodPost, path, body)
		return status, errorCode(got)
	})
	assertExactlyOneSuccess(t, results, http.StatusCreated, "open_plan_exists")
	if plans := listPlans(c, specimen.ID); len(plans) != 1 {
		t.Fatalf("racing plan creations must leave one plan, got %d", len(plans))
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "planned" {
		t.Fatalf("specimen should be planned after one winning creation, got %q", stored.Status)
	}
}

// TestConcurrentDuplicateApproval: concurrent approvals of one draft must
// succeed exactly once; the draft must not be version-incremented twice.
func TestConcurrentDuplicateApproval(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-RACE-APPROVE")
	plan := createPlan(c, specimen.ID, 80)
	path := "/plans/" + plan.ID + "/approval"
	body := contracts.ApprovalRequest{ApprovedBy: "curator-lin"}

	results := race(8, func(i int) (int, string) {
		status, got := c.send(http.MethodPost, path, body)
		return status, errorCode(got)
	})
	assertExactlyOneSuccess(t, results, http.StatusOK, "plan_not_approvable")

	snapshot := getPlanSnapshot(c, plan.ID)
	if snapshot.Plan.Status != "approved" || snapshot.Plan.Version != 2 {
		t.Fatalf("racing approvals must leave plan approved v2, got status=%q version=%d",
			snapshot.Plan.Status, snapshot.Plan.Version)
	}
	if snapshot.Plan.ApprovedBy != "curator-lin" {
		t.Fatalf("approver must be recorded once, got %q", snapshot.Plan.ApprovedBy)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" || stored.Version != 3 {
		t.Fatalf("racing approvals must leave specimen preparing v3, got %+v", stored)
	}
}

// TestConcurrentProgressAppends: concurrent monotonic entries must all succeed
// with exactly consecutive sequence numbers 1..N, never duplicated or gapped.
func TestConcurrentProgressAppends(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-RACE-PROGRESS")
	plan := createPlan(c, specimen.ID, 100)
	approvePlan(c, plan.ID, "curator-lin")
	body := contracts.ProgressRequest{
		Stage:      "并发清理",
		Percent:    50,
		Note:       "同一比例可重复",
		RecordedBy: "preparator-chen",
	}
	path := "/plans/" + plan.ID + "/progress"
	const workers = 8

	results := race(workers, func(i int) (int, string) {
		status, got := c.send(http.MethodPost, path, body)
		return status, errorCode(got)
	})
	for i, result := range results {
		if result.status != http.StatusCreated {
			t.Fatalf("monotonic racing append %d should succeed, got status=%d code=%q",
				i, result.status, result.code)
		}
	}

	snapshot := getPlanSnapshot(c, plan.ID)
	if len(snapshot.Progress) != workers {
		t.Fatalf("expected %d progress entries, got %d", workers, len(snapshot.Progress))
	}
	seen := make(map[int]bool, workers)
	for i, entry := range snapshot.Progress {
		if entry.Sequence != i+1 {
			t.Fatalf("racing sequences must be consecutive 1..N, position %d got sequence %d",
				i, entry.Sequence)
		}
		if seen[entry.Sequence] {
			t.Fatalf("duplicate sequence %d after racing appends", entry.Sequence)
		}
		seen[entry.Sequence] = true
	}
	if snapshot.Plan.Status != "active" {
		t.Fatalf("plan must remain active under 50%% racing entries, got %q", snapshot.Plan.Status)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "preparing" {
		t.Fatalf("specimen must remain preparing under partial racing entries, got %q", stored.Status)
	}
}

// TestConcurrentFinalProgress: racing 100% writes complete the plan exactly
// once and never leave a completed plan that can accept another entry.
func TestConcurrentFinalProgress(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen := registerSpecimen(c, "PPS-RACE-FINAL")
	plan := createPlan(c, specimen.ID, 100)
	approvePlan(c, plan.ID, "curator-lin")
	logProgress(c, plan.ID, 50, "矩阵剥离")
	body := contracts.ProgressRequest{
		Stage:      "表面加固",
		Percent:    100,
		RecordedBy: "preparator-chen",
	}
	path := "/plans/" + plan.ID + "/progress"

	results := race(8, func(i int) (int, string) {
		status, got := c.send(http.MethodPost, path, body)
		return status, errorCode(got)
	})
	assertExactlyOneSuccess(t, results, http.StatusCreated, "plan_not_recordable")

	snapshot := getPlanSnapshot(c, plan.ID)
	if snapshot.Plan.Status != "completed" {
		t.Fatalf("racing final entries must leave the plan completed, got %q", snapshot.Plan.Status)
	}
	if len(snapshot.Progress) != 2 {
		t.Fatalf("exactly one final entry must be stored, got %d", len(snapshot.Progress))
	}
	if last := snapshot.Progress[len(snapshot.Progress)-1]; last.Percent != 100 || last.Sequence != 2 {
		t.Fatalf("last entry must be sequence 2 at 100%%, got %+v", last)
	}
	if stored := getSpecimen(c, specimen.ID); stored.Status != "stabilized" {
		t.Fatalf("racing final entries must stabilize the specimen once, got %q", stored.Status)
	}
}

// TestConcurrentRelease: racing releases on a stabilized specimen must create
// exactly one permanent release record.
func TestConcurrentRelease(t *testing.T) {
	t.Parallel()
	c := newServer(t)
	specimen, _ := stabilizeSpecimen(c, "PPS-RACE-RELEASE")
	path := "/specimens/" + specimen.ID + "/release"
	body := contracts.ReleaseRequest{Purpose: "比较解剖研究", Recipient: "地层研究组", ApprovedBy: "keeper-zhou"}

	results := race(8, func(i int) (int, string) {
		status, got := c.send(http.MethodPost, path, body)
		return status, errorCode(got)
	})
	assertExactlyOneSuccess(t, results, http.StatusCreated, "release_exists")

	if stored := getSpecimen(c, specimen.ID); stored.Status != "available" {
		t.Fatalf("racing releases must leave the specimen available, got %q", stored.Status)
	}
	// A late release is still rejected: the record is permanent.
	c.expectError(http.StatusConflict, "release_exists", http.MethodPost, path, body)
}
