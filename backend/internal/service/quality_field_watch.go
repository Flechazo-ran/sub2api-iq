package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 字段看护规则的执行端：网关连续 3 次没看到 safety_buffering 后，由这里按规则跑一次
// 内置糖果题复核。复核走和定时检测一样的规则执行路径（领取租约、存检测记录），
// 区别只在两处：租约按触发来源领取（规则的 next_run_at 永远在远期，定时调度领不到），
// 处置只在答错时调 ApplyQualityOutcome，答对只在记录里打警告标记，无法判断不处置。

const (
	// qualityFieldWatchSource 是字段看护自动触发的复核（连续 3 次未见 safety_buffering）。
	qualityFieldWatchSource = "field_watch"
	// qualityFieldWatchManualSource 是页面「立即检测」触发的字段看护复核。
	qualityFieldWatchManualSource = "field_watch_manual"
	// qualityFieldWatchHistoryScan 是从检测记录推「最近一轮复核是否答错」时最多回看的条数。
	qualityFieldWatchHistoryScan = 50
)

func isQualityFieldWatchSource(source string) bool {
	return source == qualityFieldWatchSource || source == qualityFieldWatchManualSource
}

// IsQualityFieldWatchTrigger 供仓储层领取租约时识别字段看护复核（不受 next_run_at 限制）。
func IsQualityFieldWatchTrigger(source string) bool { return isQualityFieldWatchSource(source) }

// qualityFieldWatchNextRun 把字段看护规则的 next_run_at 放到很远的将来，定时调度永远领不到它。
func qualityFieldWatchNextRun(now time.Time) time.Time {
	return now.AddDate(100, 0, 0).Truncate(time.Second)
}

// normalizeQualityFieldWatchPlan 规范化字段看护规则：不下发题目、不用判题模型、
// 不参与 5xx 触发、不自动恢复，并行固定 1。只改要保存的规则本身；处置方式不在这里改，
// 不支持的处置由 validateQualityPolicy 直接拒绝。
func normalizeQualityFieldWatchPlan(plan *ScheduledTestPlan) {
	cfg := plan.PelicanConfig
	cfg.QuestionKind = OpenAICodexFieldWatchQuestionKind
	cfg.FieldWatch = false
	cfg.Prompt = ""
	cfg.ParallelCount = 1
	if strings.TrimSpace(cfg.ReasoningEffort) == "" {
		cfg.ReasoningEffort = "high"
	}
	if strings.TrimSpace(plan.ModelID) == "" && len(cfg.ModelIDs) == 0 {
		plan.ModelID = codexAstraWatchModel
	}
	if cfg.Quality != nil {
		cfg.Quality.ExpectedAnswer = ""
		cfg.Quality.Judge = nil
		cfg.Quality.TriggerOnUpstream5xx = false
		// 字段看护不自动恢复：答对从不调用处置，auto_restore 对它无效。
		cfg.Quality.AutoRestore = false
	}
}

// qualityFieldWatchTestConfig 把字段看护规则改写成内置糖果题的执行配置。
// 题型设为 candy，执行时和定时糖果题一样经 newCandyVariantSamples 每次随机换题面
// （结果里带题面签名），答案固定 21、内置判分。只用于本次执行和结果快照，不改存库的规则。
func qualityFieldWatchTestConfig(cfg PelicanTestConfig) PelicanTestConfig {
	cfg.QuestionKind = "candy"
	cfg.FieldWatch = false
	cfg.Prompt = CandyPrompt
	cfg.TestChannel = "account"
	cfg.ParallelCount = 1
	if normalizePelicanReasoningEffort(cfg.ReasoningEffort) == "" {
		cfg.ReasoningEffort = "high"
	}
	if cfg.Quality != nil {
		policy := *cfg.Quality
		policy.ExpectedAnswer = "21"
		policy.Judge = nil
		policy.TriggerOnUpstream5xx = false
		policy.AutoRestore = false
		cfg.Quality = &policy
	}
	return cfg
}

func qualityFieldWatchPlanActive(plan *ScheduledTestPlan) bool {
	return plan != nil && plan.Enabled && plan.PelicanConfig != nil && plan.PelicanConfig.Quality != nil && isOpenAICodexFieldWatchPlan(plan.PelicanConfig)
}

// qualityFieldWatchModels 是规则配置的模型（model_id + model_ids，默认 gpt-6-astra），统一小写。
func qualityFieldWatchModels(plan *ScheduledTestPlan) []string {
	var models []string
	for _, model := range qualityPlanModels(plan) {
		if model = strings.ToLower(strings.TrimSpace(model)); model != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		models = []string{codexAstraWatchModel}
	}
	return models
}

// qualityFieldWatchActionRetryable：处置没有真正落地（执行失败或规则中途变化），不算停测。
func qualityFieldWatchActionRetryable(action string) bool {
	return action == "action_error" || action == "stale_run"
}

func qualityFieldWatchResultDegraded(r *ScheduledTestResult) bool {
	return r != nil && r.Status == "failed" && r.ErrorMessage == "answer_mismatch" &&
		r.QualityJudgment != nil && r.QualityJudgment.Verdict == "incorrect" &&
		!qualityFieldWatchActionRetryable(r.QualityAction)
}

// qualityFieldWatchRoundSettled 看规则最近一轮字段看护复核（记录按新到旧）是否答错并已处置。
func qualityFieldWatchRoundSettled(results []*ScheduledTestResult) bool {
	matched := false
	round := ""
	for _, r := range results {
		if r == nil || r.PelicanConfig == nil || !isQualityFieldWatchSource(r.PelicanConfig.TriggerSource) {
			if matched {
				break
			}
			continue
		}
		if matched && (round == "" || r.QualityRoundID != round) {
			break
		}
		matched, round = true, r.QualityRoundID
		if qualityFieldWatchResultDegraded(r) {
			return true
		}
	}
	return false
}

// QualityGroupMembership 是判断「号是否仍被本规则隔离」所需的分组现状。
type QualityGroupMembership struct {
	// StateAction 是本规则在 account_quality_states 里记录的处置（没有记录为空）。
	StateAction string
	// Added / Removed 是本规则记录的移入 / 移出分组。
	Added   []int64
	Removed []int64
	// Current 是账号当前所在的分组。
	Current []int64
}

// QualityFieldWatchGroupStore 由仓储层的规则仓库实现：读本规则的隔离状态与账号分组现状；
// 号被人工放回承载分组后，在再次隔离前丢掉过期的隔离记录。
type QualityFieldWatchGroupStore interface {
	QualityGroupMembership(ctx context.Context, planID, accountID int64) (QualityGroupMembership, error)
	ReleaseRestoredQualityGroups(ctx context.Context, plan *ScheduledTestPlan) (bool, error)
}

func int64Union(a, b []int64) []int64 {
	seen := make(map[int64]bool, len(a)+len(b))
	out := make([]int64, 0, len(a)+len(b))
	for _, list := range [][]int64{a, b} {
		for _, id := range list {
			if id > 0 && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// QualityGroupsStillQuarantined 判断号是否还处在规则的隔离里：
// 配了移入分组时，号还在任一移入分组里就算；没配时，号不在任何移出分组里就算。
// 号被人工放回承载分组（并离开移入分组）后返回 false。
func QualityGroupsStillQuarantined(current, added, removed []int64) bool {
	in := make(map[int64]bool, len(current))
	for _, id := range current {
		in[id] = true
	}
	if len(added) > 0 {
		for _, id := range added {
			if in[id] {
				return true
			}
		}
		return false
	}
	if len(removed) == 0 {
		return false
	}
	for _, id := range removed {
		if in[id] {
			return false
		}
	}
	return true
}

// qualityAccountUnavailableReason：账号不存在、凭证失效（状态 error）或被停用（disabled）时返回原因，
// 质量检测跳过它，不白发请求。只停了调度的号照常检测：「答错停调度」类规则要靠后续检测自动恢复。
// 查询失败按可用处理，照常检测。
func qualityAccountUnavailableReason(ctx context.Context, repo AccountRepository, accountID int64) string {
	account, err := repo.GetByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return "账号不存在"
		}
		return ""
	}
	if account == nil {
		return "账号不存在"
	}
	switch account.Status {
	case StatusError:
		return "账号凭证失效（状态 error），不检测；重新授权后自动恢复"
	case StatusDisabled:
		return "账号已停用，不检测"
	}
	return ""
}

// errQualityQuarantineHeld 是「立即检测」碰到已隔离账号时的提示。
var errQualityQuarantineHeld = fmt.Errorf("该号已判降智并移入降智分组，停止检测；把号放回承载分组后自动恢复检测")

// qualityQuarantineHeld：答错移出分组、且不自动恢复的规则（remove_groups + auto_restore=false），
// 号还处在本规则的隔离里（有隔离记录、号还在移入分组 / 不在移出分组）时返回 true，
// 这时不再检测，避免对已判降智的号反复烧糖果题。查询失败按「未隔离」处理，照常检测。
func qualityQuarantineHeld(ctx context.Context, repo any, plan *ScheduledTestPlan) bool {
	if plan == nil || plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil {
		return false
	}
	q := plan.PelicanConfig.Quality
	if q.Action != "remove_groups" || q.AutoRestore {
		return false
	}
	store, ok := repo.(QualityFieldWatchGroupStore)
	if !ok {
		return false
	}
	membership, err := store.QualityGroupMembership(ctx, plan.ID, plan.AccountID)
	if err != nil || membership.StateAction != "remove_groups" {
		return false
	}
	return QualityGroupsStillQuarantined(membership.Current, int64Union(q.AddGroupIDs, membership.Added), int64Union(q.RemoveGroupIDs, membership.Removed))
}

// fieldWatchCoverage 是网关「这个号有没有字段看护规则、是否已停止看护」的查询（网关侧缓存 30 秒）。
func (s *ScheduledTestRunnerService) fieldWatchCoverage(ctx context.Context, accountID int64) (codexFieldWatchCoverage, error) {
	plans, err := s.planRepo.ListByAccountID(ctx, accountID)
	if err != nil {
		return codexFieldWatchCoverage{}, err
	}
	var coverage codexFieldWatchCoverage
	seen := map[string]bool{}
	for _, plan := range plans {
		if !qualityFieldWatchPlanActive(plan) {
			continue
		}
		coverage.Covered = true
		for _, model := range qualityFieldWatchModels(plan) {
			if !seen[model] {
				seen[model] = true
				coverage.Models = append(coverage.Models, model)
			}
		}
		if !coverage.Settled {
			settled, err := s.fieldWatchPlanSettled(ctx, plan)
			if err != nil {
				return codexFieldWatchCoverage{}, err
			}
			coverage.Settled = settled
		}
	}
	return coverage, nil
}

// fieldWatchPlanSettled 从数据库推「已判降智、停止看护」：
//   - 移出分组：本规则的隔离状态（或最近一轮字段看护复核答错）成立，且号还没被人工放回承载分组；
//   - 仅记录：最近一轮字段看护复核答错（手动「立即检测」答对后解除）。
func (s *ScheduledTestRunnerService) fieldWatchPlanSettled(ctx context.Context, plan *ScheduledTestPlan) (bool, error) {
	q := plan.PelicanConfig.Quality
	if q.Action == "remove_groups" {
		store, ok := s.planRepo.(QualityFieldWatchGroupStore)
		if !ok {
			return false, nil
		}
		membership, err := store.QualityGroupMembership(ctx, plan.ID, plan.AccountID)
		if err != nil {
			return false, err
		}
		if !QualityGroupsStillQuarantined(membership.Current, int64Union(q.AddGroupIDs, membership.Added), int64Union(q.RemoveGroupIDs, membership.Removed)) {
			return false, nil
		}
		if membership.StateAction == "remove_groups" {
			return true, nil
		}
	}
	return s.fieldWatchLatestRoundWrong(ctx, plan.ID)
}

func (s *ScheduledTestRunnerService) fieldWatchLatestRoundWrong(ctx context.Context, planID int64) (bool, error) {
	if s.scheduledSvc == nil || s.scheduledSvc.resultRepo == nil {
		return false, nil
	}
	results, err := s.scheduledSvc.resultRepo.ListByPlanID(ctx, planID, qualityFieldWatchHistoryScan, false)
	if err != nil {
		return false, err
	}
	return qualityFieldWatchRoundSettled(results), nil
}

// releaseRestoredFieldWatchQuarantine：号被人工放回承载分组后再次答错时，先丢掉本规则过期的隔离记录，
// 否则 ApplyQualityOutcome 会按「保持隔离」处理而不再移出分组。
func (s *ScheduledTestRunnerService) releaseRestoredFieldWatchQuarantine(ctx context.Context, plan *ScheduledTestPlan) {
	if plan.PelicanConfig.Quality.Action != "remove_groups" {
		return
	}
	store, ok := s.planRepo.(QualityFieldWatchGroupStore)
	if !ok {
		return
	}
	released, err := store.ReleaseRestoredQualityGroups(ctx, plan)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "field watch plan=%d stale quarantine release failed: %v", plan.ID, err)
		return
	}
	if released {
		logger.LegacyPrintf("service.scheduled_test_runner", "field watch plan=%d account=%d was put back manually; re-quarantining", plan.ID, plan.AccountID)
	}
}

// runFieldWatchAccount 是网关自动复核的执行入口：该账号每条启用的字段看护规则各跑一次。
func (s *ScheduledTestRunnerService) runFieldWatchAccount(ctx context.Context, accountID int64) codexFieldWatchConfirmResult {
	plans, err := s.planRepo.ListByAccountID(ctx, accountID)
	if err != nil {
		return codexFieldWatchConfirmResult{Outcome: codexFieldWatchUnknown, Reason: "读取字段看护规则失败"}
	}
	var outcomes []codexFieldWatchConfirmResult
	for _, plan := range plans {
		if !qualityFieldWatchPlanActive(plan) {
			continue
		}
		_, result := s.runFieldWatchPlan(ctx, plan.ID, qualityFieldWatchSource)
		outcomes = append(outcomes, result)
	}
	return mergeFieldWatchOutcomes(outcomes)
}

// mergeFieldWatchOutcomes：任一规则答错即降智；全部答对才满血；其余无法判断。
func mergeFieldWatchOutcomes(outcomes []codexFieldWatchConfirmResult) codexFieldWatchConfirmResult {
	if len(outcomes) == 0 {
		return codexFieldWatchConfirmResult{Outcome: codexFieldWatchUnknown, Reason: "没有启用的字段看护规则"}
	}
	for _, o := range outcomes {
		if o.Outcome == codexFieldWatchDegraded {
			return o
		}
	}
	for _, o := range outcomes {
		if o.Outcome != codexFieldWatchHealthy {
			return o
		}
	}
	return outcomes[0]
}

// runFieldWatchPlan 重新读取规则并按触发来源领取租约，跑一次糖果题复核。
func (s *ScheduledTestRunnerService) runFieldWatchPlan(ctx context.Context, planID int64, source string) (int64, codexFieldWatchConfirmResult) {
	unknown := func(reason string) codexFieldWatchConfirmResult {
		return codexFieldWatchConfirmResult{Outcome: codexFieldWatchUnknown, Reason: reason}
	}
	plan, err := s.planRepo.GetByID(ctx, planID)
	if err != nil || plan == nil {
		return 0, unknown("复核未开始：读取规则失败")
	}
	if !qualityFieldWatchPlanActive(plan) {
		return plan.AccountID, unknown("复核未开始：规则已停用或已改为其他检测方式")
	}
	plan.TriggerSource = source
	s.runOnePlan(ctx, plan)
	if plan.FieldWatchOutcome == "" {
		return plan.AccountID, unknown("复核未完成：规则正在检测或刚被修改")
	}
	return plan.AccountID, codexFieldWatchConfirmResult{Outcome: plan.FieldWatchOutcome, Reason: plan.FieldWatchReason}
}

// triggerFieldWatchPlan 是页面「立即检测」对字段看护规则的处理：直接跑一次糖果题复核（异步）。
func (s *ScheduledTestRunnerService) triggerFieldWatchPlan(ctx context.Context, planID int64) error {
	plan, err := s.planRepo.GetByID(ctx, planID)
	if err != nil || !qualityFieldWatchPlanActive(plan) || (plan.RunningUntil != nil && plan.RunningUntil.After(time.Now())) {
		return fmt.Errorf("plan must be enabled and idle")
	}
	run := func() {
		runCtx, cancel := context.WithTimeout(context.Background(), codexAstraWatchTimeout)
		defer cancel()
		accountID, result := s.runFieldWatchPlan(runCtx, planID, qualityFieldWatchManualSource)
		s.fieldWatch.finishConfirm(accountID, result, false)
	}
	if s.fieldWatchSpawn != nil {
		s.fieldWatchSpawn(run)
		return nil
	}
	go run()
	return nil
}

// qualityFieldWatchMismatchReason 是「字段说降智、糖果题答对」的警告文案。
const qualityFieldWatchMismatchReason = "字段显示降智（连续 3 次无 safety_buffering）、糖果题答对、未处置"

// fieldWatchRoundOutcome 把一轮复核的结果翻成看护结论。
func fieldWatchRoundOutcome(source, outcome, qualityAction string) (string, string) {
	switch {
	case outcome == "failed" && !qualityFieldWatchActionRetryable(qualityAction):
		return codexFieldWatchDegraded, "糖果题答错，已按规则处置（" + qualityAction + "），停止看护；把号放回承载分组后重新看护"
	case outcome == "failed":
		return codexFieldWatchUnknown, "糖果题答错，但处置未落地（" + qualityAction + "），下一轮再试"
	case outcome == "passed" && source == qualityFieldWatchSource:
		return codexFieldWatchHealthy, qualityFieldWatchMismatchReason
	case outcome == "passed":
		return codexFieldWatchHealthy, "手动复核：糖果题答对，未处置"
	}
	return codexFieldWatchUnknown, "糖果题复核无法判断（网络、限流或超时），未处置"
}
