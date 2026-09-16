//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// upsertCapturingQuotaRepo 实现 service.UserPlatformQuotaRepository，捕获 UpsertForUser 调用。
type upsertCapturingQuotaRepo struct {
	service.UserPlatformQuotaRepository
	listRecords []service.UserPlatformQuotaRecord
	listErr     error
	upsertCalls []upsertCall
	upsertErr   error
	resetCalls  []resetCall
	resetErr    error
}

type upsertCall struct {
	userID  int64
	records []service.UserPlatformQuotaRecord
}
type resetCall struct {
	userID   int64
	platform string
	window   string
	newStart time.Time
}

func (r *upsertCapturingQuotaRepo) ListByUser(_ context.Context, _ int64) ([]service.UserPlatformQuotaRecord, error) {
	return r.listRecords, r.listErr
}
func (r *upsertCapturingQuotaRepo) UpsertForUser(_ context.Context, userID int64, records []service.UserPlatformQuotaRecord) error {
	cloned := make([]service.UserPlatformQuotaRecord, len(records))
	copy(cloned, records)
	r.upsertCalls = append(r.upsertCalls, upsertCall{userID: userID, records: cloned})
	return r.upsertErr
}
func (r *upsertCapturingQuotaRepo) ResetExpiredWindow(_ context.Context, userID int64, platform string, window string, newStart time.Time) error {
	r.resetCalls = append(r.resetCalls, resetCall{userID, platform, window, newStart})
	return r.resetErr
}

// billingCacheStub 实现 service.BillingCache 中本测试关心的 Delete 方法；其他方法 panic。
type billingCacheStub struct {
	service.BillingCache
	deleteCalls []deleteCall
	deleteErr   error
}

type deleteCall struct {
	userID   int64
	platform string
}

func (b *billingCacheStub) DeleteUserPlatformQuotaCache(_ context.Context, userID int64, platform string) error {
	b.deleteCalls = append(b.deleteCalls, deleteCall{userID, platform})
	return b.deleteErr
}

func buildTestHandler(repo service.UserPlatformQuotaRepository, cache service.BillingCache) *UserHandler {
	return &UserHandler{
		userPlatformQuotaRepo: repo,
		billingCache:          cache,
		adminService:          newStubAdminService(),
	}
}

func putReq(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Params = []gin.Param{{Key: "id", Value: "42"}}
	return c, w
}

func TestUpdateUserPlatformQuotas_Success(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)

	// 覆盖全部允许平台（AllowedQuotaPlatforms 为单一权威来源），前两个带非 nil 上限。
	quotaBodies := make([]string, 0, len(service.AllowedQuotaPlatforms))
	for i, p := range service.AllowedQuotaPlatforms {
		switch i {
		case 0:
			quotaBodies = append(quotaBodies, `{"platform":"`+p+`","daily_limit_usd":10.0,"weekly_limit_usd":null,"monthly_limit_usd":100.0}`)
		case 1:
			quotaBodies = append(quotaBodies, `{"platform":"`+p+`","daily_limit_usd":80.0,"weekly_limit_usd":300.0,"monthly_limit_usd":null}`)
		default:
			quotaBodies = append(quotaBodies, `{"platform":"`+p+`","daily_limit_usd":null,"weekly_limit_usd":null,"monthly_limit_usd":null}`)
		}
	}
	body := `{"quotas":[` + strings.Join(quotaBodies, ",") + `]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(repo.upsertCalls) != 1 {
		t.Fatalf("UpsertForUser should be called once, got %d", len(repo.upsertCalls))
	}
	// upsert 记录数 = 请求体中至少配置了一档限额的平台数；三档全空的平台不落库
	// （handler 里 rec.HasAnyLimit() 过滤 + repository.configuredRecords 二次过滤），
	// 未进入列表的平台由 UpsertForUser 的 softDeleteMissingPlatforms 软删。
	// 本 fork 的请求体按 AllowedQuotaPlatforms 全量生成（上游同名用例手写 5 个平台），
	// 但只有 index 0/1（anthropic/openai）带非 nil 上限，因此期望值仍是上游的 2，
	// 不是 len(AllowedQuotaPlatforms)：0.2.5 起「行不存在 == 不限额」。
	if repo.upsertCalls[0].userID != 42 || len(repo.upsertCalls[0].records) != 2 {
		t.Errorf("unexpected upsert call: %+v", repo.upsertCalls[0])
	}
	for _, r := range repo.upsertCalls[0].records {
		if r.Platform != "anthropic" && r.Platform != "openai" {
			t.Errorf("platform %q has no configured limit and must not be upserted", r.Platform)
		}
	}
	// 缓存失效：按全部允许平台统一失效（含 kimi/zhipu/deepseek）。
	if len(cache.deleteCalls) != len(service.AllowedQuotaPlatforms) {
		t.Errorf("expected %d cache delete calls, got %d: %+v", len(service.AllowedQuotaPlatforms), len(cache.deleteCalls), cache.deleteCalls)
	}
}

// TestUpdateUserPlatformQuotas_AllUnlimitedClearsRows 锁定：全部平台三档全空等价于清空，
// UpsertForUser 收到空列表（软删该用户所有活跃行），且 0 = 显式禁用仍算已配置。
func TestUpdateUserPlatformQuotas_AllUnlimitedClearsRows(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)

	body := `{"quotas":[
		{"platform":"anthropic","daily_limit_usd":null,"weekly_limit_usd":null,"monthly_limit_usd":null},
		{"platform":"openai"}
	]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(repo.upsertCalls) != 1 {
		t.Fatalf("UpsertForUser should be called once, got %d", len(repo.upsertCalls))
	}
	if len(repo.upsertCalls[0].records) != 0 {
		t.Errorf("all-unlimited input must upsert zero records, got %+v", repo.upsertCalls[0].records)
	}

	repo = &upsertCapturingQuotaRepo{}
	h = buildTestHandler(repo, &billingCacheStub{})
	c, w = putReq(t, `{"quotas":[{"platform":"gemini","daily_limit_usd":0}]}`)
	h.UpdateUserPlatformQuotas(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(repo.upsertCalls) != 1 || len(repo.upsertCalls[0].records) != 1 {
		t.Fatalf("zero limit is a configured limit and must be upserted: %+v", repo.upsertCalls)
	}
	if r := repo.upsertCalls[0].records[0]; r.Platform != "gemini" || r.DailyLimitUSD == nil || *r.DailyLimitUSD != 0 {
		t.Errorf("unexpected record: %+v", r)
	}
}

func TestUpdateUserPlatformQuotas_RejectsDuplicatePlatform(t *testing.T) {
	h := buildTestHandler(&upsertCapturingQuotaRepo{}, &billingCacheStub{})
	body := `{"quotas":[
		{"platform":"anthropic","daily_limit_usd":1},
		{"platform":"anthropic","daily_limit_usd":2}
	]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateUserPlatformQuotas_RejectsInvalidPlatform(t *testing.T) {
	h := buildTestHandler(&upsertCapturingQuotaRepo{}, &billingCacheStub{})
	body := `{"quotas":[{"platform":"unknown","daily_limit_usd":1}]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateUserPlatformQuotas_RejectsNegativeLimit(t *testing.T) {
	h := buildTestHandler(&upsertCapturingQuotaRepo{}, &billingCacheStub{})
	body := `{"quotas":[{"platform":"anthropic","daily_limit_usd":-1}]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateUserPlatformQuotas_RejectsTooManyEntries(t *testing.T) {
	// 长度守卫是 len(req.Quotas) > len(service.AllowedQuotaPlatforms)（现为 10）。
	// 原用例只发 6 条，6 > 10 为假、守卫一行都没跑到，它返回 400 全靠请求体里
	// anthropic 出现两次命中了后面的 duplicate 分支——名单每涨一个平台就更失真。
	// 改为按名单派生 len+1 条（末尾重复最后一个平台以越界），并断言错误串，
	// 与 RejectsDuplicatePlatform 明确区分开。
	h := buildTestHandler(&upsertCapturingQuotaRepo{}, &billingCacheStub{})
	entries := make([]string, 0, len(service.AllowedQuotaPlatforms)+1)
	for _, p := range service.AllowedQuotaPlatforms {
		entries = append(entries, `{"platform":"`+p+`"}`)
	}
	last := service.AllowedQuotaPlatforms[len(service.AllowedQuotaPlatforms)-1]
	entries = append(entries, `{"platform":"`+last+`"}`)
	body := `{"quotas":[` + strings.Join(entries, ",") + `]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "quotas length must be") {
		t.Errorf("应命中长度上限守卫而非 duplicate 分支，实际响应: %s", w.Body.String())
	}
}

func TestUpdateUserPlatformQuotas_ReturnsLatestState(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{
		listRecords: []service.UserPlatformQuotaRecord{
			{UserID: 42, Platform: "anthropic"},
		},
	}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)

	body := `{"quotas":[{"platform":"anthropic","daily_limit_usd":10}]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if !strings.Contains(w.Body.String(), `"platform_quotas"`) {
		t.Errorf("response should contain platform_quotas array: %s", w.Body.String())
	}
}

// ───────── T4: Reset 测试 ─────────

func postReq(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Params = []gin.Param{{Key: "id", Value: "42"}}
	return c, w
}

func TestResetUserPlatformQuotaWindow_Success(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)
	body := `{"platform":"anthropic","window":"daily"}`
	c, w := postReq(t, body)
	h.ResetUserPlatformQuotaWindow(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(repo.resetCalls) != 1 {
		t.Fatalf("ResetExpiredWindow should be called once, got %d", len(repo.resetCalls))
	}
	if repo.resetCalls[0].userID != 42 ||
		repo.resetCalls[0].platform != "anthropic" ||
		repo.resetCalls[0].window != "daily" {
		t.Errorf("unexpected reset call: %+v", repo.resetCalls[0])
	}
	if len(cache.deleteCalls) != 1 ||
		cache.deleteCalls[0].userID != 42 ||
		cache.deleteCalls[0].platform != "anthropic" {
		t.Errorf("expected 1 cache delete for anthropic, got %+v", cache.deleteCalls)
	}
}

func TestResetUserPlatformQuotaWindow_RejectsInvalidWindow(t *testing.T) {
	h := buildTestHandler(&upsertCapturingQuotaRepo{}, &billingCacheStub{})
	c, w := postReq(t, `{"platform":"anthropic","window":"yearly"}`)
	h.ResetUserPlatformQuotaWindow(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestResetUserPlatformQuotaWindow_RejectsInvalidPlatform(t *testing.T) {
	h := buildTestHandler(&upsertCapturingQuotaRepo{}, &billingCacheStub{})
	c, w := postReq(t, `{"platform":"unknown","window":"daily"}`)
	h.ResetUserPlatformQuotaWindow(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestResetUserPlatformQuotaWindow_NotFound(t *testing.T) {
	// handler 检查 service.ErrUserPlatformQuotaNotFound（由 adapter 包装而来）
	repo := &upsertCapturingQuotaRepo{resetErr: service.ErrUserPlatformQuotaNotFound}
	h := buildTestHandler(repo, &billingCacheStub{})
	c, w := postReq(t, `{"platform":"anthropic","window":"daily"}`)
	h.ResetUserPlatformQuotaWindow(c)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateUserPlatformQuotas_JSONErrorOnRepoFailure(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{upsertErr: errors.New("db down")}
	cache := &billingCacheStub{}
	h := buildTestHandler(repo, cache)
	body := `{"quotas":[{"platform":"anthropic","daily_limit_usd":10}]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if w.Code < 500 {
		t.Errorf("expected 5xx, got %d", w.Code)
	}
	// 返回 JSON 错误响应
	var body2 map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body2); err != nil {
		t.Errorf("expected JSON error body, got: %s", w.Body.String())
	}
}

func TestUpdateUserPlatformQuotas_UserNotFound(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{}
	cache := &billingCacheStub{}
	adminSvc := newStubAdminService()
	adminSvc.getUserErr = service.ErrUserNotFound
	h := &UserHandler{
		userPlatformQuotaRepo: repo,
		billingCache:          cache,
		adminService:          adminSvc,
	}
	body := `{"quotas":[{"platform":"anthropic","daily_limit_usd":10}]}`
	c, w := putReq(t, body)
	h.UpdateUserPlatformQuotas(c)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when user not found, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResetUserPlatformQuotaWindow_UserNotFound(t *testing.T) {
	repo := &upsertCapturingQuotaRepo{}
	cache := &billingCacheStub{}
	adminSvc := newStubAdminService()
	adminSvc.getUserErr = service.ErrUserNotFound
	h := &UserHandler{
		userPlatformQuotaRepo: repo,
		billingCache:          cache,
		adminService:          adminSvc,
	}
	c, w := postReq(t, `{"platform":"anthropic","window":"daily"}`)
	h.ResetUserPlatformQuotaWindow(c)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when user not found, got %d: %s", w.Code, w.Body.String())
	}
}
