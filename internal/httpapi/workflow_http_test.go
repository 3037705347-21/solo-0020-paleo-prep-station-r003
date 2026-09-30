// Package httpapi_test 通过公共 HTTP 路由验证标本登记、方案审批、制备进度与研究放行
// 之间的边界条件、重复操作、并发冲突以及“失败不产生半截状态”。
//
// 这些测试不直接触碰仓储或领域对象：每个用例都拿到一个独立的内存仓储和一个
// 监听临时端口的 httptest 服务器，只通过文档中声明的公共路由交互，因此可以从
// 干净检出重复执行，也不会依赖上一次运行的残留状态。
package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

// ----------------------------------------------------------------------------
// 测试夹具：每个用例独立仓储 + 临时端口服务器
// ----------------------------------------------------------------------------

type testEnv struct {
	baseURL string
	client  *http.Client
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	// 每个用例显式新建仓储，禁止跨用例共享任何实体或版本状态。
	service := station.New(repository.NewMemory())
	server := httptest.NewServer(httpapi.NewRouter(service))
	t.Cleanup(server.Close)
	return &testEnv{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// send 发送原始字节请求，返回 HTTP 状态码与响应体。
func (e *testEnv) send(t *testing.T, method, path string, raw []byte) (int, []byte) {
	t.Helper()
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.baseURL+path, body)
	if err != nil {
		t.Fatalf("构造请求 %s %s 失败: %v", method, path, err)
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("读取 %s %s 响应失败: %v", method, path, err)
	}
	return resp.StatusCode, data
}

func (e *testEnv) jsonRequest(t *testing.T, method, path string, payload any) (int, []byte) {
	t.Helper()
	if payload == nil {
		return e.send(t, method, path, nil)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}
	return e.send(t, method, path, raw)
}

// mustDo 断言状态码并把成功响应解码到 T。
func mustDo[T any](t *testing.T, e *testEnv, method, path string, payload any, wantStatus int) T {
	t.Helper()
	status, data := e.jsonRequest(t, method, path, payload)
	if status != wantStatus {
		t.Fatalf("%s %s: 期望状态 %d，实际 %d，响应: %s", method, path, wantStatus, status, string(data))
	}
	var value T
	if len(data) > 0 {
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("解码 %s %s 成功响应失败: %v，响应: %s", method, path, err, string(data))
		}
	}
	return value
}

// expectAPIError 断言失败状态码与稳定错误码，并返回错误体以便进一步检查。
func expectAPIError(t *testing.T, e *testEnv, method, path string, payload any, wantStatus int, wantCode string) errorBody {
	t.Helper()
	status, data := e.jsonRequest(t, method, path, payload)
	if status != wantStatus {
		t.Fatalf("%s %s: 期望失败状态 %d，实际 %d，响应: %s", method, path, wantStatus, status, string(data))
	}
	var body errorBody
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("解码 %s %s 错误响应失败: %v，响应: %s", method, path, err, string(data))
	}
	if body.Code != wantCode {
		t.Fatalf("%s %s: 期望错误码 %q，实际 %q（%s）", method, path, wantCode, body.Code, string(data))
	}
	return body
}

// expectRawError 用于发送畸形 JSON 等无法结构体编码的请求。
func expectRawError(t *testing.T, e *testEnv, method, path string, raw string, wantStatus int, wantCode string) {
	t.Helper()
	status, data := e.send(t, method, path, []byte(raw))
	if status != wantStatus {
		t.Fatalf("%s %s: 期望状态 %d，实际 %d，响应: %s", method, path, wantStatus, status, string(data))
	}
	var body errorBody
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("解码错误响应失败: %v，响应: %s", err, string(data))
	}
	if body.Code != wantCode {
		t.Fatalf("%s %s: 期望错误码 %q，实际 %q（%s）", method, path, wantCode, body.Code, string(data))
	}
}

// ----------------------------------------------------------------------------
// 稳定的公共响应模型（与接口实际 JSON 保持一致，测试只依赖公共序列化形状）
// ----------------------------------------------------------------------------

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type specimenDTO struct {
	ID            string    `json:"id"`
	CatalogNumber string    `json:"catalog_number"`
	FieldLabel    string    `json:"field_label"`
	TaxonHint     string    `json:"taxon_hint"`
	MatrixKind    string    `json:"matrix_kind"`
	JacketNotes   string    `json:"jacket_notes"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type planDTO struct {
	ID             string     `json:"id"`
	SpecimenID     string     `json:"specimen_id"`
	Objective      string     `json:"objective"`
	Method         string     `json:"method"`
	Adhesive       string     `json:"adhesive"`
	TargetExposure int        `json:"target_exposure"`
	Status         string     `json:"status"`
	ApprovedBy     string     `json:"approved_by,omitempty"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	Version        int        `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type progressDTO struct {
	ID         string    `json:"id"`
	PlanID     string    `json:"plan_id"`
	Sequence   int       `json:"sequence"`
	Stage      string    `json:"stage"`
	Percent    int       `json:"percent"`
	Note       string    `json:"note"`
	RecordedBy string    `json:"recorded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

type planSnapshotDTO struct {
	Plan     planDTO       `json:"plan"`
	Progress []progressDTO `json:"progress"`
}

type releaseDTO struct {
	ID         string    `json:"id"`
	SpecimenID string    `json:"specimen_id"`
	Purpose    string    `json:"purpose"`
	Recipient  string    `json:"recipient"`
	ApprovedBy string    `json:"approved_by"`
	CreatedAt  time.Time `json:"created_at"`
}

type specimenListDTO struct {
	Items []specimenDTO `json:"items"`
}

type planListDTO struct {
	Items []planDTO `json:"items"`
}

// ----------------------------------------------------------------------------
// 业务链路构造辅助（全部走公共 HTTP）
// ----------------------------------------------------------------------------

func validRegister(catalog string) contracts.RegisterSpecimenRequest {
	return contracts.RegisterSpecimenRequest{
		CatalogNumber: catalog,
		FieldLabel:    "北坡-07",
		TaxonHint:     "大型脊椎动物",
		MatrixKind:    "粉砂岩",
		JacketNotes:   "夹克边角完好",
	}
}

func registerSpecimen(t *testing.T, e *testEnv, catalog string) specimenDTO {
	t.Helper()
	return mustDo[specimenDTO](t, e, http.MethodPost, "/specimens", validRegister(catalog), http.StatusCreated)
}

func getSpecimen(t *testing.T, e *testEnv, id string) specimenDTO {
	t.Helper()
	return mustDo[specimenDTO](t, e, http.MethodGet, "/specimens/"+id, nil, http.StatusOK)
}

func listSpecimens(t *testing.T, e *testEnv) []specimenDTO {
	t.Helper()
	return mustDo[specimenListDTO](t, e, http.MethodGet, "/specimens", nil, http.StatusOK).Items
}

func createPlanWithTarget(t *testing.T, e *testEnv, specimenID string, target int) planDTO {
	t.Helper()
	request := contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面",
		Method:         "手工显微剥离",
		Adhesive:       "可逆树脂",
		TargetExposure: target,
	}
	return mustDo[planDTO](t, e, http.MethodPost, "/specimens/"+specimenID+"/plans", request, http.StatusCreated)
}

func createPlan(t *testing.T, e *testEnv, specimenID string) planDTO {
	t.Helper()
	return createPlanWithTarget(t, e, specimenID, 80)
}

func listPlans(t *testing.T, e *testEnv, specimenID string) []planDTO {
	t.Helper()
	return mustDo[planListDTO](t, e, http.MethodGet, "/specimens/"+specimenID+"/plans", nil, http.StatusOK).Items
}

func approvePlan(t *testing.T, e *testEnv, planID, approver string) planDTO {
	t.Helper()
	return mustDo[planDTO](t, e, http.MethodPost, "/plans/"+planID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: approver}, http.StatusOK)
}

func getPlanSnapshot(t *testing.T, e *testEnv, planID string) planSnapshotDTO {
	t.Helper()
	return mustDo[planSnapshotDTO](t, e, http.MethodGet, "/plans/"+planID, nil, http.StatusOK)
}

func progressRequest(percent int) contracts.ProgressRequest {
	return contracts.ProgressRequest{
		Stage:      fmt.Sprintf("阶段-%d", percent),
		Percent:    percent,
		Note:       "边界测试记录",
		RecordedBy: "preparator-chen",
	}
}

func logProgress(t *testing.T, e *testEnv, planID string, percent int) progressDTO {
	t.Helper()
	return mustDo[progressDTO](t, e, http.MethodPost, "/plans/"+planID+"/progress",
		progressRequest(percent), http.StatusCreated)
}

func releaseSpecimen(t *testing.T, e *testEnv, specimenID string) releaseDTO {
	t.Helper()
	request := contracts.ReleaseRequest{
		Purpose:    "比较解剖研究",
		Recipient:  "地层研究组",
		ApprovedBy: "keeper-zhou",
	}
	return mustDo[releaseDTO](t, e, http.MethodPost, "/specimens/"+specimenID+"/release", request, http.StatusCreated)
}

// driveToStabilized 走通“登记 -> 方案 -> 审批 -> 100% 进度”，返回已稳定的标本与终态方案快照。
func driveToStabilized(t *testing.T, e *testEnv, catalog string) (specimenDTO, planSnapshotDTO) {
	t.Helper()
	specimen := registerSpecimen(t, e, catalog)
	plan := createPlan(t, e, specimen.ID)
	approvePlan(t, e, plan.ID, "curator-lin")
	logProgress(t, e, plan.ID, 100)
	return getSpecimen(t, e, specimen.ID), getPlanSnapshot(t, e, plan.ID)
}

// statusCounter 汇总并发写的 HTTP 结果。
type statusCounter struct {
	mu     sync.Mutex
	counts map[int]int
}

func (c *statusCounter) record(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[status]++
}

func (c *statusCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.counts {
		total += n
	}
	return total
}

func (c *statusCounter) get(status int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[status]
}

// runConcurrent 让所有 goroutine 在同一道屏障后同时发起请求，制造真实竞争窗口。
func runConcurrent(t *testing.T, workers int, fn func(worker int)) {
	t.Helper()
	var ready, start sync.WaitGroup
	ready.Add(workers)
	start.Add(1)
	var done sync.WaitGroup
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func(worker int) {
			defer done.Done()
			ready.Done()
			start.Wait()
			fn(worker)
		}(i)
	}
	ready.Wait()
	start.Done()
	done.Wait()
}

// ----------------------------------------------------------------------------
// 1. 目录号大小写归一与唯一性
// ----------------------------------------------------------------------------

func TestCatalogNumberCaseNormalizationAndUniqueness(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	// 带空白与小写的目录号应被修剪并归一化为大写。
	created := mustDo[specimenDTO](t, e, http.MethodPost, "/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: "  pps-2026-0007  ",
			FieldLabel:    "北坡-07",
			MatrixKind:    "粉砂岩",
		}, http.StatusCreated)
	if created.CatalogNumber != "PPS-2026-0007" {
		t.Fatalf("目录号应归一化为大写，实际保存 %q", created.CatalogNumber)
	}
	if created.Status != "registered" || created.Version != 1 {
		t.Fatalf("新标本应为 registered/版本1，实际 %s/版本%d", created.Status, created.Version)
	}
	if fetched := getSpecimen(t, e, created.ID); fetched.CatalogNumber != "PPS-2026-0007" {
		t.Fatalf("读回的目录号应为归一化大写形式，实际 %q", fetched.CatalogNumber)
	}

	// 不同大小写、混合大小写的同一目录号都必须被唯一性约束拒绝。
	for _, variant := range []string{"PPS-2026-0007", "pps-2026-0007", "PpS-2026-0007"} {
		expectAPIError(t, e, http.MethodPost, "/specimens", validRegister(variant),
			http.StatusConflict, "duplicate_catalog_number")
	}

	// 所有拒绝都不得产生实体。
	if items := listSpecimens(t, e); len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("重复登记被拒绝后应只剩 1 条标本，实际 %d 条", len(items))
	}
}

func TestCatalogNumberRejectsInvalidFormsWithoutSideEffects(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	cases := []struct {
		name    string
		catalog string
		code    string
	}{
		{"空目录号", "", "required_field"},
		{"纯空白", "   ", "required_field"},
		{"非法字符", "bad catalog!", "invalid_catalog_number"},
		{"长度不足两位", "a", "invalid_catalog_number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := validRegister(tc.catalog)
			expectAPIError(t, e, http.MethodPost, "/specimens", request,
				http.StatusBadRequest, tc.code)
		})
	}

	// 缺少其它必填字段同样拒绝，且整个仓储保持为空。
	missingField := validRegister("PPS-2026-0008")
	missingField.FieldLabel = "   "
	expectAPIError(t, e, http.MethodPost, "/specimens", missingField,
		http.StatusBadRequest, "required_field")

	if items := listSpecimens(t, e); len(items) != 0 {
		t.Fatalf("所有非法登记被拒绝后仓储应为空，实际存在 %d 条标本", len(items))
	}

	// 未知 JSON 字段与畸形 JSON 也必须在进入业务逻辑前被拒绝，不留实体。
	expectRawError(t, e, http.MethodPost, "/specimens", `{not-json`,
		http.StatusBadRequest, "invalid_json")
	raw, _ := json.Marshal(map[string]any{
		"catalog_number": "PPS-2026-0009",
		"field_label":    "北坡-09",
		"matrix_kind":    "泥岩",
		"unexpected":     true,
	})
	expectRawError(t, e, http.MethodPost, "/specimens", string(raw),
		http.StatusBadRequest, "invalid_json")
	if items := listSpecimens(t, e); len(items) != 0 {
		t.Fatalf("畸形请求被拒绝后仓储仍应为空，实际存在 %d 条标本", len(items))
	}
}

func TestConcurrentDuplicateRegistrationIsSerialized(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	const workers = 8
	results := statusCounter{counts: map[int]int{}}
	runConcurrent(t, workers, func(int) {
		status, _ := e.jsonRequest(t, http.MethodPost, "/specimens", validRegister("PPS-CONC-01"))
		results.record(status)
	})

	if got := results.get(http.StatusCreated); got != 1 {
		t.Fatalf("并发重复登记应恰好 1 次成功，实际 %d 次（状态分布 %v）", got, results.counts)
	}
	if got := results.get(http.StatusConflict); got != workers-1 {
		t.Fatalf("并发重复登记应有 %d 次冲突，实际 %d 次", workers-1, got)
	}
	if items := listSpecimens(t, e); len(items) != 1 {
		t.Fatalf("并发竞争后应只剩 1 条标本，实际 %d 条", len(items))
	}
}

// ----------------------------------------------------------------------------
// 2. 目标暴露比例边界
// ----------------------------------------------------------------------------

func TestTargetExposureBoundaries(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	// 0、负数、超过 100 都是 400，且标本、方案集合都不得变化。
	for _, target := range []int{0, -1, 101, 1000} {
		specimen := registerSpecimen(t, e, fmt.Sprintf("PPS-TE-%d", target+1000))
		before := getSpecimen(t, e, specimen.ID)
		request := contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			TargetExposure: target,
		}
		expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans", request,
			http.StatusBadRequest, "invalid_target_exposure")

		after := getSpecimen(t, e, specimen.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("目标暴露比例 %d 被拒绝后标本发生变化：before=%+v after=%+v", target, before, after)
		}
		if plans := listPlans(t, e, specimen.ID); len(plans) != 0 {
			t.Fatalf("非法方案不得落库，目标比例 %d 拒绝后仍有 %d 条方案", target, len(plans))
		}
	}

	// 合法边界 1 与 100 必须接受；每个标本各自只有一条草稿方案。
	for _, target := range []int{1, 100} {
		specimen := registerSpecimen(t, e, fmt.Sprintf("PPS-TE-OK-%d", target))
		plan := createPlanWithTarget(t, e, specimen.ID, target)
		if plan.TargetExposure != target || plan.Status != "draft" || plan.Version != 1 {
			t.Fatalf("目标比例 %d 的方案应为 draft/版本1，实际 %s/版本%d",
				target, plan.Status, plan.Version)
		}
		planned := getSpecimen(t, e, specimen.ID)
		if planned.Status != "planned" || planned.Version != 2 {
			t.Fatalf("建方案后标本应为 planned/版本2，实际 %s/版本%d",
				planned.Status, planned.Version)
		}
	}
}

// ----------------------------------------------------------------------------
// 3. 开放方案互斥
// ----------------------------------------------------------------------------

func TestOpenPlanMutualExclusionAcrossLifecycle(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-MUTEX-01")
	plan := createPlan(t, e, specimen.ID)

	// 草稿期间：互斥。
	expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "第二个目标", Method: "其它方法", TargetExposure: 50},
		http.StatusConflict, "open_plan_exists")

	// 审批后（preparing）：互斥。
	approvePlan(t, e, plan.ID, "curator-lin")
	expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "第二个目标", Method: "其它方法", TargetExposure: 50},
		http.StatusConflict, "open_plan_exists")

	// 进行中：互斥。
	logProgress(t, e, plan.ID, 50)
	expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "第二个目标", Method: "其它方法", TargetExposure: 50},
		http.StatusConflict, "open_plan_exists")

	// 100% 后方案闭合，但标本已稳定、不再处于可规划状态：仍拒绝，且没有第二条方案落库。
	logProgress(t, e, plan.ID, 100)
	expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "第二个目标", Method: "其它方法", TargetExposure: 50},
		http.StatusConflict, "specimen_not_plannable")

	if plans := listPlans(t, e, specimen.ID); len(plans) != 1 {
		t.Fatalf("整个生命周期内方案应始终只有 1 条，实际 %d 条", len(plans))
	}
	finalSpecimen := getSpecimen(t, e, specimen.ID)
	if finalSpecimen.Status != "stabilized" {
		t.Fatalf("终态标本应为 stabilized，实际 %s", finalSpecimen.Status)
	}
}

func TestConcurrentOpenPlanCreationHasSingleWinner(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-MUTEX-CONC")

	const workers = 8
	results := statusCounter{counts: map[int]int{}}
	runConcurrent(t, workers, func(worker int) {
		request := contracts.CreatePlanRequest{
			Objective:      fmt.Sprintf("竞争目标 %d", worker),
			Method:         "手工显微剥离",
			TargetExposure: 60,
		}
		status, _ := e.jsonRequest(t, http.MethodPost, "/specimens/"+specimen.ID+"/plans", request)
		results.record(status)
	})

	if got := results.get(http.StatusCreated); got != 1 {
		t.Fatalf("并发建方案应恰好 1 次成功，实际 %d 次（分布 %v）", got, results.counts)
	}
	if got := results.get(http.StatusConflict); got != workers-1 {
		t.Fatalf("并发建方案应有 %d 次冲突，实际 %d 次", workers-1, got)
	}
	plans := listPlans(t, e, specimen.ID)
	if len(plans) != 1 {
		t.Fatalf("竞争后应只存在 1 条方案，实际 %d 条", len(plans))
	}
	// 失败者的半截事务不得推进标本版本：registered(1) 只能被唯一赢家推进到 planned(2)。
	if got := getSpecimen(t, e, specimen.ID); got.Version != 2 || got.Status != "planned" {
		t.Fatalf("竞争收敛后标本应为 planned/版本2，实际 %s/版本%d", got.Status, got.Version)
	}
}

// ----------------------------------------------------------------------------
// 4. 只有草稿可审批
// ----------------------------------------------------------------------------

func TestOnlyDraftPlanCanBeApproved(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-APPR-01")
	plan := createPlan(t, e, specimen.ID)

	// 空审批人：400，草稿与标本均不得发生任何变化。
	beforePlan := getPlanSnapshot(t, e, plan.ID)
	beforeSpecimen := getSpecimen(t, e, specimen.ID)
	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "   "}, http.StatusBadRequest, "required_field")
	if got := getPlanSnapshot(t, e, plan.ID); !reflect.DeepEqual(beforePlan, got) {
		t.Fatalf("审批人缺失被拒绝后方案快照发生变化: before=%+v after=%+v", beforePlan, got)
	}
	if got := getSpecimen(t, e, specimen.ID); !reflect.DeepEqual(beforeSpecimen, got) {
		t.Fatalf("审批人缺失被拒绝后标本发生变化: before=%+v after=%+v", beforeSpecimen, got)
	}

	// 正常审批：方案 approved 且带审批人/审批时间，标本同步进入 preparing。
	approved := approvePlan(t, e, plan.ID, "curator-lin")
	if approved.Status != "approved" || approved.ApprovedBy != "curator-lin" ||
		approved.ApprovedAt == nil || approved.Version != 2 {
		t.Fatalf("审批后方案状态异常: %+v", approved)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "preparing" || got.Version != 3 {
		t.Fatalf("审批后标本应为 preparing/版本3，实际 %s/版本%d", got.Status, got.Version)
	}

	// 重复审批已批准方案：409，方案与标本版本冻结。
	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-zhao"}, http.StatusConflict, "plan_not_approvable")
	if got := getPlanSnapshot(t, e, plan.ID); !reflect.DeepEqual(approved, got.Plan) {
		t.Fatalf("重复审批后方案被修改: before=%+v after=%+v", approved, got.Plan)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "preparing" || got.Version != 3 {
		t.Fatalf("重复审批后标本发生变化: %+v", got)
	}

	// active 与 completed 状态同样不可审批。
	logProgress(t, e, plan.ID, 40)
	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-zhao"}, http.StatusConflict, "plan_not_approvable")
	logProgress(t, e, plan.ID, 100)
	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-zhao"}, http.StatusConflict, "plan_not_approvable")

	// 不存在的方案：404。
	expectAPIError(t, e, http.MethodPost, "/plans/plan-missing/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"}, http.StatusNotFound, "plan_not_found")
}

func TestDraftPlanCannotReceiveProgress(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-APPR-DRAFT")
	plan := createPlan(t, e, specimen.ID)

	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/progress",
		progressRequest(10), http.StatusConflict, "plan_not_recordable")
	snapshot := getPlanSnapshot(t, e, plan.ID)
	if len(snapshot.Progress) != 0 || snapshot.Plan.Status != "draft" || snapshot.Plan.Version != 1 {
		t.Fatalf("草稿被拒绝写进度后应无记录且保持 draft/版本1，实际 %+v", snapshot)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "planned" || got.Version != 2 {
		t.Fatalf("草稿被拒绝写进度后标本发生变化: %+v", got)
	}
}

func TestConcurrentPlanApprovalHasSingleWinner(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-APPR-CONC")
	plan := createPlan(t, e, specimen.ID)

	const workers = 6
	results := statusCounter{counts: map[int]int{}}
	runConcurrent(t, workers, func(int) {
		status, _ := e.jsonRequest(t, http.MethodPost, "/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"})
		results.record(status)
	})

	if got := results.get(http.StatusOK); got != 1 {
		t.Fatalf("并发审批应恰好 1 次成功，实际 %d 次（分布 %v）", got, results.counts)
	}
	if got := results.get(http.StatusConflict); got != workers-1 {
		t.Fatalf("并发审批应有 %d 次冲突，实际 %d 次", workers-1, got)
	}
	snapshot := getPlanSnapshot(t, e, plan.ID)
	if snapshot.Plan.Status != "approved" || snapshot.Plan.Version != 2 {
		t.Fatalf("竞争后方案应为 approved/版本2，实际 %s/版本%d",
			snapshot.Plan.Status, snapshot.Plan.Version)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "preparing" || got.Version != 3 {
		t.Fatalf("竞争后标本应为 preparing/版本3，实际 %s/版本%d", got.Status, got.Version)
	}
}

// ----------------------------------------------------------------------------
// 5. 进度不得回退且序号连续（含并发写）
// ----------------------------------------------------------------------------

func TestProgressMonotonicAndSequencesContiguous(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-PROG-01")
	plan := createPlan(t, e, specimen.ID)
	approvePlan(t, e, plan.ID, "curator-lin")

	// 0 是合法起点；相等比例允许重复（规则只禁止“低于”）。
	expectedPercents := []int{0, 0, 60, 60}
	for i, percent := range expectedPercents {
		entry := logProgress(t, e, plan.ID, percent)
		if entry.Sequence != i+1 {
			t.Fatalf("第 %d 条记录序号应为 %d，实际 %d", i+1, i+1, entry.Sequence)
		}
	}

	// 第一条有效进度把方案从 approved 推进到 active；之后非终态进度不再升版本。
	snapshot := getPlanSnapshot(t, e, plan.ID)
	if snapshot.Plan.Status != "active" || snapshot.Plan.Version != 3 {
		t.Fatalf("追加非终态进度后方案应为 active/版本3，实际 %s/版本%d",
			snapshot.Plan.Status, snapshot.Plan.Version)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "preparing" || got.Version != 3 {
		t.Fatalf("非终态进度不应改动标本，实际 %s/版本%d", got.Status, got.Version)
	}

	// 回退必须 409，且不得追加记录、不得改版本。
	before := getPlanSnapshot(t, e, plan.ID)
	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/progress",
		progressRequest(59), http.StatusConflict, "progress_regressed")
	if after := getPlanSnapshot(t, e, plan.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("回退被拒绝后方案状态发生变化: before=%+v after=%+v", before, after)
	}

	// 超界比例 400，同样无副作用。
	for _, percent := range []int{-1, 101} {
		expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/progress",
			progressRequest(percent), http.StatusBadRequest, "invalid_percent")
	}
	if after := getPlanSnapshot(t, e, plan.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("非法比例被拒绝后方案状态发生变化: before=%+v after=%+v", before, after)
	}

	// 完成后任何进度写入都必须拒绝，且记录数冻结。
	logProgress(t, e, plan.ID, 100)
	expectAPIError(t, e, http.MethodPost, "/plans/"+plan.ID+"/progress",
		progressRequest(100), http.StatusConflict, "plan_not_recordable")
	final := getPlanSnapshot(t, e, plan.ID)
	if len(final.Progress) != 5 {
		t.Fatalf("完成后追加必须被拒绝，最终应只有 5 条记录，实际 %d 条", len(final.Progress))
	}
	sequences := make([]int, len(final.Progress))
	perc := make([]int, len(final.Progress))
	for i, entry := range final.Progress {
		sequences[i] = entry.Sequence
		perc[i] = entry.Percent
	}
	wantSequences := []int{1, 2, 3, 4, 5}
	wantPercents := []int{0, 0, 60, 60, 100}
	if !reflect.DeepEqual(sequences, wantSequences) {
		t.Fatalf("序号必须从 1 连续，实际 %v", sequences)
	}
	if !reflect.DeepEqual(perc, wantPercents) {
		t.Fatalf("最终比例序列异常，实际 %v，期望 %v", perc, wantPercents)
	}
}

func TestConcurrentProgressWritesProduceContiguousSequences(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-PROG-CONC")
	plan := createPlan(t, e, specimen.ID)
	approvePlan(t, e, plan.ID, "curator-lin")

	// 所有写都使用相等比例 50：既不会回退，也不会触发终态；
	// 因而这是对“并发重复写 + 序号连续 + 无丢失追加”的纯粹检验。
	const workers = 20
	const perWorker = 50
	results := statusCounter{counts: map[int]int{}}
	runConcurrent(t, workers, func(int) {
		for i := 0; i < perWorker; i++ {
			status, _ := e.jsonRequest(t, http.MethodPost, "/plans/"+plan.ID+"/progress",
				progressRequest(50))
			results.record(status)
		}
	})

	wantTotal := workers * perWorker
	if results.total() != wantTotal {
		t.Fatalf("应完成全部 %d 个并发写，实际 %d", wantTotal, results.total())
	}
	if got := results.get(http.StatusCreated); got != wantTotal {
		t.Fatalf("全部并发写都应成功，实际成功 %d（分布 %v）", got, results.counts)
	}

	snapshot := getPlanSnapshot(t, e, plan.ID)
	if len(snapshot.Progress) != wantTotal {
		t.Fatalf("并发写后应有 %d 条记录，实际 %d 条", wantTotal, len(snapshot.Progress))
	}
	seen := make(map[int]bool, wantTotal)
	sequences := make([]int, 0, wantTotal)
	for _, entry := range snapshot.Progress {
		if entry.Percent != 50 {
			t.Fatalf("记录 %d 比例被污染: %d", entry.Sequence, entry.Percent)
		}
		if seen[entry.Sequence] {
			t.Fatalf("序号 %d 被重复分配，违反序号唯一连续", entry.Sequence)
		}
		seen[entry.Sequence] = true
		sequences = append(sequences, entry.Sequence)
	}
	sort.Ints(sequences)
	for i, seq := range sequences {
		if seq != i+1 {
			t.Fatalf("序号集合应恰为 1..%d，位置 %d 出现 %d", wantTotal, i+1, seq)
		}
	}
	// 没有任何一条达到 100：方案保持 active，标本保持 preparing。
	if snapshot.Plan.Status != "active" || snapshot.Plan.Version != 3 {
		t.Fatalf("并发写后方案应保持 active/版本3，实际 %s/版本%d",
			snapshot.Plan.Status, snapshot.Plan.Version)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "preparing" || got.Version != 3 {
		t.Fatalf("并发写后标本应保持 preparing/版本3，实际 %s/版本%d", got.Status, got.Version)
	}
}

// ----------------------------------------------------------------------------
// 6. 比例达到 100 时方案与标本在同一次调用中同时进入终态
// ----------------------------------------------------------------------------

func TestOneHundredPercentFinalizesPlanAndSpecimenAtomically(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	t.Run("首条进度即100", func(t *testing.T) {
		specimen := registerSpecimen(t, e, "PPS-FULL-01")
		plan := createPlan(t, e, specimen.ID)
		approvePlan(t, e, plan.ID, "curator-lin")

		entry := logProgress(t, e, plan.ID, 100)
		if entry.Sequence != 1 || entry.Percent != 100 {
			t.Fatalf("首条 100%% 记录形状异常: %+v", entry)
		}
		snapshot := getPlanSnapshot(t, e, plan.ID)
		// approved(2) -> Begin(3) -> Complete(4)，一次请求内完成，且只落一条记录。
		if snapshot.Plan.Status != "completed" || snapshot.Plan.Version != 4 {
			t.Fatalf("方案应为 completed/版本4，实际 %s/版本%d",
				snapshot.Plan.Status, snapshot.Plan.Version)
		}
		if len(snapshot.Progress) != 1 {
			t.Fatalf("应只落 1 条终态记录，实际 %d 条", len(snapshot.Progress))
		}
		got := getSpecimen(t, e, specimen.ID)
		// registered(1) -> planned(2) -> preparing(3) -> stabilized(4)。
		if got.Status != "stabilized" || got.Version != 4 {
			t.Fatalf("同一请求内标本应同步为 stabilized/版本4，实际 %s/版本%d",
				got.Status, got.Version)
		}
	})

	t.Run("并发双写100只有一次生效", func(t *testing.T) {
		specimen := registerSpecimen(t, e, "PPS-FULL-02")
		plan := createPlan(t, e, specimen.ID)
		approvePlan(t, e, plan.ID, "curator-lin")

		results := statusCounter{counts: map[int]int{}}
		runConcurrent(t, 2, func(int) {
			status, _ := e.jsonRequest(t, http.MethodPost, "/plans/"+plan.ID+"/progress",
				progressRequest(100))
			results.record(status)
		})
		if results.get(http.StatusCreated) != 1 || results.get(http.StatusConflict) != 1 {
			t.Fatalf("并发双写 100%% 应一成一冲突，分布 %v", results.counts)
		}

		snapshot := getPlanSnapshot(t, e, plan.ID)
		if len(snapshot.Progress) != 1 {
			t.Fatalf("失败方不得留下记录，应只有 1 条，实际 %d 条", len(snapshot.Progress))
		}
		if snapshot.Plan.Status != "completed" || snapshot.Plan.Version != 4 {
			t.Fatalf("方案终态异常: %s/版本%d", snapshot.Plan.Status, snapshot.Plan.Version)
		}
		got := getSpecimen(t, e, specimen.ID)
		if got.Status != "stabilized" || got.Version != 4 {
			t.Fatalf("标本终态异常: %s/版本%d", got.Status, got.Version)
		}
	})
}

// ----------------------------------------------------------------------------
// 7. 只有“已稳定且无开放方案”的标本才能放行
// ----------------------------------------------------------------------------

func TestReleaseGateAcrossSpecimenStates(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	validRelease := contracts.ReleaseRequest{
		Purpose:    "比较解剖研究",
		Recipient:  "地层研究组",
		ApprovedBy: "keeper-zhou",
	}

	// registered：直接拒绝。
	registered := registerSpecimen(t, e, "PPS-REL-01")
	expectAPIError(t, e, http.MethodPost, "/specimens/"+registered.ID+"/release", validRelease,
		http.StatusConflict, "specimen_not_releasable")
	if got := getSpecimen(t, e, registered.ID); !reflect.DeepEqual(got, registered) {
		t.Fatalf("registered 被拒绝放行后标本发生变化: before=%+v after=%+v", registered, got)
	}

	// planned + 开放草稿：open_plan 优先拒绝。
	draftSpecimen := registerSpecimen(t, e, "PPS-REL-02")
	draftPlan := createPlan(t, e, draftSpecimen.ID)
	expectAPIError(t, e, http.MethodPost, "/specimens/"+draftSpecimen.ID+"/release", validRelease,
		http.StatusConflict, "open_plan_blocks_release")
	if got := getSpecimen(t, e, draftSpecimen.ID); got.Status != "planned" || got.Version != 2 {
		t.Fatalf("planned 阶段被拒绝放行后标本发生变化: %+v", got)
	}

	// preparing + 进行中方案：同样被开放方案挡住。
	approvePlan(t, e, draftPlan.ID, "curator-lin")
	logProgress(t, e, draftPlan.ID, 30)
	expectAPIError(t, e, http.MethodPost, "/specimens/"+draftSpecimen.ID+"/release", validRelease,
		http.StatusConflict, "open_plan_blocks_release")
	if got := getSpecimen(t, e, draftSpecimen.ID); got.Status != "preparing" || got.Version != 3 {
		t.Fatalf("preparing 阶段被拒绝放行后标本发生变化: %+v", got)
	}

	// 稳定且方案闭合：放行成功，标本 available，版本再进一步。
	logProgress(t, e, draftPlan.ID, 100)
	stabilized := getSpecimen(t, e, draftSpecimen.ID)
	if stabilized.Status != "stabilized" {
		t.Fatalf("前置状态错误，期望 stabilized，实际 %s", stabilized.Status)
	}
	release := releaseSpecimen(t, e, draftSpecimen.ID)
	if release.SpecimenID != draftSpecimen.ID || release.ApprovedBy != "keeper-zhou" {
		t.Fatalf("放行记录形状异常: %+v", release)
	}
	available := getSpecimen(t, e, draftSpecimen.ID)
	if available.Status != "available" || available.Version != 5 {
		t.Fatalf("放行后标本应为 available/版本5，实际 %s/版本%d",
			available.Status, available.Version)
	}

	// 放行记录永久不可覆盖：重复放行 409，标本冻结在 available/版本5。
	expectAPIError(t, e, http.MethodPost, "/specimens/"+draftSpecimen.ID+"/release", validRelease,
		http.StatusConflict, "release_exists")
	if got := getSpecimen(t, e, draftSpecimen.ID); !reflect.DeepEqual(got, available) {
		t.Fatalf("重复放行后标本被修改: before=%+v after=%+v", available, got)
	}

	// 不存在的标本：404。
	expectAPIError(t, e, http.MethodPost, "/specimens/specimen-missing/release", validRelease,
		http.StatusNotFound, "specimen_not_found")
}

func TestReleaseValidationFailureLeavesStateUntouched(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen, _ := driveToStabilized(t, e, "PPS-REL-INVALID")
	before := getSpecimen(t, e, specimen.ID)

	invalidCases := []struct {
		name    string
		request contracts.ReleaseRequest
	}{
		{"空借阅目的", contracts.ReleaseRequest{Purpose: "  ", Recipient: "研究组", ApprovedBy: "keeper-zhou"}},
		{"空接收方", contracts.ReleaseRequest{Purpose: "科研", Recipient: "", ApprovedBy: "keeper-zhou"}},
		{"空批准人", contracts.ReleaseRequest{Purpose: "科研", Recipient: "研究组", ApprovedBy: ""}},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/release", tc.request,
				http.StatusBadRequest, "required_field")
			if got := getSpecimen(t, e, specimen.ID); !reflect.DeepEqual(before, got) {
				t.Fatalf("非法放行被拒绝后标本发生变化: before=%+v after=%+v", before, got)
			}
		})
	}

	// 所有非法请求之后，一次合法放行仍应成功，证明确实没有提前写入过放行记录。
	releaseSpecimen(t, e, specimen.ID)
	if got := getSpecimen(t, e, specimen.ID); got.Status != "available" {
		t.Fatalf("合法放行应在多次非法尝试后成功，实际 %s", got.Status)
	}
}

func TestConcurrentReleaseHasSingleWinner(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen, _ := driveToStabilized(t, e, "PPS-REL-CONC")

	const workers = 8
	results := statusCounter{counts: map[int]int{}}
	runConcurrent(t, workers, func(int) {
		status, _ := e.jsonRequest(t, http.MethodPost, "/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{Purpose: "科研", Recipient: "研究组", ApprovedBy: "keeper-zhou"})
		results.record(status)
	})
	if got := results.get(http.StatusCreated); got != 1 {
		t.Fatalf("并发放行应恰好 1 次成功，实际 %d 次（分布 %v）", got, results.counts)
	}
	if got := results.get(http.StatusConflict); got != workers-1 {
		t.Fatalf("并发放行应有 %d 次冲突，实际 %d 次", workers-1, got)
	}
	if got := getSpecimen(t, e, specimen.ID); got.Status != "available" || got.Version != 5 {
		t.Fatalf("并发收敛后标本应为 available/版本5，实际 %s/版本%d", got.Status, got.Version)
	}
}

// ----------------------------------------------------------------------------
// 8. 综合：校验失败不改变任何实体或版本（完整快照前后比对）
// ----------------------------------------------------------------------------

func TestRejectedWritesLeaveNoHalfState(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)

	t.Run("非法登记不改仓储", func(t *testing.T) {
		expectAPIError(t, e, http.MethodPost, "/specimens", validRegister("!!"),
			http.StatusBadRequest, "invalid_catalog_number")
		if items := listSpecimens(t, e); len(items) != 0 {
			t.Fatalf("仓储应保持为空，实际 %d 条", len(items))
		}
	})

	t.Run("重复登记不改原实体", func(t *testing.T) {
		original := registerSpecimen(t, e, "PPS-SIDE-01")
		expectAPIError(t, e, http.MethodPost, "/specimens", validRegister("pps-side-01"),
			http.StatusConflict, "duplicate_catalog_number")
		if got := getSpecimen(t, e, original.ID); !reflect.DeepEqual(original, got) {
			t.Fatalf("冲突后原标本被修改: before=%+v after=%+v", original, got)
		}
	})

	t.Run("非法方案不改标本版本", func(t *testing.T) {
		specimen := registerSpecimen(t, e, "PPS-SIDE-02")
		before := getSpecimen(t, e, specimen.ID)
		bad := contracts.CreatePlanRequest{Objective: "目标", Method: "方法", TargetExposure: 101}
		expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans", bad,
			http.StatusBadRequest, "invalid_target_exposure")
		if got := getSpecimen(t, e, specimen.ID); !reflect.DeepEqual(before, got) {
			t.Fatalf("非法方案被拒绝后标本发生变化: before=%+v after=%+v", before, got)
		}
		if plans := listPlans(t, e, specimen.ID); len(plans) != 0 {
			t.Fatalf("非法方案不得落库，实际 %d 条", len(plans))
		}
	})

	t.Run("向未知标本建方案返回404", func(t *testing.T) {
		bad := contracts.CreatePlanRequest{Objective: "目标", Method: "方法", TargetExposure: 50}
		expectAPIError(t, e, http.MethodPost, "/specimens/ghost/plans", bad,
			http.StatusNotFound, "specimen_not_found")
	})

	t.Run("开放方案互斥拒绝不改版本", func(t *testing.T) {
		specimen := registerSpecimen(t, e, "PPS-SIDE-03")
		first := createPlan(t, e, specimen.ID)
		beforeSpecimen := getSpecimen(t, e, specimen.ID)
		beforePlan := getPlanSnapshot(t, e, first.ID)
		second := contracts.CreatePlanRequest{Objective: "另一个目标", Method: "其它方法", TargetExposure: 40}
		expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans", second,
			http.StatusConflict, "open_plan_exists")
		if got := getSpecimen(t, e, specimen.ID); !reflect.DeepEqual(beforeSpecimen, got) {
			t.Fatalf("互斥拒绝后标本发生变化: before=%+v after=%+v", beforeSpecimen, got)
		}
		if got := getPlanSnapshot(t, e, first.ID); !reflect.DeepEqual(beforePlan, got) {
			t.Fatalf("互斥拒绝后既有方案发生变化: before=%+v after=%+v", beforePlan, got)
		}
	})

	t.Run("未知方案的进度写入返回404", func(t *testing.T) {
		expectAPIError(t, e, http.MethodPost, "/plans/ghost/progress", progressRequest(10),
			http.StatusNotFound, "plan_not_found")
	})
}

// ----------------------------------------------------------------------------
// 9. 已声明但未在服务边界暴露的状态转换：草稿 -> cancelled
//
// PROJECT_SPEC 声明“草稿也可以进入 cancelled”，且开放方案会互斥新方案并阻塞放行。
// 当前公共路由中没有任何取消草稿的入口。此测试刻画当前真实边界（路由不存在 ->
// 404），以保证套件可重复跑绿；规则层面的缺口、影响与建议在测试报告中单独说明，
// 不在测试里复制或篡改被测实现。
// ----------------------------------------------------------------------------

func TestDraftCancellation_DeclaredTransitionHasNoRoute(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	specimen := registerSpecimen(t, e, "PPS-CANCEL-01")
	plan := createPlan(t, e, specimen.ID)

	status, data := e.jsonRequest(t, http.MethodPost, "/plans/"+plan.ID+"/cancel", struct{}{})
	t.Logf("已知缺口：草稿取消在公共路由上不可达，POST /plans/{id}/cancel -> %d %s",
		status, string(bytes.TrimSpace(data)))
	if status != http.StatusNotFound {
		t.Fatalf("当前实现未声明取消路由，期望 404，实际 %d", status)
	}

	// 取消路径缺失的实际后果：草稿永远开放，该标本无法再建方案。
	expectAPIError(t, e, http.MethodPost, "/specimens/"+specimen.ID+"/plans",
		contracts.CreatePlanRequest{Objective: "替代方案", Method: "其它方法", TargetExposure: 50},
		http.StatusConflict, "open_plan_exists")
}
