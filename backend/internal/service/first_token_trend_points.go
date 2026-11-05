package service

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// 号的变动从这几处还原（没有专门的历史表）：
//   - 审计日志：单号编辑 / 批量编辑里的 proxy_id、concurrency、status、schedulable，
//     开关调度、清错误、恢复状态。审计只存新值，旧值取同一个号上一次已知的值；
//     一个号某字段的第一条记录只当起点，不算变动（不知道之前是什么）。
//     批量编辑用筛选条件（没写 account_ids）的那种还原不出来是哪些号，跳过。
//   - proxy_autoswitch_events：代理优选自己换的（带旧代理），以及换回。
//   - ops_system_logs：认证失败把号标成 error（account_disabled_auth_error、账号测试返回 401/403），
//     连接错误临时停调度（openai.account_temp_unscheduled_transport）。
//   - account_schedulable_timer_audit：定时开关调度实际执行的记录。
//   - 兜底：号现在是 error、但上面都没记到，就按账号 updated_at 估一个时间（会在 detail 里写明）。
//
// 还原不出来的：限流 / 过载造成的临时不可调度（只在内存和 Redis 里），以及其它不走审计、
// 不写日志的内部改动。
const (
	fttFieldProxy       = "proxy"
	fttFieldConcurrency = "concurrency"
	fttFieldStatus      = "status"
	fttFieldSchedulable = "schedulable"
	fttFieldTemp        = "temp"

	fttActionUpdate      = "admin.accounts.update"
	fttActionBulkUpdate  = "admin.accounts.bulk_update.create"
	fttActionSchedulable = "admin.accounts.schedulable.create"
	fttActionClearError  = "admin.accounts.clear_error.create"
	fttActionRecover     = "admin.accounts.recover_state.create"

	fttLogAuthError     = "account_disabled_auth_error"
	fttLogTestFailed    = "account_test.failed"
	fttLogTempTransport = "openai.account_temp_unscheduled_transport"
)

// FirstTokenTrendAuditActions 是仓储要捞的审计动作。
var FirstTokenTrendAuditActions = []string{fttActionUpdate, fttActionBulkUpdate, fttActionSchedulable, fttActionClearError, fttActionRecover}

// FirstTokenTrendStatusLogMessages 是仓储要捞的系统日志；account_test.failed 只要 401/403 的。
var FirstTokenTrendStatusLogMessages = []string{fttLogAuthError, fttLogTestFailed, fttLogTempTransport}

type fttPoint struct {
	accountID     int64
	at            time.Time
	order         int
	field         string
	to            string
	from          *string
	actor         string
	source        string
	note          string
	estimated     bool
	setsUnsched   bool // SetError 同时把 schedulable 置 false
	emitIfUnknown bool // 不知道旧值也算一次变动（标 error 这类信号）
	silent        bool // 只更新状态、不出变动（标 error 时顺带的停调度）
}

func fttStr(v string) *string { return &v }

func fttProxyValue(id *int64) string {
	if id == nil || *id <= 0 {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}

func parseFirstTokenTrendAudit(w FirstTokenTrendAuditWrite, order int) []fttPoint {
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w.RequestBody), &body); err != nil && w.Action != fttActionClearError && w.Action != fttActionRecover {
		return nil
	}
	var targets []int64
	if w.Action == fttActionBulkUpdate {
		if raw, ok := body["account_ids"]; ok {
			_ = json.Unmarshal(raw, &targets)
		}
	} else if id, err := strconv.ParseInt(strings.TrimSpace(w.ParamID), 10, 64); err == nil && id > 0 {
		targets = []int64{id}
	}
	if len(targets) == 0 {
		return nil
	}
	actor := w.ActorEmail
	if actor == "" {
		actor = "后台"
	}
	type fv struct{ field, value string }
	var values []fv
	switch w.Action {
	case fttActionClearError, fttActionRecover:
		values = append(values, fv{fttFieldStatus, StatusActive})
	default:
		if raw, ok := body["proxy_id"]; ok {
			var id *int64
			if err := json.Unmarshal(raw, &id); err == nil && id != nil { // null = 没改
				values = append(values, fv{fttFieldProxy, fttProxyValue(id)}) // 0 = 清掉代理
			}
		}
		if raw, ok := body["concurrency"]; ok {
			var c *int
			if err := json.Unmarshal(raw, &c); err == nil && c != nil {
				values = append(values, fv{fttFieldConcurrency, strconv.Itoa(*c)})
			}
		}
		if raw, ok := body["status"]; ok {
			var st string
			if err := json.Unmarshal(raw, &st); err == nil && strings.TrimSpace(st) != "" {
				values = append(values, fv{fttFieldStatus, strings.TrimSpace(st)})
			}
		}
		if raw, ok := body["schedulable"]; ok {
			var sch *bool
			if err := json.Unmarshal(raw, &sch); err == nil && sch != nil {
				values = append(values, fv{fttFieldSchedulable, strconv.FormatBool(*sch)})
			}
		}
	}
	out := make([]fttPoint, 0, len(targets)*len(values))
	for _, id := range targets {
		for _, v := range values {
			out = append(out, fttPoint{accountID: id, at: w.CreatedAt, order: order, field: v.field, to: v.value,
				actor: actor, source: FirstTokenTrendSourceAudit})
		}
	}
	return out
}
