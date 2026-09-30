// Package workflow_test contains end-to-end checks that exercise the paleo
// preparation station exclusively through its public HTTP boundary. Every test
// gets its own httptest server on an ephemeral port and its own empty in-memory
// repository, so the suite is safe to repeat from a clean checkout and safe to
// run with -count=N or -parallel.
package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

func itoa(value int) string { return strconv.Itoa(value) }

// apiClient is a minimal JSON client for the public HTTP surface.
type apiClient struct {
	t    *testing.T
	base string
	hc   *http.Client
}

// newServer wires the production composition root (the same one used by
// cmd/workflowcheck and cmd/paleo-prep-station) with an isolated repository.
func newServer(t *testing.T) *apiClient {
	t.Helper()
	repo := repository.NewMemory()
	server := httptest.NewServer(httpapi.NewRouter(station.New(repo)))
	t.Cleanup(server.Close)
	return &apiClient{
		t:    t,
		base: server.URL,
		hc:   &http.Client{Timeout: 10 * time.Second},
	}
}

// send performs one request and returns the status code together with the
// decoded JSON body (nil when the body is empty). Invalid JSON in a response is
// a test infrastructure failure.
func (c *apiClient) send(method, path string, body any) (int, any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("perform request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.t.Fatalf("read response %s %s: %v", method, path, err)
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		c.t.Fatalf("decode response %s %s (%s): %v", method, path, string(raw), err)
	}
	return resp.StatusCode, decoded
}

// sendRaw posts a verbatim request body, used to send malformed JSON.
func (c *apiClient) sendRaw(method, path, rawBody string) (int, any) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(
		context.Background(), method, c.base+path, bytes.NewReader([]byte(rawBody)),
	)
	if err != nil {
		c.t.Fatalf("build raw request %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("perform raw request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.t.Fatalf("read raw response: %v", err)
	}
	var decoded any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			c.t.Fatalf("decode raw response (%s): %v", string(raw), err)
		}
	}
	return resp.StatusCode, decoded
}

// expectStatus sends the request and asserts the status code, returning the
// decoded body.
func (c *apiClient) expectStatus(want int, method, path string, body any) any {
	c.t.Helper()
	status, got := c.send(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s: expected status %d, got %d, body: %v", method, path, want, status, got)
	}
	return got
}

// expectError asserts both the HTTP status and the stable machine-readable code.
func (c *apiClient) expectError(wantStatus int, wantCode, method, path string, body any) {
	c.t.Helper()
	status, got := c.send(method, path, body)
	if status != wantStatus {
		c.t.Fatalf("%s %s: expected status %d, got %d, body: %v", method, path, wantStatus, status, got)
	}
	obj, ok := got.(map[string]any)
	if !ok {
		c.t.Fatalf("%s %s: expected JSON error body, got %v", method, path, got)
	}
	if code, _ := obj["code"].(string); code != wantCode {
		c.t.Fatalf("%s %s: expected error code %q, got %q (body: %v)", method, path, wantCode, code, got)
	}
}

// decode converts a decoded generic JSON value into a typed target.
func decode[T any](t *testing.T, value any) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal intermediate value: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode into target: %v (value: %v)", err, value)
	}
	return out
}

// ---- Transport views of the public JSON objects ----

type specimenDTO struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	FieldLabel    string `json:"field_label"`
	TaxonHint     string `json:"taxon_hint"`
	MatrixKind    string `json:"matrix_kind"`
	JacketNotes   string `json:"jacket_notes"`
	Status        string `json:"status"`
	Version       int    `json:"version"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type planDTO struct {
	ID             string `json:"id"`
	SpecimenID     string `json:"specimen_id"`
	Objective      string `json:"objective"`
	Method         string `json:"method"`
	Adhesive       string `json:"adhesive"`
	TargetExposure int    `json:"target_exposure"`
	Status         string `json:"status"`
	ApprovedBy     string `json:"approved_by,omitempty"`
	ApprovedAt     string `json:"approved_at,omitempty"`
	Version        int    `json:"version"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type progressDTO struct {
	ID         string `json:"id"`
	PlanID     string `json:"plan_id"`
	Sequence   int    `json:"sequence"`
	Stage      string `json:"stage"`
	Percent    int    `json:"percent"`
	Note       string `json:"note"`
	RecordedBy string `json:"recorded_by"`
	CreatedAt  string `json:"created_at"`
}

type planSnapshotDTO struct {
	Plan     planDTO       `json:"plan"`
	Progress []progressDTO `json:"progress"`
}

type releaseDTO struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Purpose    string `json:"purpose"`
	Recipient  string `json:"recipient"`
	ApprovedBy string `json:"approved_by"`
	CreatedAt  string `json:"created_at"`
}

type listEnvelope[T any] struct {
	Items []T `json:"items"`
}

// ---- Workflow builders expressed only in public requests ----

func registerSpecimen(c *apiClient, catalogNumber string) specimenDTO {
	c.t.Helper()
	body := contracts.RegisterSpecimenRequest{
		CatalogNumber: catalogNumber,
		FieldLabel:    "北坡-07 野外标签",
		TaxonHint:     "大型脊椎动物",
		MatrixKind:    "粉砂岩",
		JacketNotes:   "夹克边角完好",
	}
	return decode[specimenDTO](c.t, c.expectStatus(http.StatusCreated, http.MethodPost, "/specimens", body))
}

func getSpecimen(c *apiClient, specimenID string) specimenDTO {
	c.t.Helper()
	return decode[specimenDTO](c.t, c.expectStatus(http.StatusOK, http.MethodGet, "/specimens/"+specimenID, nil))
}

func listSpecimens(c *apiClient) []specimenDTO {
	c.t.Helper()
	value := c.expectStatus(http.StatusOK, http.MethodGet, "/specimens", nil)
	return decode[listEnvelope[specimenDTO]](c.t, value).Items
}

func createPlan(c *apiClient, specimenID string, targetExposure int) planDTO {
	c.t.Helper()
	body := contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面",
		Method:         "手工显微剥离",
		Adhesive:       "可逆树脂",
		TargetExposure: targetExposure,
	}
	path := "/specimens/" + specimenID + "/plans"
	return decode[planDTO](c.t, c.expectStatus(http.StatusCreated, http.MethodPost, path, body))
}

func listPlans(c *apiClient, specimenID string) []planDTO {
	c.t.Helper()
	path := "/specimens/" + specimenID + "/plans"
	return decode[listEnvelope[planDTO]](c.t, c.expectStatus(http.StatusOK, http.MethodGet, path, nil)).Items
}

func approvePlan(c *apiClient, planID, approver string) planDTO {
	c.t.Helper()
	body := contracts.ApprovalRequest{ApprovedBy: approver}
	path := "/plans/" + planID + "/approval"
	return decode[planDTO](c.t, c.expectStatus(http.StatusOK, http.MethodPost, path, body))
}

func logProgress(c *apiClient, planID string, percent int, stage string) progressDTO {
	c.t.Helper()
	body := contracts.ProgressRequest{
		Stage:      stage,
		Percent:    percent,
		Note:       "阶段记录",
		RecordedBy: "preparator-chen",
	}
	path := "/plans/" + planID + "/progress"
	return decode[progressDTO](c.t, c.expectStatus(http.StatusCreated, http.MethodPost, path, body))
}

func getPlanSnapshot(c *apiClient, planID string) planSnapshotDTO {
	c.t.Helper()
	return decode[planSnapshotDTO](c.t, c.expectStatus(http.StatusOK, http.MethodGet, "/plans/"+planID, nil))
}

func releaseSpecimen(c *apiClient, specimenID string) releaseDTO {
	c.t.Helper()
	body := contracts.ReleaseRequest{
		Purpose:    "比较解剖研究",
		Recipient:  "地层研究组",
		ApprovedBy: "keeper-zhou",
	}
	path := "/specimens/" + specimenID + "/release"
	return decode[releaseDTO](c.t, c.expectStatus(http.StatusCreated, http.MethodPost, path, body))
}

// stabilizeSpecimen drives a specimen through registration, plan approval and
// full preparation so release-related tests start from the stabilized state.
func stabilizeSpecimen(c *apiClient, catalogNumber string) (specimenDTO, planDTO) {
	c.t.Helper()
	specimen := registerSpecimen(c, catalogNumber)
	plan := createPlan(c, specimen.ID, 80)
	approvePlan(c, plan.ID, "curator-lin")
	logProgress(c, plan.ID, 100, "表面加固")
	return getSpecimen(c, specimen.ID), getPlanSnapshot(c, plan.ID).Plan
}
