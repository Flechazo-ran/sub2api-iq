package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

const CandyPrompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4`

const PelicanDeliveryContract = "所有账号使用相同交付约定：直接返回独立 HTML，不使用 Markdown 代码块或外部依赖。只输出 HTML，不要解释。"

var pelicanHTMLPattern = regexp.MustCompile(`(?i)<(?:!doctype\s+html|html|svg)[\s>]`)

// Failures that describe the model's output rather than the account; group tests keep
// them as the group's answer instead of trying another account.
const (
	pelicanErrEmptyOutput  = "Model returned empty output"
	pelicanErrCaptureLimit = "Response exceeds 4 MiB capture limit"
	pelicanErrHistoryLimit = "Output exceeds 2 MiB history limit"
	pelicanErrMaxTokens    = "Model output hit max_tokens before finishing"
	pelicanErrRefused      = "Model refused the request"
)

func (s *AccountTestService) RunPelicanBackground(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
	ctx = context.WithValue(ctx, qualityProbeContextKey{}, true)
	// 探针题型不下发题目，直接走门票探针。
	if isOpenAICodexStateProbePlan(cfg) {
		return s.runOpenAICodexStateProbeScheduled(ctx, accountID, model, cfg)
	}
	// astra 探针同样不下发题目：只发一次最小请求，看响应流里有没有 safety_buffering。
	if isOpenAICodexAstraProbePlan(cfg) {
		return s.runOpenAICodexAstraProbeScheduled(ctx, accountID, model, cfg)
	}
	// 字段看护规则没有可执行的题目：复核由规则执行器改写成糖果题后才会走到这里。
	if isOpenAICodexFieldWatchPlan(cfg) {
		return nil, errFieldWatchNotRunnable
	}
	// Recognize the exact built-in question in legacy HTML plans as well.
	if isBuiltinCandyPlan(cfg) {
		copy := *cfg
		copy.QuestionKind = "candy"
		cfg = &copy

	}
	started := time.Now()
	directOAuth401, directOAuthRevoked401, nativeOAuthTransport := false, false, false
	ctx = withPelicanTestOptions(ctx, pelicanTestOptions{
		directOAuth401:        &directOAuth401,
		directOAuthRevoked401: &directOAuthRevoked401,
		nativeOAuthTransport:  &nativeOAuthTransport,
		qualityBatch:          cfg.QualityBatch,
		testChannel:           cfg.TestChannel,
		observeOnly:           cfg.Quality != nil && cfg.Quality.Action == QualityActionObserveOnly,
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &pelicanRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(w)
	c.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
	err := s.TestPelicanAccountConnection(c, accountID, model, intelligenceTestPrompt(cfg), cfg.ReasoningEffort)
	output, message := parsePelicanOutput(w.Body.String())
	if w.overflow {
		output = ""
		message = pelicanErrCaptureLimit
	}
	if err != nil && message == "" {
		message = err.Error()
	}
	if message == "" {
		message = intelligenceTestOutputError(cfg, output)
	}
	// Bounded history storage; never persist a truncated animation as a success.
	if len(output) > 2<<20 {
		output = ""
		message = pelicanErrHistoryLimit
	}
	status := "success"
	if message != "" {
		status = "failed"
	}
	finished := time.Now()
	snapshot := *cfg
	snapshot.ModelID = model
	return &ScheduledTestResult{DirectOAuth401: directOAuth401, DirectOAuthRevoked401: directOAuthRevoked401, Status: status, ResponseText: output, ErrorMessage: message, LatencyMs: finished.Sub(started).Milliseconds(), StartedAt: started, FinishedAt: finished, PelicanConfig: &snapshot}, nil
}

func (s *ScheduledTestRunnerService) runPelicanPlan(ctx context.Context, plan *ScheduledTestPlan) bool {
	// 字段看护规则只在真实请求连续未见 safety_buffering（或页面「立即检测」）时跑复核，
	// 定时轮次和 5xx 触发都不跑任何题。
	fieldWatch := isOpenAICodexFieldWatchPlan(plan.PelicanConfig)
	if fieldWatch && !isQualityFieldWatchSource(plan.TriggerSource) {
		return false
	}
	var triggeredAccount *Account
	if plan.TriggerSource == quality5xxSource && s.accountTestSvc != nil {
		var lookupErr error
		triggeredAccount, lookupErr = s.accountTestSvc.accountRepo.GetByID(ctx, plan.AccountID)
		if lookupErr != nil || triggeredAccount == nil {
			return false // Keep the queued signal; no completed run has taken place.
		}
		if triggeredAccount.TempUnschedulableUntil != nil && triggeredAccount.TempUnschedulableUntil.After(time.Now()) {
			return false // Let the native cooldown drain before probing the account again.
		}
	}
	now := time.Now()
	next, err := nextPlanRun(plan, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d invalid config: %v", plan.ID, err)
		return false
	}
	if plan.TriggerSource == quality5xxSource && plan.NextRunAt != nil && plan.NextRunAt.After(now) {
		next = *plan.NextRunAt
	}
	// Persisted lease prevents duplicate execution across ticks and server replicas.
	// It also recovers automatically after a process crash.
	until := now.Add(15 * time.Minute).Truncate(time.Microsecond)
	claimed, err := s.planRepo.ClaimPelican(ctx, plan, now, until, next)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d claim failed: %v", plan.ID, err)
	}
	if err != nil || !claimed {
		return false
	}
	// 账号凭证失效（error）或被停用（disabled）：不检测、不记结果，只推进周期，
	// 不白发请求。号重新授权、恢复正常后自动恢复检测。
	if plan.PelicanConfig.Quality != nil && s.accountTestSvc != nil && s.accountTestSvc.accountRepo != nil {
		if reason := qualityAccountUnavailableReason(ctx, s.accountTestSvc.accountRepo, plan.AccountID); reason != "" {
			logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d account=%d skipped: %s", plan.ID, plan.AccountID, reason)
			finishCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if finishErr := s.planRepo.FinishPelican(finishCtx, plan.ID, until, time.Now()); finishErr != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, finishErr)
			}
			return false
		}
	}
	// 答错移出分组且不自动恢复的规则：号还在隔离中（被移进降智分组、没被人工放回）就不再检测，
	// 只推进检测周期；人工把号放回承载分组后自动恢复检测。
	if !fieldWatch && qualityQuarantineHeld(ctx, s.planRepo, plan) {
		logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d account=%d skipped: account is quarantined (no auto restore)", plan.ID, plan.AccountID)
		finishCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if finishErr := s.planRepo.FinishPelican(finishCtx, plan.ID, until, time.Now()); finishErr != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, finishErr)
		}
		return false
	}
	// Result-only metadata must not mutate the stored rule or shared plan config.
	snapshot := *plan.PelicanConfig
	snapshot.QualityRestoreDiagnostic = nil
	plan.QualityRestoreDiagnostic = nil
	snapshot.TriggerSource = plan.TriggerSource
	if snapshot.TriggerSource == "" {
		snapshot.TriggerSource = "scheduled"
	}
	applyTriggeredQuality := true
	if triggeredAccount != nil {
		snapshot, applyTriggeredQuality = quality5xxTestConfig(triggeredAccount, plan.ModelID, snapshot)
	}
	if fieldWatch {
		// 复核固定跑内置糖果题（答案 21、内置判分），模型和推理强度用规则里的。
		snapshot = qualityFieldWatchTestConfig(snapshot)
	}
	plan.PelicanConfig = &snapshot
	// Legacy rules can target API-key accounts, or an account can change type
	// after a rule is saved. Advance the claimed schedule without running a
	// probe or recording a misleading inconclusive quality round.
	if isOpenAICodexStateProbePlan(plan.PelicanConfig) && s.accountTestSvc != nil {
		account, lookupErr := s.accountTestSvc.accountRepo.GetByID(ctx, plan.AccountID)
		ignoreBPS := plan.PelicanConfig.Quality != nil && (plan.PelicanConfig.Quality.Action == QualityActionEnableBPS || plan.PelicanConfig.BPSRecoveryPending)
		if lookupErr != nil || openAICodexStateProbeUnsupportedReason(account, plan.ModelID, ignoreBPS) != "" {
			logger.LegacyPrintf("service.scheduled_test_runner", "state probe plan=%d account=%d skipped: account unavailable or unsupported", plan.ID, plan.AccountID)
			finishCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if finishErr := s.planRepo.FinishPelican(finishCtx, plan.ID, until, time.Now()); finishErr != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, finishErr)
			}
			return false
		}
	}
	// Fallback for queued legacy signals without an immediately applied episode.
	// A 5xx triggers protection; it is not itself a quality verdict.
	if plan.TriggerSource == quality5xxSource && plan.PelicanConfig.Quality != nil &&
		plan.PelicanConfig.Quality.Action == QualityActionRemoveModel && plan.Quality5xxEpisode == 0 && applyTriggeredQuality {
		preCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		preAction, preErr := s.planRepo.ApplyQualityOutcome(preCtx, plan, until, "pending")
		stop()
		if preErr != nil || preAction == "stale_run" || preAction == "account_deleted" {
			return false
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	models := []string{plan.ModelID}
	if len(plan.PelicanConfig.ModelIDs) > 0 {
		models = plan.PelicanConfig.ModelIDs
	}
	results := make([]*ScheduledTestResult, len(models)*plan.PelicanConfig.ParallelCount)
	var randomSamples []*PelicanTestConfig
	var randomQuestionErr error
	var sharedBatch *QualitySharedBatch
	if plan.PelicanConfig.QuestionKind == QualityRandomQuestionKind {
		candidate, _ := ctx.Value(qualitySharedBatchContextKey{}).(*QualitySharedBatch)
		if candidate == nil {
			candidate, randomQuestionErr = newQualitySharedBatch(now)
		}
		repo := s.sharedQualityBatchRepo()
		if repo == nil {
			randomQuestionErr = fmt.Errorf("quality shared batch storage unavailable")
		} else if randomQuestionErr == nil {
			sharedBatch, randomQuestionErr = repo.StartQualityBatch(runCtx, candidate, plan, until)
		}
		if randomQuestionErr == nil {
			randomSamples, randomQuestionErr = sharedQualitySamples(plan.PelicanConfig, sharedBatch)
		}
	}
	// 糖果题每个并行样本换一个题面，同一并行样本下的所有模型共用；不修改存库的 plan.PelicanConfig。
	var candySamples []*PelicanTestConfig
	if isCandyQuestion(plan.PelicanConfig) {
		candySamples = newCandyVariantSamples(plan.PelicanConfig, len(results)/len(models))
	}
	// Samples are interleaved by model so each model can start before repeats.
	// Bound upstream concurrency while waiting for every result, even failures.
	jobs := make(chan int, len(results))
	for i := range results {
		jobs <- i
	}
	close(jobs)
	var unsupported sync.Map
	var allowed []string
	var catalogErr error
	if plan.PelicanConfig.Quality != nil {
		allowed, _, catalogErr = s.scheduledSvc.eligibleQualityModels(runCtx, plan)
	}
	var wg sync.WaitGroup
	workers := len(results)
	if workers > 8 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				samplePlan := *plan
				cfg := *plan.PelicanConfig
				samplePlan.ModelID = models[index%len(models)]
				cfg.ModelID = samplePlan.ModelID
				if randomSamples != nil {
					cfg = *randomSamples[index/len(models)]
					cfg.ModelID = samplePlan.ModelID
					if cfg.QualityBatch != nil {
						evidence := *cfg.QualityBatch
						cfg.QualityBatch = &evidence
					}
				} else if candySamples != nil {
					cfg = *candySamples[index/len(models)]
					cfg.ModelID = samplePlan.ModelID
				}
				samplePlan.PelicanConfig = &cfg
				if randomQuestionErr != nil {
					results[index] = &ScheduledTestResult{Status: "failed", ErrorMessage: "random_question_generation_failed", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg,
						QualityJudgment: &QualityJudgment{Verdict: "unknown", Reason: "random_question_generation_failed"}}
					continue
				}
				_, excluded := unsupported.Load(samplePlan.ModelID)
				if plan.PelicanConfig.Quality != nil && (catalogErr != nil || excluded || !containsString(allowed, samplePlan.ModelID)) {
					reason := "model_unavailable"
					if excluded {
						reason = "model_unsupported"
					}
					if catalogErr != nil {
						reason = "model_catalog_unavailable"
					}
					results[index] = &ScheduledTestResult{Status: "skipped", ErrorMessage: reason, StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg}
					continue
				}
				result := s.runPelicanSample(runCtx, &samplePlan)
				if plan.PelicanConfig.Quality != nil && qualityModelUnsupported(result.ErrorMessage) {
					unsupported.Store(samplePlan.ModelID, true)
					persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(runCtx), 3*time.Second)
					persistErr := s.scheduledSvc.rememberUnsupportedQualityModel(persistCtx, plan.AccountID, samplePlan.ModelID)
					persistCancel()
					if persistErr != nil {
						logger.LegacyPrintf("service.scheduled_test_runner", "quality model exclusion could not be saved: account=%d", plan.AccountID)
					}
					result.Status = "skipped"
					result.ErrorMessage = "model_unsupported"
					result.QualityJudgment = nil
				}
				results[index] = result
			}
		}()
	}
	wg.Wait()
	// Persist timeout failures with a fresh context even after the request deadline.
	saveCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if sharedBatch != nil && randomQuestionErr == nil {
		for _, result := range results {
			result.PlanID = plan.ID
			result.QualityRoundID = until.Format(time.RFC3339Nano)
			result.QualityAction = "comparison_pending"
		}
		applyBatch := applyTriggeredQuality
		if plan.TriggerSource == quality5xxSource && plan.TriggerObservedAt != nil && s.qualityTrigger != nil {
			applyBatch = applyBatch && s.qualityTrigger.signalIsCurrent(plan.AccountID, *plan.TriggerObservedAt)
		}
		run := &QualityBatchRun{Apply: applyBatch, Plan: plan, Until: until, Results: results, Episode: plan.Quality5xxEpisode,
			Trigger: plan.TriggerSource, ObservedAt: plan.TriggerObservedAt}
		if err := s.sharedQualityBatchRepo().CollectQualityBatch(saveCtx, run); err != nil {
			logQualityBatchFailure()
			return false
		}
		// Release worker/concurrency slots before waiting for other accounts.
		// A later tick finalizes this durable run using the original lease/version.
		return true
	}
	if plan.PelicanConfig.Quality != nil && plan.PelicanConfig.Quality.UsesModelResults() {
		plan.QualityModelOutcomes = qualityPlanModelOutcomes(plan, results)
	}
	qualityAction := ""
	fieldWatchOutcome := ""
	if plan.PelicanConfig.Quality != nil {
		qualityAction = "inconclusive"
		freshSignal := true
		if plan.TriggerSource == quality5xxSource && plan.TriggerObservedAt != nil && s.qualityTrigger != nil {
			freshSignal = s.qualityTrigger.signalIsCurrent(plan.AccountID, *plan.TriggerObservedAt)
		}
		if fieldWatch {
			// 字段看护复核：只有答错才按规则处置（移出 / 移入分组或仅记录）；答对不处置只记警告；
			// 无法判断不算定论。字段看护不自动恢复，答对也不调用处置。
			fieldWatchOutcome = qualityRoundOutcome(results)
			switch fieldWatchOutcome {
			case "failed":
				s.releaseRestoredFieldWatchQuarantine(saveCtx, plan)
				var actionErr error
				qualityAction, actionErr = s.planRepo.ApplyQualityOutcome(saveCtx, plan, until, fieldWatchOutcome)
				if actionErr != nil {
					qualityAction = "action_error"
					logger.LegacyPrintf("service.scheduled_test_runner", "field watch plan=%d action failed: %v", plan.ID, actionErr)
				}
			case "passed":
				qualityAction = "no_change"
			}
		} else if applyTriggeredQuality && freshSignal && (qualityRoundHasResults(results) || plan.PelicanConfig.Quality.Action == QualityActionOpenAITwoFAAdmission) {
			var actionErr error
			roundOutcome := qualityRoundOutcome(results)
			if roundOutcome == "failed" && !plan.PelicanConfig.Quality.AutoRestore {
				// 不自动恢复的移出分组规则：号被人工放回承载分组后又答错，先丢掉过期的隔离记录，
				// 否则会按「保持隔离」处理而不再移出分组。
				s.releaseRestoredFieldWatchQuarantine(saveCtx, plan)
			}
			qualityAction, actionErr = s.planRepo.ApplyQualityOutcome(saveCtx, plan, until, roundOutcome)
			if actionErr != nil {
				qualityAction = "action_error"
				logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d action failed: %v", plan.ID, actionErr)
			}
		}
	}
	succeeded := false
	allSaved := true
	for _, result := range results {
		result.QualityAction = qualityAction
		if plan.PelicanConfig.Quality != nil {
			result.QualityRoundID = until.Format(time.RFC3339Nano)
			result.PelicanConfig.QualityModelOutcomes = plan.QualityModelOutcomes
			result.PelicanConfig.QualityRestoreDiagnostic = nil
			if qualityAction == "restore_conflict" {
				result.PelicanConfig.QualityRestoreDiagnostic = plan.QualityRestoreDiagnostic
			}
			// A rejected transaction must never advertise uncommitted changes.
			if qualityAction != "action_error" && qualityAction != "action_conflict" && qualityAction != "restore_conflict" && qualityAction != "stale_run" {
				result.PelicanConfig.QualityModelActions = plan.QualityModelActions
			} else {
				result.PelicanConfig.QualityModelActions = nil
			}
		}
		if fieldWatch && fieldWatchOutcome == "passed" && plan.TriggerSource == qualityFieldWatchSource && result.PelicanConfig != nil {
			// 字段说降智、糖果题却答对：不处置，只在记录里留警告等人看。
			result.PelicanConfig.FieldWatchMismatch = true
			if result.QualityJudgment != nil {
				judgment := *result.QualityJudgment
				judgment.Reason = strings.TrimSpace(qualityFieldWatchMismatchReason + "；" + judgment.Reason)
				result.QualityJudgment = &judgment
			}
		}
		if result.Status == "success" {
			succeeded = true
		}
		if err := s.scheduledSvc.SaveResult(saveCtx, plan.ID, plan.MaxResults, result); err != nil {
			allSaved = false
			logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d save failed: %v", plan.ID, err)
		}
	}
	if cleanup, ok := s.planRepo.(QualityOAuth401Repository); ok {
		evidence := &QualityOAuth401Round{}
		if allSaved {
			evidence = QualityOAuth401Evidence(plan, results)
		}
		deleted, cleanupErr := cleanup.CompleteQualityOAuth401Round(saveCtx, plan, until, evidence)
		if cleanupErr != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "quality OAuth 401 cleanup plan=%d account=%d failed", plan.ID, plan.AccountID)
		}
		if deleted {
			return true // Native deletion cascaded results; do not write them again.
		}
	}
	if succeeded && plan.AutoRecover && plan.PelicanConfig.Quality == nil && !isBuiltinCandyPlan(plan.PelicanConfig) && !isOpenAICodexStateProbePlan(plan.PelicanConfig) {
		s.tryRecoverAccount(saveCtx, plan.AccountID, plan.ID)
	}
	if fieldWatch {
		plan.FieldWatchOutcome, plan.FieldWatchReason = fieldWatchRoundOutcome(plan.TriggerSource, fieldWatchOutcome, qualityAction)
	}
	finished := time.Now()
	if plan.TriggerSource == quality5xxSource {
		// The stored rule was validated before claiming. The execution-only
		// question may now be candy even for a state-probe BPS recovery rule.
		following, nextErr := computeNextRun(plan.CronExpression, finished)
		if nextErr != nil {
			return false
		}
		err = s.planRepo.FinishTriggeredQuality(saveCtx, plan, until, finished, following)
	} else {
		err = s.planRepo.FinishPelican(saveCtx, plan.ID, until, finished)
	}
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, err)
		return false
	}
	return true
}

// Each sample runs in its own goroutine, outside Gin recovery. Always return a
// result so an upstream adapter or judge panic cannot kill the process or leave
// the persistence loop with a nil entry. Panic values may contain request data.
func (s *ScheduledTestRunnerService) runPelicanSample(ctx context.Context, plan *ScheduledTestPlan) (result *ScheduledTestResult) {
	started := time.Now()
	failure := func(message string) *ScheduledTestResult {
		finished := time.Now()
		return &ScheduledTestResult{Status: "failed", ErrorMessage: message, StartedAt: started, FinishedAt: finished, LatencyMs: finished.Sub(started).Milliseconds(), PelicanConfig: plan.PelicanConfig}
	}
	defer func() {
		if recover() != nil {
			result = failure("scheduled_test_panic: background sample failed")
			logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d account=%d sample panicked", plan.ID, plan.AccountID)
		}
	}()
	if plan.PelicanConfig.QuestionKind == QualityRandomQuestionKind && (plan.PelicanConfig.RandomQuestion == nil || plan.PelicanConfig.Prompt == "" || plan.PelicanConfig.Quality == nil || plan.PelicanConfig.Quality.ExpectedAnswer == "") {
		return failure("random_question_not_prepared")
	}
	if batch := plan.PelicanConfig.QualityBatch; batch != nil && !time.Now().Before(batch.StartDeadline) {
		return failure("quality_batch_start_window_closed")
	}
	var err error
	result, err = s.runPelicanAnswer(ctx, plan)
	if err != nil {
		return failure(fmt.Sprint(err))
	}
	if result == nil {
		return failure("scheduled_test_empty_result: background sample returned no result")
	}
	result.PelicanConfig = plan.PelicanConfig
	// 探针题型的结果自带 correct/incorrect/unknown 判定，不经过判题模型。
	if plan.PelicanConfig.Quality != nil && result.Status == "success" && !isOpenAICodexStateProbePlan(plan.PelicanConfig) {
		var judgment *QualityJudgment
		if isCandyQuestion(plan.PelicanConfig) {
			// 糖果题答案固定，一律内置判分；规则里配了判题模型也忽略，不占用客户并发。
			g := GradeCandyAnswer(result.ResponseText, 21)
			switch g.Outcome {
			case CandyOutcomeCorrect:
				judgment = &QualityJudgment{Verdict: "correct", Reason: "builtin_candy_" + g.Format, AccountID: plan.AccountID}
			case CandyOutcomeWrong:
				judgment = &QualityJudgment{Verdict: "incorrect", Reason: "builtin_candy_" + g.Format + ": answered " + g.Value, AccountID: plan.AccountID}
			default:
				judgment = &QualityJudgment{Verdict: "unknown", Reason: "builtin_candy_no_answer", AccountID: plan.AccountID}
			}
		} else if s.judgeQuality != nil {
			judgment = s.judgeQuality(ctx, plan.AccountID, plan.PelicanConfig, result.ResponseText)
		}
		applyQualityJudgment(result, judgment)
		result.FinishedAt = time.Now()
		if judgment == nil || judgment.Kind != "jev" {
			result.LatencyMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		}
	}
	return result
}

// The generated SSE is captured in memory, so cap it before buffering, not just at persistence.
type pelicanRecorder struct {
	*httptest.ResponseRecorder
	cancel   context.CancelFunc
	overflow bool
}

func (w *pelicanRecorder) Write(data []byte) (int, error) {
	if w.overflow || w.Body.Len()+len(data) > 4<<20 {
		w.overflow = true
		w.cancel()
		return 0, io.ErrShortWrite
	}
	return w.ResponseRecorder.Write(data)
}
func (w *pelicanRecorder) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

func parsePelicanOutput(body string) (string, string) {
	output, message := parseTestSSEOutput(body)
	complete := false
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event TestEvent
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		if event.Type == "test_complete" {
			complete = event.Success
			if !event.Success && message == "" {
				message = "Generation did not complete successfully"
			}
		}
	}
	if !complete && message == "" {
		message = "Generation stream ended before completion"
	}
	return output, message
}

func isBuiltinCandyPlan(cfg *PelicanTestConfig) bool {
	return cfg != nil && strings.TrimSpace(cfg.Prompt) == strings.TrimSpace(CandyPrompt)
}

func isCandyQuestion(cfg *PelicanTestConfig) bool {
	return cfg != nil && (cfg.QuestionKind == "candy" || isBuiltinCandyPlan(cfg))
}

// newCandyVariant 生成一个随机糖果题面；熵不可用时退回原题面，不让整轮失败。
func newCandyVariant() CandyVariant {
	var seed [16]byte
	if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
		return OriginalCandyVariant()
	}
	rng := mathrand.New(mathrand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:]))) //nolint:gosec
	return RenderCandyVariant(rng)
}

// newCandyVariantSamples 为每个并行样本准备一个题面；同一并行样本下的所有模型共用同一个题面。
func newCandyVariantSamples(cfg *PelicanTestConfig, count int) []*PelicanTestConfig {
	if count <= 0 {
		return nil
	}
	samples := make([]*PelicanTestConfig, count)
	for i := range samples {
		variant := newCandyVariant()
		snapshot := *cfg
		snapshot.Prompt = variant.Prompt
		snapshot.CandyVariant = variant.Signature
		samples[i] = &snapshot
	}
	return samples
}

// Missing kind preserves HTML validation for saved plans from older versions.
func intelligenceTestPrompt(cfg *PelicanTestConfig) string {
	if isCandyQuestion(cfg) {
		// 糖果题题面已含 JSON 作答要求，不能再追加「只输出最终整数」，否则要求冲突。
		if cfg.CandyVariant == "" {
			return newCandyVariant().Prompt
		}
		return cfg.Prompt
	}
	if cfg.Quality != nil {
		return cfg.Prompt + "\n\n只输出最终答案，不要解释。"
	}
	if cfg.QuestionKind == ChannelMonitorV3ProbeQuestionKind {
		// The V1 challenge already asks for the bare number.
		return cfg.Prompt
	}
	return cfg.Prompt + "\n\n" + PelicanDeliveryContract
}
func intelligenceTestOutputError(cfg *PelicanTestConfig, output string) string {
	if cfg.Quality != nil {
		// Completed answers are graded by the configured model in the runner.
		if strings.TrimSpace(output) == "" {
			return pelicanErrEmptyOutput
		}
		return ""
	}
	if strings.TrimSpace(output) == "" {
		return pelicanErrEmptyOutput
	}
	if cfg.QuestionKind == ChannelMonitorV3ProbeQuestionKind {
		// The V3 prober checks the arithmetic answer itself.
		return ""
	}
	if isCandyQuestion(cfg) {
		switch g := GradeCandyAnswer(output, 21); g.Outcome {
		case CandyOutcomeWrong:
			return "answer_mismatch: expected 21, got " + g.Value
		case CandyOutcomeNoAnswer:
			return "answer_missing: expected 21"
		}
		return ""
	}
	if !pelicanHTMLPattern.MatchString(output) {
		return "Model did not return HTML or SVG"
	}
	return ""
}
