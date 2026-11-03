package service

import "net/http"

// codexSafetyBufferingInjectExtraKey 是账号级实验开关：开启后向 Codex 上游
// 补注安全缓冲声明头，用于对照观察该账号被分配到哪条流水线。
//
// 背景（2026-11 实测）：
//   - `safety_buffering` 只由上游在 /responses 的 SSE 事件里下发，是「结果」而非「开关」；
//     满血账号每个事件都带 `safety_buffering:false`，被降档（luna）的账号完全不出现该字段。
//   - 客户端把它放进请求体会被严格校验拒绝：顶层 → `Unsupported parameter: safety_buffering`；
//     驼峰 → `Unsupported parameter: safetyBuffering`；metadata → `Unsupported parameter: metadata`；
//     include 数组 → `Invalid value`（合法值里没有它）；reasoning 内嵌 → `Unknown parameter`。
//   - 额外对比：19 个活跃账号里，只有满血号带该字段，其余全部不带，且同一后端集群内
//     两个账号（208 分片上的好号与坏号）仍一好一坏 → 判定发生在账号层，与分片无关。
//
// 因此这里只做「补注请求头」这一条能被上游接受的合法路径（实测返回 200），
// 默认关闭，仅用于新号从首笔请求起持续注入的对照实验。
const codexSafetyBufferingInjectExtraKey = "codex_safety_buffering_inject"

// codexSafetyBufferingInjectHeader 是补注时使用的上游头名。
const codexSafetyBufferingInjectHeader = "x-codex-safety-buffering"

// codexSafetyBufferingInjectEnabled 判断账号是否开启补注。默认关闭。
func codexSafetyBufferingInjectEnabled(account *Account) bool {
	if account == nil {
		return false
	}
	return account.Extra[codexSafetyBufferingInjectExtraKey] == true
}

// applyCodexSafetyBufferingInjectHeader 在出站请求上补注安全缓冲声明头。
// 必须在身份收口（enforceCodexIdentityHeadersWithUA / applyOpenAIAPIKeyIdentityHeaders）
// 之后调用，避免被后续的头部清洗覆盖。
func applyCodexSafetyBufferingInjectHeader(account *Account, h http.Header) {
	if h == nil || !codexSafetyBufferingInjectEnabled(account) {
		return
	}
	h.Set(codexSafetyBufferingInjectHeader, "true")
}
