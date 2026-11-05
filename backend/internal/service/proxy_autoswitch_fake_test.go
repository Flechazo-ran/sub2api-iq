package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// pasWorld is an in-memory stand-in for the database, proxies, admin edit path
// and probes used by ProxyAutoSwitchService tests.
type pasWorld struct {
	mu        sync.Mutex
	now       time.Time
	accounts  map[int64]*ProxyAutoSwitchAccount
	groupsOf  map[int64][]int64
	proxies   map[int64]*Proxy
	external  map[int64]int64 // proxy id -> accounts outside the test groups using it
	statsFn   func(metric string, accountIDs []int64, from, to time.Time) []ProxyAutoSwitchLatencyStat
	statsLog  []string // metric of every LatencyStats call
	events    []ProxyAutoSwitchEvent
	nextEvent int64
	states    map[int64]ProxyAutoSwitchAccountState
	audit     []ProxyAutoSwitchAuditWrite
	updates   [][2]int64
	updateErr error
	probeMS   map[int64][]any // proxy id -> sequence of int64 ms or error, last value repeats
	probeLog  []int64
	settings  map[string]string
	groups    map[int64]*Group
	latency   map[int64]*ProxyLatencyInfo
	exitCode  map[int64]string
	exitCalls []int64
}

func newPASWorld() *pasWorld {
	return &pasWorld{
		now:      time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		accounts: map[int64]*ProxyAutoSwitchAccount{},
		groupsOf: map[int64][]int64{},
		proxies:  map[int64]*Proxy{},
		external: map[int64]int64{},
		states:   map[int64]ProxyAutoSwitchAccountState{},
		probeMS:  map[int64][]any{},
		settings: map[string]string{},
		groups: map[int64]*Group{
			16: {ID: 16, Name: "GPT-速刷-AUTO", Platform: PlatformOpenAI},
			9:  {ID: 9, Name: "Claude", Platform: PlatformAnthropic},
		},
		latency:  map[int64]*ProxyLatencyInfo{},
		exitCode: map[int64]string{},
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
	// Created long ago by default, so proxy changes in tests count as manual
	// unless a test makes the account new.
	a := &ProxyAutoSwitchAccount{ID: id, Name: fmt.Sprintf("acc-%d", id), Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, CreatedAt: w.now.Add(-30 * 24 * time.Hour)}
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

// uniform makes every account have the given samples/p50 on whatever proxy it uses now.
func (w *pasWorld) uniform(samples int, p50 int64) {
	w.statsFn = func(_ string, ids []int64, _, _ time.Time) []ProxyAutoSwitchLatencyStat {
		out := []ProxyAutoSwitchLatencyStat{}
		for _, id := range ids {
			if a := w.accounts[id]; a != nil && a.ProxyID != nil {
				out = append(out, ProxyAutoSwitchLatencyStat{AccountID: id, ProxyID: *a.ProxyID, Samples: samples, P50MS: p50})
			}
		}
		return out
	}
}

func (w *pasWorld) usedCount(proxyID int64) int64 {
	n := w.external[proxyID]
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

func (w *pasWorld) service() *ProxyAutoSwitchService {
	svc := NewProxyAutoSwitchService(w, w, pasGroups{w}, pasProxies{w}, w, w, w, w, nil, nil)
	svc.now = w.clock
	return svc
}

func (w *pasWorld) saveConfig(cfg ProxyAutoSwitchConfig) {
	raw, err := jsonMarshalForTest(cfg)
	if err != nil {
		panic(err)
	}
	w.settings[proxyAutoSwitchConfigKey] = raw
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
	return nil, errors.New("proxy not found")
}

func (p pasProxies) ListActiveWithAccountCount(_ context.Context) ([]ProxyWithAccountCount, error) {
	out := []ProxyWithAccountCount{}
	for _, proxy := range p.w.proxies {
		if proxy.Status != StatusActive {
			continue
		}
		out = append(out, ProxyWithAccountCount{Proxy: *proxy, AccountCount: p.w.usedCount(proxy.ID)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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

func (w *pasWorld) ProbeProxy(_ context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.proxyIDByURL(proxyURL)
	w.exitCalls = append(w.exitCalls, id)
	if code := w.exitCode[id]; code != "" {
		return &ProxyExitInfo{CountryCode: code}, 100, nil
	}
	return nil, 0, errors.New("exit probe failed")
}

func (w *pasWorld) ProbeChatGPT(_ context.Context, proxyURL string) (ProxyAutoSwitchProbeResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.proxyIDByURL(proxyURL)
	w.probeLog = append(w.probeLog, id)
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

func (w *pasWorld) probedProxies() map[int64]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[int64]int{}
	for _, id := range w.probeLog {
		out[id]++
	}
	return out
}

// --- repository ---

func (w *pasWorld) ListGroupAccounts(_ context.Context, groupIDs []int64) ([]ProxyAutoSwitchAccount, error) {
	want := map[int64]bool{}
	for _, id := range groupIDs {
		want[id] = true
	}
	out := []ProxyAutoSwitchAccount{}
	for id, a := range w.accounts {
		if a.Deleted || a.ParentAccountID != nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth {
			continue
		}
		for _, g := range w.groupsOf[id] {
			if want[g] {
				out = append(out, *a)
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

func (w *pasWorld) LatencyStats(_ context.Context, metric string, ids []int64, from, to time.Time) ([]ProxyAutoSwitchLatencyStat, error) {
	if !proxyAutoSwitchValidMetric(metric) {
		return nil, fmt.Errorf("unknown metric %q", metric)
	}
	w.statsLog = append(w.statsLog, metric)
	if w.statsFn == nil {
		return nil, nil
	}
	return w.statsFn(metric, ids, from, to), nil
}

func (w *pasWorld) InsertEvent(_ context.Context, e *ProxyAutoSwitchEvent) error {
	w.nextEvent++
	e.ID = w.nextEvent
	cp := *e
	cp.Candidates = append([]ProxyAutoSwitchCandidate(nil), e.Candidates...)
	w.events = append(w.events, cp)
	return nil
}

func (w *pasWorld) UpdateEventIfStatus(_ context.Context, e *ProxyAutoSwitchEvent, expected string) (bool, error) {
	cur := w.event(e.ID)
	if cur == nil || cur.Status != expected {
		return false, nil
	}
	cur.UpdatedAt, cur.Status, cur.Reason = e.UpdatedAt, e.Status, e.Reason
	cur.ToProxyID, cur.ToProxyLabel, cur.ToRegion = e.ToProxyID, e.ToProxyLabel, e.ToRegion
	cur.CandidateProbeMS, cur.AfterP50MS, cur.AfterSamples = e.CandidateProbeMS, e.AfterP50MS, e.AfterSamples
	cur.Candidates, cur.FinishedAt = e.Candidates, e.FinishedAt
	return true, nil
}

func (w *pasWorld) GetEvent(_ context.Context, id int64) (*ProxyAutoSwitchEvent, error) {
	if e := w.event(id); e != nil {
		cp := *e
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
		out = append(out, e)
		if len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

func (w *pasWorld) ListObserving(_ context.Context) ([]ProxyAutoSwitchEvent, error) {
	out := []ProxyAutoSwitchEvent{}
	for _, e := range w.events {
		if e.Status == ProxyAutoSwitchStatusObserving {
			out = append(out, e)
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

func (w *pasWorld) AccountHistory(_ context.Context, ids []int64, since time.Time, switched, misses []string) (map[int64]ProxyAutoSwitchHistory, error) {
	out := map[int64]ProxyAutoSwitchHistory{}
	for _, id := range ids {
		var h ProxyAutoSwitchHistory
		for _, e := range w.events {
			if e.AccountID != id || e.CreatedAt.Before(since) {
				continue
			}
			at := e.CreatedAt
			if pasContains(switched, e.Status) && (h.LastSwitchAt == nil || at.After(*h.LastSwitchAt)) {
				h.LastSwitchAt = &at
			}
			if pasContains(misses, e.Status) && (h.LastMissAt == nil || at.After(*h.LastMissAt)) {
				h.LastMissAt = &at
			}
		}
		if h.LastSwitchAt != nil || h.LastMissAt != nil {
			out[id] = h
		}
	}
	return out, nil
}

func (w *pasWorld) CountEventsSince(_ context.Context, since time.Time, statuses []string) (int, error) {
	n := 0
	for _, e := range w.events {
		if !e.CreatedAt.Before(since) && pasContains(statuses, e.Status) {
			n++
		}
	}
	return n, nil
}

func (w *pasWorld) LatestEventPerAccount(_ context.Context, since time.Time, statuses []string) ([]ProxyAutoSwitchEvent, error) {
	latest := map[int64]ProxyAutoSwitchEvent{}
	for _, e := range w.events {
		if e.CreatedAt.Before(since) || !pasContains(statuses, e.Status) {
			continue
		}
		if cur, ok := latest[e.AccountID]; !ok || e.ID > cur.ID {
			latest[e.AccountID] = e
		}
	}
	out := []ProxyAutoSwitchEvent{}
	for _, e := range latest {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (w *pasWorld) LoadAccountStates(_ context.Context, ids []int64) (map[int64]ProxyAutoSwitchAccountState, error) {
	out := map[int64]ProxyAutoSwitchAccountState{}
	for _, id := range ids {
		if st, ok := w.states[id]; ok {
			out[id] = st
		}
	}
	return out, nil
}

// UpsertAccountState mirrors the SQL: keep the stored change unless a newer one
// is given; with no new one, drop a stored change at or before ClearExternalUpTo.
func (w *pasWorld) UpsertAccountState(_ context.Context, st ProxyAutoSwitchAccountState) error {
	if prev, ok := w.states[st.AccountID]; ok && prev.ExternalChangeAt != nil {
		cleared := st.ExternalChangeAt == nil && st.ClearExternalUpTo != nil && !prev.ExternalChangeAt.After(*st.ClearExternalUpTo)
		if !cleared && (st.ExternalChangeAt == nil || prev.ExternalChangeAt.After(*st.ExternalChangeAt)) {
			st.ExternalChangeAt = prev.ExternalChangeAt
		}
	}
	st.ClearExternalUpTo = nil // not a stored column
	w.states[st.AccountID] = st
	return nil
}

func (w *pasWorld) ListAuditProxyWrites(_ context.Context, since time.Time) ([]ProxyAutoSwitchAuditWrite, error) {
	out := []ProxyAutoSwitchAuditWrite{}
	for _, a := range w.audit {
		if !a.CreatedAt.Before(since) {
			out = append(out, a)
		}
	}
	return out, nil
}
