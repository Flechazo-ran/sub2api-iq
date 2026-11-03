package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// OpenAICodexAstraProbeQuestionKind 是「astra 探针」题型：不下发题目，只发一次最小请求，
// 用响应流里是否出现 safety_buffering 判断这个账号当前走的是哪条上游流水线。
//
// 判据来源（2026-11 抓包实测，19 个活跃账号零例外）：
//   - 满血账号（Astra）：每个 SSE 事件都带 safety_buffering:false —— 说明输出经过
//     「Astra + 安全缓冲层」这条流水线，缓冲未触发替换。
//   - 降智账号（luna）：完全没有该字段 —— 直接被派给 gpt-6-luna，没有缓冲包装。
//
// 该字段只由上游在响应中下发，客户端任何位置注入都会被严格校验拒绝
// （body 顶层/camelCase/metadata/include/reasoning 全部 400；仅请求头与
// client_metadata 能过校验但对结果无影响），因此只能读、不能伪造。
const OpenAICodexAstraProbeQuestionKind = "astra_probe"

const (
	// openAICodexAstraDegradedError 是降智结果的固定标记，参与质量判定。
	openAICodexAstraDegradedError = "astra_degraded"
	// openAICodexAstraProbeInconclusivePrefix 是无法判断时的失败分类前缀，只作展示。
	openAICodexAstraProbeInconclusivePrefix = "astra_probe_inconclusive"

	// openAICodexAstraProbeDefaultModel 是探针默认模型。
	openAICodexAstraProbeDefaultModel = "gpt-6-astra"
	// openAICodexAstraProbeTimeout 限制单次探针总时长。
	openAICodexAstraProbeTimeout = 45 * time.Second
	// openAICodexAstraProbeMaxBody 只读取响应流的前若干字节，足够覆盖首个事件即可。
	openAICodexAstraProbeMaxBody = 256 << 10
	// openAICodexAstraProbeMarker 是判据字段名。
	openAICodexAstraProbeMarker = "safety_buffering"
)

func isOpenAICodexAstraProbePlan(cfg *PelicanTestConfig) bool {
	return cfg != nil && cfg.QuestionKind == OpenAICodexAstraProbeQuestionKind
}

// openAICodexAstraProbeResult 是一次 astra 探针的结果。
type openAICodexAstraProbeResult struct {
	AccountID  int64
	Model      string
	Healthy    bool   // true=Astra 满血；false=luna 降智
	Inconclusive bool // true=未能判断（网络/鉴权/非 200），不算降智证据
	Reason     string
	LatencyMs  int64
	StartedAt  time.Time
	FinishedAt time.Time
}

// runOpenAICodexAstraProbeScheduled 以定时测试结果的形态跑一次 astra 探针。
// 满血 = success；降智 = failed + astra_degraded；无法判断 = failed + 失败分类（不算降智证据）。
func (s *AccountTestService) runOpenAICodexAstraProbeScheduled(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
	probe, err := s.probeOpenAICodexAstra(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	snapshot := *cfg
	snapshot.ModelID = probe.Model
	summary := "astra 探针："
	if probe.Healthy {
		summary += "满血（响应流带 safety_buffering）"
	} else if probe.Inconclusive {
		summary += "无法判断（" + probe.Reason + "）"
	} else {
		summary += "降智（响应流无 safety_buffering）"
	}
	result := &ScheduledTestResult{
		Status:        "failed",
		ResponseText:  summary,
		LatencyMs:     probe.LatencyMs,
		StartedAt:     probe.StartedAt,
		FinishedAt:    probe.FinishedAt,
		PelicanConfig: &snapshot,
	}
	switch {
	case probe.Healthy:
		result.Status = "success"
	case probe.Inconclusive:
		result.ErrorMessage = openAICodexAstraProbeInconclusivePrefix + ": " + probe.Reason
	default:
		result.ErrorMessage = openAICodexAstraDegradedError
	}
	if cfg.Quality != nil {
		verdict := "incorrect"
		if probe.Healthy {
			verdict = "correct"
		} else if probe.Inconclusive {
			verdict = "unknown"
		}
		result.QualityJudgment = &QualityJudgment{Verdict: verdict, Reason: summary, AccountID: accountID}
	}
	return result, nil
}

// probeOpenAICodexAstra 解析账号并发一次最小请求，按 safety_buffering 是否存在定性。
func (s *AccountTestService) probeOpenAICodexAstra(ctx context.Context, accountID int64, model string) (*openAICodexAstraProbeResult, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errAccountTestServiceUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrAccountNotFound
	}
	return s.openaiGatewayService.probeOpenAICodexAstra(ctx, account, model), nil
}

// probeOpenAICodexAstra 是 astra 探针的网关实现：发一次极小请求，读响应流找判据字段。
func (s *OpenAIGatewayService) probeOpenAICodexAstra(ctx context.Context, account *Account, model string) *openAICodexAstraProbeResult {
	started := time.Now()
	result := &openAICodexAstraProbeResult{StartedAt: started, Inconclusive: true}
	defer func() {
		result.FinishedAt = time.Now()
		result.LatencyMs = result.FinishedAt.Sub(started).Milliseconds()
		if result.Reason == "" {
			if result.Healthy {
				result.Reason = "响应流带 safety_buffering，判定为 Astra 满血"
			} else if result.Inconclusive {
				result.Reason = "未能判断"
			} else {
				result.Reason = "响应流无 safety_buffering，判定为 luna 降智"
			}
		}
	}()
	if account == nil {
		result.Reason = "账号不存在"
		return result
	}
	result.AccountID = account.ID
	requested := strings.TrimSpace(model)
	if requested == "" {
		requested = openAICodexAstraProbeDefaultModel
	}
	upstreamModel := normalizeOpenAIModelForUpstream(account, account.GetMappedModel(requested))
	result.Model = upstreamModel
	if s == nil || s.httpUpstream == nil {
		result.Reason = "OpenAI 网关不可用"
		return result
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil || strings.TrimSpace(token) == "" {
		result.Reason = "账号认证失败"
		return result
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account); err != nil {
		result.Reason = "账号上游标识解析失败"
		return result
	}
	proxy := openAIAccountProxyURL(account)

	probeCtx, cancel := context.WithTimeout(ctx, openAICodexAstraProbeTimeout)
	defer cancel()
	body := []byte(`{"model":` + jsonString(upstreamModel) +
		`,"instructions":"You are a helpful assistant.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"stream":true,"store":false}`)
	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		result.Reason = "请求构造失败"
		return result
	}
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header = headers.Clone()
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	for _, name := range []string{openAICodexTurnStateHeader, "Cookie", "Content-Length", "Content-Encoding", "conversation_id", "x-codex-turn-metadata"} {
		req.Header.Del(name)
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		result.Reason = "上游请求失败"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		result.Reason = "上游返回非 200"
		return result
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, openAICodexAstraProbeMaxBody))
	// 读到判据即可定性；没读到说明这次响应没有该字段（降智流水线）。
	result.Healthy = bytes.Contains(snippet, []byte(openAICodexAstraProbeMarker))
	result.Inconclusive = false
	return result
}

// errAccountTestServiceUnavailable 表示测试服务未初始化。
var errAccountTestServiceUnavailable = errors.New("account test service unavailable")
