package service

import (
	"bytes"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// codexFieldWatchDiag 是字段看护的诊断采样（2026-11-03 加）：线上发现满血号的 astra 请求
// 不是每个都带 safety_buffering，用它弄清「不带字段的请求长什么样」。只记录事件类型、
// 输出项类型和计数，不记录任何请求或回答内容；只在请求被计入字段看护时输出一行日志。
type codexFieldWatchDiag struct {
	frames      int
	markFrames  int
	trueFrames  int
	events      map[string]int // 事件类型 → 数据帧数
	marked      map[string]int // 事件类型 → 带 safety_buffering 的帧数
	outputItems map[string]int // response.output_item.added 的 item.type → 个数
	status      string         // 终态事件里的 response.status
}

const codexFieldWatchDiagMaxKeys = 48

func newCodexFieldWatchDiag() *codexFieldWatchDiag {
	return &codexFieldWatchDiag{events: map[string]int{}, marked: map[string]int{}, outputItems: map[string]int{}}
}

func codexFieldWatchDiagBump(m map[string]int, key string) {
	if key == "" {
		key = "-"
	}
	if _, ok := m[key]; !ok && len(m) >= codexFieldWatchDiagMaxKeys {
		key = "other"
	}
	m[key]++
}

// observe 记录一帧（用改写前的原始数据）。
func (d *codexFieldWatchDiag) observe(eventType string, data []byte) {
	if d == nil {
		return
	}
	eventType = strings.TrimSpace(eventType)
	d.frames++
	codexFieldWatchDiagBump(d.events, eventType)
	if bytes.Contains(data, []byte(codexAstraWatchMarker)) {
		d.markFrames++
		codexFieldWatchDiagBump(d.marked, eventType)
		if bytes.Contains(data, []byte(`"safety_buffering":true`)) {
			d.trueFrames++
		}
	}
	switch eventType {
	case "response.output_item.added":
		codexFieldWatchDiagBump(d.outputItems, gjson.GetBytes(data, "item.type").String())
	case "response.completed", "response.incomplete", "response.failed":
		d.status = gjson.GetBytes(data, "response.status").String()
	}
}

func codexFieldWatchDiagFormat(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, ",")
}

func (d *codexFieldWatchDiag) log(accountID int64, originalModel, mappedModel, effort string, sawMarker bool) {
	if d == nil {
		return
	}
	slog.Info("codex_field_watch_sample",
		"account_id", accountID,
		"model", originalModel,
		"mapped_model", mappedModel,
		"effort", effort,
		"marker", sawMarker,
		"frames", d.frames,
		"marker_frames", d.markFrames,
		"marker_true_frames", d.trueFrames,
		"status", d.status,
		"output_items", codexFieldWatchDiagFormat(d.outputItems),
		"events", codexFieldWatchDiagFormat(d.events),
		"marked_events", codexFieldWatchDiagFormat(d.marked))
}
