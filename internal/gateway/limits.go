package gateway

import (
	"errors"
	"sync"
	"time"

	"personalrouter/internal/store"
)

var (
	errRateLimited = errors.New("rate limited")
	errConcurrency = errors.New("concurrency limited")
	errBudget      = errors.New("daily budget exceeded")
)

// admission keeps the process-local part of the caller policy. The store is
// deliberately not used for the hot admission counter: admission must be one
// atomic operation even when several requests arrive at the same time.
type admission struct {
	mu       sync.Mutex
	callers  map[string]*callerAdmission
	global   int
	inflight int
}

type callerAdmission struct {
	inflight int
	requests []time.Time
}

func newAdmission(global int) *admission {
	return &admission{callers: make(map[string]*callerAdmission), global: global}
}

func (a *admission) acquire(c store.Caller, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	state := a.callers[c.ID]
	if state == nil {
		state = &callerAdmission{}
		a.callers[c.ID] = state
	}
	cutoff := now.Add(-time.Minute)
	first := 0
	for first < len(state.requests) && state.requests[first].After(cutoff) == false {
		first++
	}
	if first > 0 {
		state.requests = append([]time.Time(nil), state.requests[first:]...)
	}

	// A zero caller concurrency means the safe gateway default. RPM zero is
	// intentionally unlimited, as documented by the implementation contract.
	maxConcurrency := c.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 2
	}
	if state.inflight >= maxConcurrency {
		return errConcurrency
	}
	if a.global > 0 && a.inflight >= a.global {
		return errConcurrency
	}
	if c.RPM > 0 && len(state.requests) >= c.RPM {
		return errRateLimited
	}
	state.inflight++
	a.inflight++
	state.requests = append(state.requests, now)
	return nil
}

func (a *admission) release(callerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state := a.callers[callerID]; state != nil && state.inflight > 0 {
		state.inflight--
	}
	if a.inflight > 0 {
		a.inflight--
	}
}

// budgetAvailable implements the fail-closed rule for budgeted callers. The
// date boundary is explicitly Asia/Shanghai to match the operator's daily
// accounting day. An old unknown-usage request does not affect today's
// budget; an unknown request from today does, because its token cost cannot be
// safely bounded. Concurrent in-flight requests can exceed the threshold after
// this check; that bounded overrun is inherent in admission-before-accounting
// and is recorded in the ledger rather than hidden.
func budgetAvailable(s *store.Store, c store.Caller, now time.Time) (bool, error) {
	if c.DailyTokenLimit <= 0 {
		return true, nil
	}
	if s == nil {
		return false, errors.New("store unavailable")
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return false, err
	}
	localNow := now.In(location)
	year, month, day := localNow.Date()
	since := time.Date(year, month, day, 0, 0, 0, 0, location)
	total, unknown, err := s.CallerUsageSince(c.ID, since)
	if err != nil {
		return false, err
	}
	if unknown || total >= c.DailyTokenLimit {
		return false, errBudget
	}
	return true, nil
}
