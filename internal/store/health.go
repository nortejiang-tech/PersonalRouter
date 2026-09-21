package store

import (
	"context"
	"errors"
	"time"
)

// ErrHealthUnavailable is intentionally fixed so database paths, DSNs, and
// driver details never cross the runtime health boundary.
var ErrHealthUnavailable = errors.New("store health unavailable")

// CheckHealth performs a bounded read against the existing callers table.
// An empty table is healthy; the query is deliberately not a cached Ping.
func (s *Store) CheckHealth(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrHealthUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var count int64
	if err := s.db.QueryRowContext(checkCtx, `SELECT COUNT(*) FROM callers`).Scan(&count); err != nil {
		return ErrHealthUnavailable
	}
	return nil
}
