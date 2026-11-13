package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---- 网关侧计数 ----

type fieldWatchStub struct {
	mu       sync.Mutex
	coverage codexFieldWatchCoverage
	lookups  int
	confirms int
	result   codexFieldWatchConfirmResult
}

func (f *fieldWatchStub) lookup(context.Context, int64) (codexFieldWatchCoverage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	return f.coverage, nil
}

func (f *fieldWatchStub) confirm(context.Context, int64) codexFieldWatchConfirmResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms++
	return f.result
}

func (f *fieldWatchStub) confirmCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.confirms
}

func syncFieldWatcher(stub *fieldWatchStub) *codexAstraWatcher {
	w := newCodexAstraWatcher(stub.lookup, stub.confirm)
	w.spawn = func(run func()) { run() }
	return w
}

func fieldWatchState(t *testing.T, w *codexAstraWatcher, accountID int64) QualityFieldWatchStatus {
	t.Helper()
	items := w.statuses([]int64{accountID})
	require.Len(t, items, 1)
	return items[0]
}

func TestFieldWatchConfirmsOnlyAfterThreeConsecutiveMisses(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}, result: codexFieldWatchConfirmResult{Outcome: codexFieldWatchUnknown}}
	w := syncFieldWatcher(stub)
	ctx := context.Background()

	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	require.Zero(t, stub.confirmCount(), "two misses must not trigger the candy confirmation")
	require.Equal(t, 2, fieldWatchState(t, w, 57).Streak)

	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	require.Equal(t, 1, stub.confirmCount(), "the third consecutive miss triggers exactly one confirmation")
	state := fieldWatchState(t, w, 57)
	require.EqualValues(t, 3, state.Observed)
	require.Zero(t, state.Streak, "an inconclusive confirmation resets the streak")
	require.Equal(t, codexFieldWatchUnknown, state.Verdict)
	require.False(t, state.Settled, "an inconclusive confirmation is not a verdict")
	require.Equal(t, 1, stub.lookups, "the per-account coverage is cached")

	// 无法判断后冷却 5 分钟；冷却过后要重新连续 3 次才再试。
	clock := time.Now()
	w.now = func() time.Time { return clock }
	w.mu.Lock()
	w.states[57].cooldownUntil = clock.Add(codexFieldWatchUnknownCooldown)
	w.mu.Unlock()
	for i := 0; i < 5; i++ {
		w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	}
	require.Equal(t, 1, stub.confirmCount(), "no automatic confirmation during the inconclusive cooldown")
	clock = clock.Add(codexFieldWatchUnknownCooldown + time.Second)
	w.mu.Lock()
	w.states[57].streak = 0
	w.cache = map[int64]codexFieldWatchCacheEntry{}
	w.mu.Unlock()
	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	require.Equal(t, 1, stub.confirmCount())
	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	require.Equal(t, 2, stub.confirmCount())
}

func TestFieldWatchPassCooldownLimitsRepeatedConfirms(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}, result: codexFieldWatchConfirmResult{Outcome: codexFieldWatchHealthy, Reason: qualityFieldWatchMismatchReason}}
	w := syncFieldWatcher(stub)
	clock := time.Now()
	w.now = func() time.Time { return clock }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	}
	require.Equal(t, 1, stub.confirmCount())
	state := fieldWatchState(t, w, 57)
	require.NotNil(t, state.CooldownUntil, "a passing confirmation starts the cooldown")
	require.False(t, state.Settled)

	// 冷却期内一直不带字段也不再复核。
	clock = clock.Add(14 * time.Minute)
	for i := 0; i < 30; i++ {
		w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	}
	require.Equal(t, 1, stub.confirmCount(), "an account that keeps answering 21 is not re-tested every 3 requests")

	// 冷却过后下一次未见字段立即复核。
	clock = clock.Add(2 * time.Minute)
	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	require.Equal(t, 2, stub.confirmCount())
}

func TestFieldWatchMarkerClearsStreak(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}}
	w := syncFieldWatcher(stub)
	ctx := context.Background()
	for _, saw := range []bool{false, false, true, false, false} {
		w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", saw)
	}
	require.Zero(t, stub.confirmCount(), "a request carrying safety_buffering breaks the consecutive count")
	state := fieldWatchState(t, w, 57)
	require.Equal(t, 2, state.Streak)
	require.EqualValues(t, 5, state.Observed)
	require.NotNil(t, state.LastSeenAt)
	require.NotNil(t, state.LastMissAt)
	require.Equal(t, codexFieldWatchHealthy, state.Verdict)
}

func TestFieldWatchCountsOnlyRuleModelsAndCoveredAccounts(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}}
	w := syncFieldWatcher(stub)
	ctx := context.Background()
	for _, model := range []string{"gpt-6-luna", "gpt-5.6", "gpt-image-2", "codex-auto-review", ""} {
		w.observe(ctx, 57, model, model, false)
	}
	require.Empty(t, w.statuses(nil), "requests for other models must not be counted")

	// 原始请求名或映射后的名字任一命中即可，大小写不敏感。
	w.observe(ctx, 57, "my-astra-alias", "gpt-6-astra", false)
	w.observe(ctx, 57, " GPT-6-Astra ", "upstream-name", false)
	require.Equal(t, 2, fieldWatchState(t, w, 57).Streak)

	uncovered := syncFieldWatcher(&fieldWatchStub{})
	for i := 0; i < 5; i++ {
		uncovered.observe(ctx, 58, "gpt-6-astra", "gpt-6-astra", false)
	}
	require.Empty(t, uncovered.statuses(nil), "accounts without a field-watch rule are never counted")
}

func TestFieldWatchSkipsQualityProbeTraffic(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}}
	w := syncFieldWatcher(stub)
	probeCtx := context.WithValue(context.Background(), qualityProbeContextKey{}, true)
	for i := 0; i < 5; i++ {
		w.observe(probeCtx, 57, "gpt-6-astra", "gpt-6-astra", false)
	}
	require.Zero(t, stub.lookups)
	require.Empty(t, w.statuses(nil), "the confirmation's own candy request must not feed the watcher")
}

func TestFieldWatchConcurrencyCapKeepsStreak(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}, result: codexFieldWatchConfirmResult{Outcome: codexFieldWatchHealthy}}
	w := syncFieldWatcher(stub)
	for i := 0; i < codexFieldWatchMaxConfirms; i++ {
		w.slots <- struct{}{}
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	}
	require.Zero(t, stub.confirmCount(), "a full confirmation pool must not start another run")
	state := fieldWatchState(t, w, 57)
	require.False(t, state.Confirming)
	require.Equal(t, 3, state.Streak, "the streak is kept so the next miss retries")
	for i := 0; i < codexFieldWatchMaxConfirms; i++ {
		<-w.slots
	}
	w.observe(ctx, 57, "gpt-6-astra", "gpt-6-astra", false)
	require.Equal(t, 1, stub.confirmCount())
}

// ---- 只统计正常结束的流（直通通道和原生通道） ----

func fieldWatchSSE(payload string) string { return "data: " + payload + "\n\n" }

func fieldWatchStream(marker bool, terminal string) string {
	extra := ""
	if marker {
		extra = `,"safety_buffering":false`
	}
	parts := []string{
		fieldWatchSSE(`{"type":"response.created","response":{"id":"resp_fw","model":"gpt-6-astra"}` + extra + `}`),
		fieldWatchSSE(`{"type":"response.output_text.delta","delta":"hi"` + extra + `}`),
	}
	switch terminal {
	case "completed":
		parts = append(parts, fieldWatchSSE(`{"type":"response.completed","response":{"id":"resp_fw","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`+extra+`}`))
	case "failed":
		parts = append(parts, fieldWatchSSE(`{"type":"response.failed","response":{"id":"resp_fw","status":"failed","error":{"code":"server_error","message":"upstream overloaded"}}}`))
	case "none":
	}
	return strings.Join(parts, "")
}

func runFieldWatchStream(t *testing.T, svc *OpenAIGatewayService, passthrough bool, model, body string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	account := &Account{ID: 57, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	if passthrough {
		_, _ = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), model, model)
		return
	}
	_, _ = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), model, model)
}

func TestFieldWatchCountsOnlyCompletedStreams(t *testing.T) {
	for _, passthrough := range []bool{true, false} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}}
			w := syncFieldWatcher(stub)
			svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector(), codexAstraWatcher: w}

			// 上游报错、没收到终态就断开的流都不计数。
			runFieldWatchStream(t, svc, passthrough, "gpt-6-astra", fieldWatchStream(false, "failed"))
			runFieldWatchStream(t, svc, passthrough, "gpt-6-astra", fieldWatchStream(false, "none"))
			runFieldWatchStream(t, svc, passthrough, "gpt-6-astra", "")
			require.Empty(t, w.statuses(nil), "only streams that reached a successful terminal are counted")

			// 正常结束、带字段 → 满血；正常结束、不带字段 → 计数 +1。
			runFieldWatchStream(t, svc, passthrough, "gpt-6-astra", fieldWatchStream(true, "completed"))
			state := fieldWatchState(t, w, 57)
			require.EqualValues(t, 1, state.Observed)
			require.Zero(t, state.Streak)
			require.Equal(t, codexFieldWatchHealthy, state.Verdict)

			runFieldWatchStream(t, svc, passthrough, "gpt-6-astra", fieldWatchStream(false, "completed"))
			runFieldWatchStream(t, svc, passthrough, "gpt-6-luna", fieldWatchStream(false, "completed"))
			state = fieldWatchState(t, w, 57)
			require.EqualValues(t, 2, state.Observed, "another model's completed stream is not counted")
			require.Equal(t, 1, state.Streak)
		})
	}
}

// ---- 复核走规则执行路径 ----

// fieldWatchPlanRepo mirrors the database pieces the field-watch path relies on:
// lease claims, ApplyQualityOutcome's group move, and the quarantine record.
type fieldWatchPlanRepo struct {
	ScheduledTestPlanRepository
	mu        sync.Mutex
	plan      ScheduledTestPlan
	claims    []string
	nextRuns  []time.Time
	outcomes  []string
	action    string // forced ApplyQualityOutcome result (skips the group move)
	finished  int
	triggered int
	released  int
	// account groups and this rule's quarantine record
	groups     []int64
	state      string
	stateAdded []int64
	stateRem   []int64
}

func (r *fieldWatchPlanRepo) snapshot() *ScheduledTestPlan {
	p := r.plan
	if r.plan.PelicanConfig != nil {
		cfg := *r.plan.PelicanConfig
		if cfg.Quality != nil {
			q := *cfg.Quality
			cfg.Quality = &q
		}
		cfg.ModelIDs = append([]string(nil), cfg.ModelIDs...)
		p.PelicanConfig = &cfg
	}
	return &p
}
func (r *fieldWatchPlanRepo) ListByAccountID(_ context.Context, accountID int64) ([]*ScheduledTestPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if accountID != r.plan.AccountID {
		return nil, nil
	}
	return []*ScheduledTestPlan{r.snapshot()}, nil
}
func (r *fieldWatchPlanRepo) GetByID(context.Context, int64) (*ScheduledTestPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot(), nil
}
func (r *fieldWatchPlanRepo) TriggerQuality(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.triggered++
	return nil
}

// ClaimPelican mirrors the SQL: due plans, or field-watch confirmations by trigger source.
func (r *fieldWatchPlanRepo) ClaimPelican(_ context.Context, p *ScheduledTestPlan, now, _, next time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	due := r.plan.NextRunAt != nil && !r.plan.NextRunAt.After(now)
	if !due && (!IsQualityFieldWatchTrigger(p.TriggerSource) || !isOpenAICodexFieldWatchPlan(r.plan.PelicanConfig)) {
		return false, nil
	}
	r.claims = append(r.claims, p.TriggerSource)
	r.nextRuns = append(r.nextRuns, next)
	r.plan.NextRunAt = &next
	return true, nil
}
func (r *fieldWatchPlanRepo) ApplyQualityOutcome(_ context.Context, p *ScheduledTestPlan, _ time.Time, outcome string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
	if r.action != "" {
		return r.action, nil
	}
	q := p.PelicanConfig.Quality
	switch {
	case q.Action == QualityActionObserveOnly:
		r.state = ""
		return "observed", nil
	case r.state != "":
		return "already_quarantined", nil
	}
	remove := map[int64]bool{}
	for _, id := range q.RemoveGroupIDs {
		remove[id] = true
	}
	var kept, removed []int64
	for _, id := range r.groups {
		if remove[id] {
			removed = append(removed, id)
		} else {
			kept = append(kept, id)
		}
	}
	var added []int64
	for _, id := range q.AddGroupIDs {
		if !containsInt64(kept, id) {
			kept = append(kept, id)
			added = append(added, id)
		}
	}
	if len(removed) == 0 && len(added) == 0 {
		return "no_change", nil
	}
	r.groups, r.state, r.stateAdded, r.stateRem = kept, "remove_groups", added, removed
	return "groups_removed", nil
}
func (r *fieldWatchPlanRepo) FinishPelican(context.Context, int64, time.Time, time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished++
	return nil
}
func (r *fieldWatchPlanRepo) QualityGroupMembership(context.Context, int64, int64) (QualityGroupMembership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return QualityGroupMembership{StateAction: r.state, Added: append([]int64(nil), r.stateAdded...), Removed: append([]int64(nil), r.stateRem...), Current: append([]int64(nil), r.groups...)}, nil
}
func (r *fieldWatchPlanRepo) ReleaseRestoredQualityGroups(_ context.Context, p *ScheduledTestPlan) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != "remove_groups" || QualityGroupsStillQuarantined(r.groups, int64Union(p.PelicanConfig.Quality.AddGroupIDs, r.stateAdded), int64Union(p.PelicanConfig.Quality.RemoveGroupIDs, r.stateRem)) {
		return false, nil
	}
	r.state, r.stateAdded, r.stateRem = "", nil, nil
	r.released++
	return true, nil
}
func (r *fieldWatchPlanRepo) setGroups(groups ...int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groups = groups
}

type fieldWatchResultRepo struct {
	ScheduledTestResultRepository
	mu      sync.Mutex
	results []*ScheduledTestResult
}

func (r *fieldWatchResultRepo) Create(_ context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *result
	copy.ID = int64(len(r.results) + 1)
	r.results = append(r.results, &copy)
	return &copy, nil
}
func (r *fieldWatchResultRepo) PruneOldResults(context.Context, int64, int) error { return nil }

// ListByPlanID returns newest first, like the database query.
func (r *fieldWatchResultRepo) ListByPlanID(_ context.Context, _ int64, limit int, _ ...bool) ([]*ScheduledTestResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*ScheduledTestResult, 0, len(r.results))
	for i := len(r.results) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, r.results[i])
	}
	return out, nil
}

// 线上配置：移出 #16「GPT-速刷-AUTO」，移入 #9「GPT-降智」。
const (
	fieldWatchHostGroup     int64 = 16
	fieldWatchDegradedGroup int64 = 9
)

func fieldWatchRulePlan() ScheduledTestPlan {
	next := time.Now().AddDate(100, 0, 0)
	return ScheduledTestPlan{ID: 9, AccountID: 57, ModelID: "gpt-6-astra", CronExpression: "*/30 * * * *", Enabled: true, MaxResults: 100, NextRunAt: &next,
		PelicanConfig: &PelicanTestConfig{QuestionKind: OpenAICodexFieldWatchQuestionKind, ReasoningEffort: "xhigh", ParallelCount: 1, ModelID: "gpt-6-astra",
			Quality: &QualityPolicy{Action: "remove_groups", RemoveGroupIDs: []int64{fieldWatchHostGroup}, AddGroupIDs: []int64{fieldWatchDegradedGroup}}}}
}

type fieldWatchHarness struct {
	plans   *fieldWatchPlanRepo
	results *fieldWatchResultRepo
	runner  *ScheduledTestRunnerService
	watcher *codexAstraWatcher
	clock   time.Time
	mu      sync.Mutex
	asked   []*PelicanTestConfig
	answer  func() (*ScheduledTestResult, error)
}

func newFieldWatchHarness(t *testing.T, answer string) *fieldWatchHarness {
	t.Helper()
	h := &fieldWatchHarness{plans: &fieldWatchPlanRepo{plan: fieldWatchRulePlan(), groups: []int64{fieldWatchHostGroup}}, results: &fieldWatchResultRepo{}, clock: time.Now()}
	h.runner = &ScheduledTestRunnerService{planRepo: h.plans, scheduledSvc: NewScheduledTestService(h.plans, h.results)}
	h.answer = func() (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "success", ResponseText: answer, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	h.runner.runPelican = func(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
		require.Equal(t, true, ctx.Value(qualityProbeContextKey{}), "the confirmation request must carry the probe marker")
		require.Equal(t, int64(57), accountID)
		require.Equal(t, "gpt-6-astra", model)
		copy := *cfg
		h.mu.Lock()
		h.asked = append(h.asked, &copy)
		h.mu.Unlock()
		return h.answer()
	}
	h.watcher = h.newWatcher()
	h.runner.fieldWatch = h.watcher
	h.runner.fieldWatchSpawn = func(run func()) { run() }
	return h
}

func (h *fieldWatchHarness) newWatcher() *codexAstraWatcher {
	w := newCodexAstraWatcher(h.runner.fieldWatchCoverage, h.runner.runFieldWatchAccount)
	w.spawn = func(run func()) { run() }
	w.now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.clock
	}
	return w
}

// advance moves the watcher clock forward (e.g. past a confirmation cooldown).
func (h *fieldWatchHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

// expireCache moves past the 30-second coverage cache.
func (h *fieldWatchHarness) expireCache() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(codexFieldWatchCacheTTL + time.Second)
}

func (h *fieldWatchHarness) observe(w *codexAstraWatcher, n int, marker bool) {
	for i := 0; i < n; i++ {
		w.observe(context.Background(), 57, "gpt-6-astra", "gpt-6-astra", marker)
	}
}

func (h *fieldWatchHarness) misses(n int) { h.observe(h.watcher, n, false) }

func (h *fieldWatchHarness) askedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

func TestFieldWatchWrongCandyMovesAccountAndStopsWatching(t *testing.T) {
	h := newFieldWatchHarness(t, `{"answer":29}`)
	h.misses(3)

	require.Equal(t, 1, h.askedCount())
	asked := h.asked[0]
	require.Equal(t, "candy", asked.QuestionKind, "the confirmation runs the built-in candy question")
	require.NotEmpty(t, asked.CandyVariant, "every confirmation renders a fresh randomized candy prompt")
	require.NotEqual(t, CandyPrompt, asked.Prompt)
	require.Equal(t, "xhigh", asked.ReasoningEffort, "the rule's reasoning effort is used")
	require.Equal(t, "21", asked.Quality.ExpectedAnswer)
	require.Nil(t, asked.Quality.Judge)
	require.Equal(t, 1, asked.ParallelCount)

	require.Equal(t, []string{"failed"}, h.plans.outcomes, "a wrong answer is handled exactly like a scheduled candy failure")
	require.Equal(t, []int64{fieldWatchDegradedGroup}, h.plans.groups, "moved out of #16 and into #9")
	require.Equal(t, []string{qualityFieldWatchSource}, h.plans.claims)
	require.True(t, h.plans.nextRuns[0].After(time.Now().AddDate(50, 0, 0)), "claiming keeps the rule out of the schedule")
	require.Len(t, h.results.results, 1, "the confirmation is saved to the rule's history")
	saved := h.results.results[0]
	require.Equal(t, qualityFieldWatchSource, saved.PelicanConfig.TriggerSource)
	require.Equal(t, asked.CandyVariant, saved.PelicanConfig.CandyVariant, "the record carries the prompt signature")
	require.Equal(t, "failed", saved.Status)
	require.Equal(t, "answer_mismatch", saved.ErrorMessage)
	require.Equal(t, "groups_removed", saved.QualityAction)
	require.False(t, saved.PelicanConfig.FieldWatchMismatch)
	require.Equal(t, OpenAICodexFieldWatchQuestionKind, h.plans.plan.PelicanConfig.QuestionKind, "the stored rule is not rewritten")

	state := fieldWatchState(t, h.watcher, 57)
	require.True(t, state.Settled)
	require.Equal(t, codexFieldWatchDegraded, state.Verdict)
	observed := state.Observed

	// 判降智以后：不再看字段、不再复测，看到字段也不解除（含缓存过期后按数据库重新推）。
	h.misses(6)
	h.observe(h.watcher, 2, true)
	h.expireCache()
	h.observe(h.watcher, 1, true)
	h.misses(3)
	require.Equal(t, 1, h.askedCount(), "a confirmed degraded account is not re-tested")
	state = fieldWatchState(t, h.watcher, 57)
	require.True(t, state.Settled, "safety_buffering does not lift the stop")
	require.Equal(t, observed, state.Observed, "fields are no longer looked at")

	// 只把号放回 #16 但还留在 #9：仍算隔离中。
	h.plans.setGroups(fieldWatchHostGroup, fieldWatchDegradedGroup)
	h.expireCache()
	h.misses(3)
	require.Equal(t, 1, h.askedCount())

	// 人工放回承载分组（离开 #9）→ 缓存过期后重新看护；再次答错会重新移出（过期隔离记录先丢掉）。
	h.plans.setGroups(fieldWatchHostGroup)
	h.expireCache()
	h.misses(2)
	require.False(t, fieldWatchState(t, h.watcher, 57).Settled)
	require.Equal(t, 1, h.askedCount())
	h.misses(1)
	require.Equal(t, 2, h.askedCount())
	require.Equal(t, 1, h.plans.released, "the stale quarantine record is released before quarantining again")
	require.Equal(t, []string{"groups_removed", "groups_removed"}, []string{h.results.results[0].QualityAction, h.results.results[1].QualityAction})
	require.Equal(t, []int64{fieldWatchDegradedGroup}, h.plans.groups)
	require.True(t, fieldWatchState(t, h.watcher, 57).Settled)
}

func TestFieldWatchCorrectCandyOnlyWarns(t *testing.T) {
	h := newFieldWatchHarness(t, `{"answer":21}`)
	h.misses(3)

	require.Equal(t, 1, h.askedCount())
	require.Empty(t, h.plans.outcomes, "a correct answer never calls ApplyQualityOutcome")
	require.Equal(t, []int64{fieldWatchHostGroup}, h.plans.groups)
	require.Len(t, h.results.results, 1)
	saved := h.results.results[0]
	require.Equal(t, "success", saved.Status)
	require.Equal(t, "no_change", saved.QualityAction)
	require.Equal(t, qualityFieldWatchSource, saved.PelicanConfig.TriggerSource)
	require.NotEmpty(t, saved.PelicanConfig.CandyVariant)
	require.True(t, saved.PelicanConfig.FieldWatchMismatch, "the record shows field says degraded but candy passed")
	require.Contains(t, saved.QualityJudgment.Reason, "糖果题答对")
	require.Contains(t, saved.QualityJudgment.Reason, "未处置")
	encoded, err := json.Marshal(saved.PelicanConfig)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"field_watch_mismatch":true`)
	require.False(t, h.plans.plan.PelicanConfig.FieldWatchMismatch, "the warning lives only on the result")

	state := fieldWatchState(t, h.watcher, 57)
	require.False(t, state.Settled)
	require.Zero(t, state.Streak)
	require.Equal(t, codexFieldWatchHealthy, state.Verdict)
	require.Contains(t, state.Reason, "未处置")

	h.misses(3)
	require.Equal(t, 1, h.askedCount(), "a passing confirmation starts a 15-minute cooldown")
	h.advance(codexFieldWatchPassCooldown + time.Second)
	h.misses(1)
	require.Equal(t, 2, h.askedCount(), "a passing confirmation does not stop monitoring")
	for _, asked := range h.asked {
		require.NotEmpty(t, asked.CandyVariant, "each confirmation renders its own randomized prompt")
	}
}

func TestFieldWatchInconclusiveConfirmTakesNoAction(t *testing.T) {
	h := newFieldWatchHarness(t, "")
	h.answer = func() (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "failed", ErrorMessage: "Request failed: context deadline exceeded", StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	h.misses(3)

	require.Equal(t, 1, h.askedCount())
	require.Empty(t, h.plans.outcomes, "transport failures are never handled as degradation")
	require.Len(t, h.results.results, 1)
	require.Equal(t, "inconclusive", h.results.results[0].QualityAction)
	require.False(t, h.results.results[0].PelicanConfig.FieldWatchMismatch)
	state := fieldWatchState(t, h.watcher, 57)
	require.False(t, state.Settled)
	require.Zero(t, state.Streak)
	require.Equal(t, codexFieldWatchUnknown, state.Verdict)

	h.misses(3)
	require.Equal(t, 1, h.askedCount(), "an inconclusive confirmation starts a 5-minute cooldown")
	h.advance(codexFieldWatchUnknownCooldown + time.Second)
	h.misses(3)
	require.Equal(t, 2, h.askedCount(), "the next three misses after the cooldown try again")

	// 处置没落地（规则中途变化）也不算停测。
	h.answer = func() (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "success", ResponseText: `{"answer":29}`, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	h.plans.action = "stale_run"
	h.advance(codexFieldWatchUnknownCooldown + time.Second)
	h.misses(3)
	require.Equal(t, 3, h.askedCount())
	require.False(t, fieldWatchState(t, h.watcher, 57).Settled)
	require.Equal(t, codexFieldWatchUnknown, fieldWatchState(t, h.watcher, 57).Verdict)
}

func TestFieldWatchStopPersistsAcrossRestart(t *testing.T) {
	h := newFieldWatchHarness(t, `{"answer":29}`)
	h.misses(3)
	require.True(t, fieldWatchState(t, h.watcher, 57).Settled)

	// 模拟重启：新的看护实例，内存状态从 0 开始，停止看护从规则隔离状态推出来。
	restarted := h.newWatcher()
	h.observe(restarted, 6, false)
	h.observe(restarted, 1, true)
	require.Equal(t, 1, h.askedCount(), "the rule still holds the account in #9, so watching stays stopped")
	state := fieldWatchState(t, restarted, 57)
	require.True(t, state.Settled)
	require.Zero(t, state.Observed)
	require.Equal(t, codexFieldWatchDegraded, state.Verdict)

	// 人工放回后重新看护。
	h.plans.setGroups(fieldWatchHostGroup)
	h.expireCache()
	h.observe(restarted, 3, false)
	require.Equal(t, 2, h.askedCount())
}

func TestFieldWatchObserveOnlyStopsUntilManualPass(t *testing.T) {
	h := newFieldWatchHarness(t, `{"answer":29}`)
	h.plans.plan.PelicanConfig.Quality = &QualityPolicy{Action: QualityActionObserveOnly}
	svc := h.runner.scheduledSvc
	svc.fieldWatchRun = h.runner.triggerFieldWatchPlan
	h.misses(3)
	require.Equal(t, []string{"failed"}, h.plans.outcomes)
	require.Equal(t, "observed", h.results.results[0].QualityAction)
	require.Equal(t, []int64{fieldWatchHostGroup}, h.plans.groups, "observe-only never moves the account")
	require.True(t, fieldWatchState(t, h.watcher, 57).Settled)

	restarted := h.newWatcher()
	h.observe(restarted, 3, false)
	require.Equal(t, 1, h.askedCount(), "the last field-watch round was wrong: still stopped after restart")
	require.True(t, fieldWatchState(t, restarted, 57).Settled)

	h.answer = func() (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "success", ResponseText: `{"answer":21}`, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	require.NoError(t, svc.TriggerQuality(context.Background(), h.plans.plan.ID))
	h.expireCache()
	h.observe(restarted, 3, false)
	require.Equal(t, 3, h.askedCount(), "a later passing manual confirmation lifts the observe-only stop")
}

func TestQualityGroupsStillQuarantined(t *testing.T) {
	require.True(t, QualityGroupsStillQuarantined([]int64{9}, []int64{9}, []int64{16}))
	require.True(t, QualityGroupsStillQuarantined([]int64{9, 16}, []int64{9}, []int64{16}), "still inside the added group")
	require.False(t, QualityGroupsStillQuarantined([]int64{16}, []int64{9}, []int64{16}), "put back and out of the added group")
	require.False(t, QualityGroupsStillQuarantined(nil, []int64{9}, []int64{16}))
	require.True(t, QualityGroupsStillQuarantined([]int64{3}, nil, []int64{16}), "without added groups: not back in a removed group")
	require.False(t, QualityGroupsStillQuarantined([]int64{16}, nil, []int64{16}))
	require.False(t, QualityGroupsStillQuarantined([]int64{3}, nil, nil))
}

func TestQualityFieldWatchRoundSettled(t *testing.T) {
	wrong := func(round, action string) *ScheduledTestResult {
		return &ScheduledTestResult{Status: "failed", ErrorMessage: "answer_mismatch", QualityAction: action, QualityRoundID: round,
			QualityJudgment: &QualityJudgment{Verdict: "incorrect"}, PelicanConfig: &PelicanTestConfig{TriggerSource: qualityFieldWatchSource}}
	}
	passed := func(round, source string) *ScheduledTestResult {
		return &ScheduledTestResult{Status: "success", QualityAction: "no_change", QualityRoundID: round,
			QualityJudgment: &QualityJudgment{Verdict: "correct"}, PelicanConfig: &PelicanTestConfig{TriggerSource: source}}
	}
	scheduledWrong := wrong("old", "groups_removed")
	scheduledWrong.PelicanConfig.TriggerSource = "scheduled"

	require.False(t, qualityFieldWatchRoundSettled(nil))
	require.True(t, qualityFieldWatchRoundSettled([]*ScheduledTestResult{wrong("r2", "observed"), passed("r1", qualityFieldWatchSource)}))
	// 同一轮多个模型：任一答错即停测。
	require.True(t, qualityFieldWatchRoundSettled([]*ScheduledTestResult{passed("r2", qualityFieldWatchSource), wrong("r2", "groups_removed")}))
	require.False(t, qualityFieldWatchRoundSettled([]*ScheduledTestResult{passed("r2", qualityFieldWatchManualSource), wrong("r1", "groups_removed")}), "a later passing confirmation lifts the stop")
	require.False(t, qualityFieldWatchRoundSettled([]*ScheduledTestResult{wrong("r2", "action_error")}), "an action that did not commit is retried")
	require.False(t, qualityFieldWatchRoundSettled([]*ScheduledTestResult{scheduledWrong}), "non field-watch history is ignored")
}

// ---- 不按 cron 定时跑 ----

func TestFieldWatchRulesAreNeverScheduled(t *testing.T) {
	now := time.Now()
	plan := fieldWatchRulePlan()
	plan.PelicanConfig.Prompt = "leftover prompt"
	plan.PelicanConfig.ParallelCount = 4
	plan.PelicanConfig.ReasoningEffort = ""
	plan.PelicanConfig.Quality.ExpectedAnswer = "21"
	plan.PelicanConfig.Quality.Judge = &QualityJudgeConfig{GroupID: 0}
	plan.PelicanConfig.Quality.TriggerOnUpstream5xx = true
	plan.PelicanConfig.Quality.AutoRestore = true
	next, err := nextPlanRun(&plan, now)
	require.NoError(t, err, "field watch needs no question, answer or judge")
	require.True(t, next.After(now.AddDate(50, 0, 0)), "the rule's next run is pushed far into the future")
	cfg := plan.PelicanConfig
	require.Empty(t, cfg.Prompt)
	require.Equal(t, 1, cfg.ParallelCount)
	require.Equal(t, "high", cfg.ReasoningEffort)
	require.Empty(t, cfg.Quality.ExpectedAnswer)
	require.Nil(t, cfg.Quality.Judge)
	require.False(t, cfg.Quality.TriggerOnUpstream5xx, "field watch does not take part in 5xx triggers")
	require.False(t, cfg.Quality.AutoRestore, "field watch never restores automatically")
	require.Equal(t, []int64{fieldWatchDegradedGroup}, cfg.Quality.AddGroupIDs, "remove_groups keeps add_group_ids")

	// 处置只允许仅记录 / 移出分组（可带移入）；其他处置明确拒绝，不偷偷改。
	observe := fieldWatchRulePlan()
	observe.PelicanConfig.Quality = &QualityPolicy{Action: QualityActionObserveOnly}
	_, err = nextPlanRun(&observe, now)
	require.NoError(t, err)
	for _, q := range []*QualityPolicy{
		{Action: "disable_scheduling"},
		{Action: QualityActionEnableBPS, BPS: &QualityBPSPolicy{}},
		{Action: QualityActionIsolateModels, RemoveGroupIDs: []int64{16}},
		{Action: QualityActionRemoveModel, RemoveModels: []string{"gpt-6-astra"}},
	} {
		bad := fieldWatchRulePlan()
		bad.PelicanConfig.Quality = q
		_, err = nextPlanRun(&bad, now)
		require.Error(t, err, q.Action)
		require.Contains(t, err.Error(), "字段看护只支持", q.Action)
		require.Equal(t, q.Action, bad.PelicanConfig.Quality.Action, "the action is not silently rewritten")
	}

	// 旧数据的 field_watch 布尔规范化成 question_kind。
	legacy := fieldWatchRulePlan()
	legacy.PelicanConfig.QuestionKind = "candy"
	legacy.PelicanConfig.Prompt = CandyPrompt
	legacy.PelicanConfig.FieldWatch = true
	_, err = nextPlanRun(&legacy, now)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexFieldWatchQuestionKind, legacy.PelicanConfig.QuestionKind)
	require.False(t, legacy.PelicanConfig.FieldWatch)

	missingPolicy := fieldWatchRulePlan()
	missingPolicy.PelicanConfig.Quality = nil
	_, err = nextPlanRun(&missingPolicy, now)
	require.Error(t, err)

	// 分组规则模板同样接受。
	tpl := &QualityRuleTemplate{ModelID: "gpt-6-astra", CronExpression: "*/30 * * * *", Enabled: true, PelicanConfig: cloneQualityPelicanConfig(fieldWatchRulePlan().PelicanConfig)}
	require.NoError(t, prepareQualityTemplate(tpl))
	require.Equal(t, OpenAICodexFieldWatchQuestionKind, tpl.PelicanConfig.QuestionKind)

	// 定时轮次即使拿到（旧数据 next_run_at 已到期）也不跑、不领租约。
	due := fieldWatchRulePlan()
	past := now.Add(-time.Minute)
	due.NextRunAt = &past
	repo := &fieldWatchPlanRepo{plan: due}
	runner := &ScheduledTestRunnerService{planRepo: repo, scheduledSvc: NewScheduledTestService(repo, &fieldWatchResultRepo{})}
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		t.Fatal("a scheduled tick must not run a field-watch rule")
		return nil, nil
	}
	candy := pelicanPlan()
	require.Equal(t, []*ScheduledTestPlan{candy}, withoutFieldWatchPlans([]*ScheduledTestPlan{repo.snapshot(), candy}))
	require.False(t, runner.runOnePlan(context.Background(), repo.snapshot()))
	fiveXX := repo.snapshot()
	fiveXX.TriggerSource = quality5xxSource
	require.False(t, runner.runOnePlan(context.Background(), fiveXX))
	require.Empty(t, repo.claims)
	_, err = (&AccountTestService{}).RunPelicanBackground(context.Background(), 57, "gpt-6-astra", due.PelicanConfig)
	require.ErrorIs(t, err, errFieldWatchNotRunnable)
}

func TestFieldWatchSkipsUpstream5xxTriggers(t *testing.T) {
	plan := fieldWatchRulePlan()
	plan.PelicanConfig.Quality.TriggerOnUpstream5xx = true // 旧数据
	repo := &fieldWatchPlanRepo{plan: plan}
	runner := &ScheduledTestRunnerService{planRepo: repo, scheduledSvc: NewScheduledTestService(repo, &fieldWatchResultRepo{})}
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		t.Fatal("5xx signals must not run a field-watch rule")
		return nil, nil
	}
	require.NoError(t, runner.runQualityTriggeredAccount(context.Background(), 57, time.Now()))
	require.Empty(t, repo.claims)
}

// ---- 「立即检测」直接跑一次复核 ----

func TestFieldWatchManualRunConfirmsImmediately(t *testing.T) {
	h := newFieldWatchHarness(t, `{"answer":21}`)
	svc := h.runner.scheduledSvc
	svc.fieldWatch = h.watcher
	svc.fieldWatchRun = h.runner.triggerFieldWatchPlan

	require.NoError(t, svc.TriggerQuality(context.Background(), h.plans.plan.ID))
	require.Equal(t, 1, h.askedCount(), "the manual button runs a candy confirmation, not a probe")
	require.Zero(t, h.plans.triggered, "field-watch rules are not made due for the scheduler")
	require.Equal(t, []string{qualityFieldWatchManualSource}, h.plans.claims)
	require.Len(t, h.results.results, 1)
	saved := h.results.results[0]
	require.Equal(t, qualityFieldWatchManualSource, saved.PelicanConfig.TriggerSource)
	require.False(t, saved.PelicanConfig.FieldWatchMismatch, "a manual run has no field evidence to contradict")
	require.Empty(t, h.plans.outcomes)

	// 手动答错同样按规则处置（移出 #16、移入 #9），并让网关停止看护。
	h.answer = func() (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "success", ResponseText: `{"answer":29}`, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	require.NoError(t, svc.TriggerQuality(context.Background(), h.plans.plan.ID))
	require.Equal(t, []string{"failed"}, h.plans.outcomes)
	require.Equal(t, []int64{fieldWatchDegradedGroup}, h.plans.groups)
	require.True(t, fieldWatchState(t, h.watcher, 57).Settled)
	h.misses(3)
	require.Equal(t, 2, h.askedCount(), "no automatic confirmation after a manual wrong answer")

	running := time.Now().Add(time.Minute)
	h.plans.plan.RunningUntil = &running
	require.Error(t, svc.TriggerQuality(context.Background(), h.plans.plan.ID), "a running rule is reported like other quality rules")
	h.plans.plan.RunningUntil = nil
	h.plans.plan.Enabled = false
	require.Error(t, svc.TriggerQuality(context.Background(), h.plans.plan.ID))
	require.Equal(t, 2, h.askedCount())

	// 非字段看护规则仍走原来的「立即到期」。
	h.plans.plan = *pelicanPlan()
	require.NoError(t, svc.TriggerQuality(context.Background(), h.plans.plan.ID))
	require.Equal(t, 1, h.plans.triggered)
}

// ---- 状态接口 ----

func TestFieldWatchStatusReportsState(t *testing.T) {
	stub := &fieldWatchStub{coverage: codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}}
	w := syncFieldWatcher(stub)
	now := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	w.observe(context.Background(), 58, "gpt-6-astra", "gpt-6-astra", true)
	w.observe(context.Background(), 57, "gpt-6-astra", "gpt-6-astra", false)
	svc := NewScheduledTestService(nil, nil)
	svc.fieldWatch = w

	all := svc.FieldWatchStatus(nil)
	require.Len(t, all, 2)
	require.Equal(t, int64(57), all[0].AccountID, "items are sorted by account")
	require.Equal(t, 1, all[0].Streak)
	require.EqualValues(t, 1, all[0].Observed)
	require.Nil(t, all[0].LastSeenAt)
	require.Equal(t, now, *all[0].LastMissAt)
	require.Equal(t, codexFieldWatchHealthy, all[1].Verdict)

	some := svc.FieldWatchStatus([]int64{58, 99, 58})
	require.Len(t, some, 2, "requested accounts are returned once each, unseen ones as zero state")
	require.Equal(t, int64(58), some[0].AccountID)
	require.Equal(t, QualityFieldWatchStatus{AccountID: 99}, some[1])

	encoded, err := json.Marshal(some[1])
	require.NoError(t, err)
	require.JSONEq(t, `{"account_id":99,"observed":0,"streak":0,"last_observed_at":null,"last_seen_at":null,"last_miss_at":null,"verdict":"","reason":"","confirming":false,"settled":false,"cooldown_until":null}`, string(encoded))

	require.Empty(t, NewScheduledTestService(nil, nil).FieldWatchStatus(nil), "no watcher means no state")
	require.NotNil(t, NewScheduledTestService(nil, nil).FieldWatchStatus(nil))
}

func TestFieldWatchStaleLookupDoesNotUndoFreshVerdict(t *testing.T) {
	var w *codexAstraWatcher
	w = newCodexAstraWatcher(func(context.Context, int64) (codexFieldWatchCoverage, error) {
		// 查库期间复核刚判了降智：这次（旧的）查询结果不能把停止看护撤掉。
		w.finishConfirm(57, codexFieldWatchConfirmResult{Outcome: codexFieldWatchDegraded}, true)
		return codexFieldWatchCoverage{Covered: true, Models: []string{"gpt-6-astra"}}, nil
	}, nil)
	w.observe(context.Background(), 57, "gpt-6-astra", "gpt-6-astra", false)
	state := fieldWatchState(t, w, 57)
	require.True(t, state.Settled)
	require.Zero(t, state.Observed)
	require.Empty(t, w.cache, "a lookup that raced a verdict is not cached")
}

func TestFieldWatchDiagRecordsEventShapeOnly(t *testing.T) {
	d := newCodexFieldWatchDiag()
	d.observe("response.created", []byte(`{"type":"response.created","response":{"status":"in_progress"},"safety_buffering":false}`))
	d.observe("response.output_item.added", []byte(`{"type":"response.output_item.added","item":{"type":"function_call","name":"shell"}}`))
	d.observe("response.output_text.delta", []byte(`{"type":"response.output_text.delta","delta":"secret customer text","safety_buffering":true}`))
	d.observe("response.completed", []byte(`{"type":"response.completed","response":{"status":"completed"}}`))
	require.Equal(t, 4, d.frames)
	require.Equal(t, 2, d.markFrames)
	require.Equal(t, 1, d.trueFrames)
	require.Equal(t, "completed", d.status)
	require.Equal(t, "function_call:1", codexFieldWatchDiagFormat(d.outputItems))
	require.Equal(t, "response.created:1,response.output_text.delta:1", codexFieldWatchDiagFormat(d.marked))
	for _, v := range []string{codexFieldWatchDiagFormat(d.events), codexFieldWatchDiagFormat(d.marked), codexFieldWatchDiagFormat(d.outputItems)} {
		require.NotContains(t, v, "secret")
	}
	var nilDiag *codexFieldWatchDiag
	nilDiag.observe("x", nil)
	nilDiag.log(1, "m", "m", "", false)
}

// 糖果题规则（答错移出 #16、移入 #9、不自动恢复）：号被隔离后不再检测，人工放回后恢复。
func TestCandyRuleStopsTestingQuarantinedAccount(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	plan := ScheduledTestPlan{ID: 64, AccountID: 57, ModelID: "gpt-6-astra", CronExpression: "@random 10-15", Enabled: true, MaxResults: 100, NextRunAt: &past,
		PelicanConfig: &PelicanTestConfig{QuestionKind: "candy", Prompt: CandyPrompt, ReasoningEffort: "high", ParallelCount: 1, ModelID: "gpt-6-astra", ModelIDs: []string{"gpt-6-astra"},
			Quality: &QualityPolicy{ExpectedAnswer: "21", Action: "remove_groups", RemoveGroupIDs: []int64{fieldWatchHostGroup}, AddGroupIDs: []int64{fieldWatchDegradedGroup}}}}
	repo := &fieldWatchPlanRepo{plan: plan, groups: []int64{fieldWatchDegradedGroup}, state: "remove_groups", stateAdded: []int64{fieldWatchDegradedGroup}, stateRem: []int64{fieldWatchHostGroup}}
	results := &fieldWatchResultRepo{}
	runner := &ScheduledTestRunnerService{planRepo: repo, scheduledSvc: NewScheduledTestService(repo, results)}
	asked := 0
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		asked++
		return &ScheduledTestResult{Status: "success", ResponseText: `{"answer":21}`, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}

	require.False(t, runner.runPelicanPlan(context.Background(), repo.snapshot()))
	require.Zero(t, asked, "a quarantined account is not tested again")
	require.Equal(t, 1, repo.finished, "the schedule still advances")
	require.Empty(t, results.results)
	require.ErrorIs(t, runner.scheduledSvc.TriggerQuality(context.Background(), 64), errQualityQuarantineHeld, "manual run explains why nothing happens")
	require.Zero(t, repo.triggered)

	// 人工把号放回 #16（离开 #9）后恢复检测。
	repo.setGroups(fieldWatchHostGroup)
	again := time.Now().Add(-time.Minute)
	repo.mu.Lock()
	repo.plan.NextRunAt = &again
	repo.mu.Unlock()
	require.NoError(t, runner.scheduledSvc.TriggerQuality(context.Background(), 64))
	runner.runPelicanPlan(context.Background(), repo.snapshot())
	require.Equal(t, 1, asked, "testing resumes once the account is put back")

	// 放回后再答错：先丢掉过期隔离记录，再移出 #16、移入 #9，然后又停测。
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		asked++
		return &ScheduledTestResult{Status: "success", ResponseText: `{"answer":29}`, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	repo.mu.Lock()
	repo.plan.NextRunAt = &again
	repo.mu.Unlock()
	runner.runPelicanPlan(context.Background(), repo.snapshot())
	require.Equal(t, 2, asked)
	require.Equal(t, 1, repo.released, "the stale quarantine record is released first")
	require.Equal(t, []int64{fieldWatchDegradedGroup}, repo.groups, "moved out of #16 and into #9 again")
	require.True(t, qualityQuarantineHeld(context.Background(), repo, repo.snapshot()))

	// 自动恢复的规则不受影响：隔离中照常检测（答对才能放回）。
	repo.setGroups(fieldWatchDegradedGroup)
	repo.mu.Lock()
	repo.plan.PelicanConfig.Quality.AutoRestore = true
	repo.plan.NextRunAt = &again
	repo.mu.Unlock()
	require.False(t, qualityQuarantineHeld(context.Background(), repo, repo.snapshot()))
}

type qualityAccountStatusRepo struct {
	AccountRepository
	mu      sync.Mutex
	account Account
}

func (r *qualityAccountStatusRepo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.account
	return &a, nil
}

// 账号凭证失效（状态 error）或停用：质量检测不发请求、不记结果，重新授权后自动接着测。
// 只停了调度的号照常检测（「答错停调度」规则要靠检测自动恢复）。
func TestQualityRuleSkipsUnavailableAccounts(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	plan := ScheduledTestPlan{ID: 65, AccountID: 58, ModelID: "gpt-6-astra", CronExpression: "@random 10-15", Enabled: true, MaxResults: 100, NextRunAt: &past,
		PelicanConfig: &PelicanTestConfig{QuestionKind: "candy", Prompt: CandyPrompt, ReasoningEffort: "high", ParallelCount: 1, ModelID: "gpt-6-astra", ModelIDs: []string{"gpt-6-astra"},
			Quality: &QualityPolicy{ExpectedAnswer: "21", Action: "remove_groups", RemoveGroupIDs: []int64{fieldWatchHostGroup}, AddGroupIDs: []int64{fieldWatchDegradedGroup}}}}
	repo := &fieldWatchPlanRepo{plan: plan, groups: []int64{fieldWatchHostGroup}}
	results := &fieldWatchResultRepo{}
	accounts := &qualityAccountStatusRepo{account: Account{ID: 58, Status: StatusError, Schedulable: false,
		// 糖果题可直接用账号显式配置的模型映射检测，不依赖上游模型目录（v2.17.7 起执行前校验模型）。
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}}}}
	accountTests := &AccountTestService{accountRepo: accounts}
	scheduled := NewScheduledTestService(repo, results)
	scheduled.accountTests = accountTests
	runner := &ScheduledTestRunnerService{planRepo: repo, scheduledSvc: scheduled, accountTestSvc: accountTests}
	asked := 0
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		asked++
		return &ScheduledTestResult{Status: "success", ResponseText: `{"answer":21}`, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
	}
	due := func() {
		again := time.Now().Add(-time.Minute)
		repo.mu.Lock()
		repo.plan.NextRunAt = &again
		repo.mu.Unlock()
	}

	require.False(t, runner.runPelicanPlan(context.Background(), repo.snapshot()))
	require.Zero(t, asked, "an account with an expired/revoked token is not tested")
	require.Empty(t, results.results, "no inconclusive record is written")
	require.Equal(t, 1, repo.finished, "the schedule still advances")
	err := scheduled.TriggerQuality(context.Background(), 65)
	require.Error(t, err)
	require.Contains(t, err.Error(), "不检测")

	// 停用的号同样不测。
	accounts.mu.Lock()
	accounts.account.Status = StatusDisabled
	accounts.mu.Unlock()
	due()
	runner.runPelicanPlan(context.Background(), repo.snapshot())
	require.Zero(t, asked, "a disabled account is not tested")

	// 重新授权后状态恢复正常（即使调度还没开）就自动恢复检测。
	accounts.mu.Lock()
	accounts.account.Status = StatusActive
	accounts.mu.Unlock()
	due()
	require.NoError(t, scheduled.TriggerQuality(context.Background(), 65))
	runner.runPelicanPlan(context.Background(), repo.snapshot())
	require.Equal(t, 1, asked)
	require.Len(t, results.results, 1)
}
