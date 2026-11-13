package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// pasWorld is an in-memory stand-in for the database, proxies, admin edit path
// and probes used by ProxyAutoSwitchService tests.
type pasWorld struct {
	mu       sync.Mutex
	now      time.Time
	accounts map[int64]*ProxyAutoSwitchAccount
	groupsOf map[int64][]int64
	proxies  map[int64]*Proxy
	usage    []FirstTokenTrendUsageSample
	traces   []FirstTokenTrendTraceSample

	events    []ProxyAutoSwitchEvent
	nextEvent int64
	updates   [][2]int64
	updateErr error
	audits    []FirstTokenTrendAuditWrite

	probeMS  map[int64][]any // proxy id -> sequence of int64 ms or error, last value repeats
	settings map[string]string
	groups   map[int64]*Group
	latency  map[int64]*ProxyLatencyInfo
}

func newPASWorld() *pasWorld {
	return &pasWorld{
		now:      time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		accounts: map[int64]*ProxyAutoSwitchAccount{},
		groupsOf: map[int64][]int64{},
		proxies:  map[int64]*Proxy{},
		probeMS:  map[int64][]any{},
		settings: map[string]string{},
		groups: map[int64]*Group{
			16: {ID: 16, Name: "GPT-速刷-AUTO", Platform: PlatformOpenAI},
			9:  {ID: 9, Name: "Claude", Platform: PlatformAnthropic},
		},
		latency: map[int64]*ProxyLatencyInfo{},
	}
}

func (w *pasWorld) addProxy(id int64, region string, expiresInDays int) *Proxy {
	p := &Proxy{ID: id, Name: fmt.Sprintf("P%d", id), Protocol: "http", Host: fmt.Sprintf("10.0.0.%d", id), Port: 8000, Status: StatusActive}
	if expiresInDays > 0 {
		at := w.now.Add(time.Duration(expiresInDays) * 24 * time.Hour)
		p.ExpiresAt = &at
	}
	w.proxies[id] = p
	if region != "" {
		w.latency[id] = &ProxyLatencyInfo{Success: true, CountryCode: region}
	}
	return p
}

func (w *pasWorld) addAccount(id, proxyID int64) *ProxyAutoSwitchAccount {
	a := &ProxyAutoSwitchAccount{ID: id, Name: fmt.Sprintf("acc-%d", id), Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, GroupIDs: []int64{16}}
	if proxyID > 0 {
		a.ProxyID = autoSwitchInt64(proxyID)
	}
	w.accounts[id] = a
	w.groupsOf[id] = []int64{16}
	return a
}

func (w *pasWorld) setProbe(proxyID int64, values ...any) { w.probeMS[proxyID] = values }

func (w *pasWorld) advance(d time.Duration) { w.mu.Lock(); w.now = w.now.Add(d); w.mu.Unlock() }

func (w *pasWorld) clock() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now }

func (w *pasWorld) usedCount(proxyID int64) int64 {
	n := int64(0)
	for _, a := range w.accounts {
		if !a.Deleted && a.ProxyID != nil && *a.ProxyID == proxyID {
			n++
		}
	}
	return n
}

func (w *pasWorld) event(id int64) *ProxyAutoSwitchEvent {
	for i := range w.events {
		if w.events[i].ID == id {
			return &w.events[i]
		}
	}
	return nil
}

func (w *pasWorld) lastEvent() *ProxyAutoSwitchEvent {
	if len(w.events) == 0 {
		return nil
	}
	return &w.events[len(w.events)-1]
}

func clonePASEvent(e ProxyAutoSwitchEvent) ProxyAutoSwitchEvent {
	cp := e
	if e.Before != nil {
		b := *e.Before
		cp.Before = &b
	}
	if e.After != nil {
		a := *e.After
		cp.After = &a
	}
	return cp
}

func (w *pasWorld) service() *ProxyAutoSwitchService {
	svc := NewProxyAutoSwitchService(w, w, pasGroups{w}, pasProxies{w}, w, w, w, w, nil, nil)
	svc.now = w.clock
	return svc
}

func (w *pasWorld) saveConfig(cfg ProxyAutoSwitchConfig) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	w.settings[proxyAutoSwitchConfigKey] = string(raw)
}

// --- settings ---

func (w *pasWorld) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := w.settings[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (w *pasWorld) Set(_ context.Context, key, value string) error {
	w.settings[key] = value
	return nil
}

// --- groups ---

type pasGroups struct{ w *pasWorld }

func (g pasGroups) GetByID(_ context.Context, id int64) (*Group, error) {
	if group, ok := g.w.groups[id]; ok {
		return group, nil
	}
	return nil, ErrGroupNotFound
}

// --- proxies ---

type pasProxies struct{ w *pasWorld }

func (p pasProxies) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if proxy, ok := p.w.proxies[id]; ok {
		cp := *proxy
		return &cp, nil
	}
	return nil, ErrProxyNotFound
}

func (p pasProxies) CountAccountsByProxyID(_ context.Context, proxyID int64) (int64, error) {
	return p.w.usedCount(proxyID), nil
}

// --- admin edit path ---

func (w *pasWorld) BulkUpdateAccounts(_ context.Context, input *BulkUpdateAccountsInput) (*BulkUpdateAccountsResult, error) {
	if w.updateErr != nil {
		return nil, w.updateErr
	}
	if input == nil || input.ProxyID == nil || len(input.AccountIDs) != 1 || input.Filters != nil {
		return nil, errors.New("bad update")
	}
	if input.Name != "" || input.Status != "" || input.Credentials != nil || input.Extra != nil || input.GroupIDs != nil ||
		input.Concurrency != nil || input.Priority != nil || input.Schedulable != nil {
		return nil, errors.New("autoswitch must only change proxy_id")
	}
	id := input.AccountIDs[0]
	a, ok := w.accounts[id]
	if !ok {
		return nil, errors.New("bad update")
	}
	a.ProxyID = autoSwitchInt64(*input.ProxyID)
	w.updates = append(w.updates, [2]int64{id, *input.ProxyID})
	return &BulkUpdateAccountsResult{Success: 1, SuccessIDs: []int64{id}, Results: []BulkUpdateAccountResult{{AccountID: id, Success: true}}}, nil
}

// --- latency cache / exit prober / chatgpt prober ---

func (w *pasWorld) GetProxyLatencies(_ context.Context, ids []int64) (map[int64]*ProxyLatencyInfo, error) {
	out := map[int64]*ProxyLatencyInfo{}
	for _, id := range ids {
		if info, ok := w.latency[id]; ok {
			out[id] = info
		}
	}
	return out, nil
}

func (w *pasWorld) SetProxyLatency(_ context.Context, id int64, info *ProxyLatencyInfo) error {
	w.latency[id] = info
	return nil
}

func (w *pasWorld) proxyIDByURL(url string) int64 {
	for id, p := range w.proxies {
		if p.URL() == url {
			return id
		}
	}
	return 0
}

func (w *pasWorld) ProbeProxy(_ context.Context, _ string) (*ProxyExitInfo, int64, error) {
	return nil, 0, errors.New("exit probe failed")
}

func (w *pasWorld) ProbeChatGPT(_ context.Context, proxyURL string) (ProxyAutoSwitchProbeResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.proxyIDByURL(proxyURL)
	seq := w.probeMS[id]
	if len(seq) == 0 {
		return ProxyAutoSwitchProbeResult{}, errors.New("no probe configured")
	}
	v := seq[0]
	if len(seq) > 1 {
		w.probeMS[id] = seq[1:]
	}
	switch val := v.(type) {
	case int:
		return ProxyAutoSwitchProbeResult{LatencyMS: int64(val)}, nil
	case int64:
		return ProxyAutoSwitchProbeResult{LatencyMS: val}, nil
	case error:
		return ProxyAutoSwitchProbeResult{}, val
	}
	return ProxyAutoSwitchProbeResult{}, errors.New("bad probe value")
}

// --- repository ---

func (w *pasWorld) ListGroupAccounts(_ context.Context, groupIDs []int64) ([]ProxyAutoSwitchAccount, error) {
	want := map[int64]bool{}
	for _, id := range groupIDs {
		want[id] = true
	}
	out := []ProxyAutoSwitchAccount{}
	for id, a := range w.accounts {
		if a.Deleted {
			continue
		}
		for _, g := range w.groupsOf[id] {
			if want[g] {
				cp := *a
				cp.GroupIDs = append([]int64(nil), a.GroupIDs...)
				out = append(out, cp)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (w *pasWorld) GetAccount(_ context.Context, id int64) (*ProxyAutoSwitchAccount, error) {
	if a, ok := w.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}

// FirstTokenUsage filters like the real queries: usage of the groups that
// overlap [from, to), traces of the groups started in [traceFrom, to).
func (w *pasWorld) FirstTokenUsage(_ context.Context, groupIDs []int64, from, to, traceFrom time.Time) ([]FirstTokenTrendUsageSample, []FirstTokenTrendTraceSample, error) {
	inGroups := func(g int64) bool {
		for _, id := range groupIDs {
			if id == g {
				return true
			}
		}
		return false
	}
	usage := []FirstTokenTrendUsageSample{}
	for _, u := range w.usage {
		if inGroups(16) && u.EndedAt.After(from) && u.StartedAt.Before(to) {
			usage = append(usage, u)
		}
	}
	traces := []FirstTokenTrendTraceSample{}
	for _, t := range w.traces {
		if inGroups(16) && !t.StartedAt.Before(traceFrom) && t.StartedAt.Before(to) {
			traces = append(traces, t)
		}
	}
	return usage, traces, nil
}

// addUsage adds one streaming usage_logs row that started at start and got
// its first token after ms; proxyID > 0 also adds the matching trace.
func (w *pasWorld) addUsage(accountID, proxyID int64, start time.Time, ms int64) {
	id := fmt.Sprintf("req-%d", len(w.usage)+1)
	v := ms
	w.usage = append(w.usage, FirstTokenTrendUsageSample{StartedAt: start, EndedAt: start.Add(time.Duration(ms+1000) * time.Millisecond),
		FirstTokenMS: &v, Stream: true, AccountID: accountID, Model: "gpt-5", RequestID: id})
	if proxyID > 0 {
		w.traces = append(w.traces, FirstTokenTrendTraceSample{StartedAt: start, AccountID: accountID, ProxyID: proxyID, Model: "gpt-5",
			AfterMS: float64(ms) / 2, RequestID: id})
	}
}

func (w *pasWorld) addUsages(accountID, proxyID int64, count int, start time.Time, ms int64) {
	for i := 0; i < count; i++ {
		w.addUsage(accountID, proxyID, start, ms)
	}
}

func (w *pasWorld) ListProxies(_ context.Context) ([]ProxyWithAccountCount, error) {
	out := []ProxyWithAccountCount{}
	for _, proxy := range w.proxies {
		out = append(out, ProxyWithAccountCount{Proxy: *proxy, AccountCount: w.usedCount(proxy.ID)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (w *pasWorld) InsertEvent(_ context.Context, e *ProxyAutoSwitchEvent) error {
	w.nextEvent++
	e.ID = w.nextEvent
	w.events = append(w.events, clonePASEvent(*e))
	return nil
}

func (w *pasWorld) UpdateEventIfStatus(_ context.Context, e *ProxyAutoSwitchEvent, expected string) (bool, error) {
	cur := w.event(e.ID)
	if cur == nil || cur.Status != expected {
		return false, nil
	}
	cp := clonePASEvent(*e)
	cp.ID = cur.ID
	*cur = cp
	return true, nil
}

func (w *pasWorld) GetEvent(_ context.Context, id int64) (*ProxyAutoSwitchEvent, error) {
	if e := w.event(id); e != nil {
		cp := clonePASEvent(*e)
		if a := w.accounts[e.AccountID]; a != nil {
			cp.AccountName = a.Name
		}
		return &cp, nil
	}
	return nil, nil
}

func (w *pasWorld) ListEvents(_ context.Context, f ProxyAutoSwitchEventFilter) ([]ProxyAutoSwitchEvent, error) {
	out := []ProxyAutoSwitchEvent{}
	for i := len(w.events) - 1; i >= 0; i-- {
		e := w.events[i]
		if (f.BeforeID > 0 && e.ID >= f.BeforeID) || (f.AccountID > 0 && e.AccountID != f.AccountID) {
			continue
		}
		out = append(out, clonePASEvent(e))
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

func (w *pasWorld) ListMeasuring(_ context.Context) ([]ProxyAutoSwitchEvent, error) {
	out := []ProxyAutoSwitchEvent{}
	for _, e := range w.events {
		if e.Status == ProxyAutoSwitchStatusMeasuring {
			out = append(out, clonePASEvent(e))
		}
	}
	return out, nil
}

func pasContains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func (w *pasWorld) AuditWrites(_ context.Context, _ []int64, since, until time.Time) ([]FirstTokenTrendAuditWrite, error) {
	out := []FirstTokenTrendAuditWrite{}
	for _, a := range w.audits {
		if !a.CreatedAt.Before(since) && !a.CreatedAt.After(until) {
			out = append(out, a)
		}
	}
	return out, nil
}

// manualProxyEdit 模拟后台人工改代理：改账号的 proxy_id，并写一条和真实接口一样的审计记录。
func (w *pasWorld) manualProxyEdit(accountID, proxyID int64, bulk bool) {
	w.accounts[accountID].ProxyID = autoSwitchInt64(proxyID)
	audit := FirstTokenTrendAuditWrite{CreatedAt: w.clock(), Action: "admin.accounts.update", ParamID: fmt.Sprint(accountID),
		RequestBody: fmt.Sprintf(`{"proxy_id":%d}`, proxyID)}
	if bulk {
		audit.Action, audit.ParamID = "admin.accounts.bulk_update.create", ""
		audit.RequestBody = fmt.Sprintf(`{"account_ids":[%d],"proxy_id":%d}`, accountID, proxyID)
	}
	w.audits = append(w.audits, audit)
}

// SlowSwitchAccountCounts mirrors the repository: distinct accounts per
// from-proxy among slow-verdict events since the given time.
func (w *pasWorld) SlowSwitchAccountCounts(_ context.Context, since time.Time) (map[int64]int, error) {
	seen := map[int64]map[int64]struct{}{}
	for _, e := range w.events {
		if e.CreatedAt.Before(since) || e.FromProxyID == nil || !pasContains([]string{ProxyAutoSwitchStatusMeasuring, ProxyAutoSwitchStatusDone, ProxyAutoSwitchStatusNoCandidate, ProxyAutoSwitchStatusFailed}, e.Status) {
			continue
		}
		if seen[*e.FromProxyID] == nil {
			seen[*e.FromProxyID] = map[int64]struct{}{}
		}
		seen[*e.FromProxyID][e.AccountID] = struct{}{}
	}
	out := map[int64]int{}
	for id, accounts := range seen {
		out[id] = len(accounts)
	}
	return out, nil
}

func (w *pasWorld) LatestEventPerAccount(_ context.Context, since time.Time, statuses []string) ([]ProxyAutoSwitchEvent, error) {
	latest := map[int64]ProxyAutoSwitchEvent{}
	for _, e := range w.events {
		if e.CreatedAt.Before(since) || !pasContains(statuses, e.Status) {
			continue
		}
		if cur, ok := latest[e.AccountID]; !ok || e.ID > cur.ID {
			latest[e.AccountID] = clonePASEvent(e)
		}
	}
	out := []ProxyAutoSwitchEvent{}
	for _, e := range latest {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}
