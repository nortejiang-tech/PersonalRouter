package store

import "fmt"

// CaptureCallerKey stores an encrypted caller key only if the caller still
// has expectedHash and has no previously captured key. The comparison and
// update are one SQLite statement so concurrent first captures cannot replace
// one another.
func (s *Store) CaptureCallerKey(id, expectedHash, encrypted string) (bool, error) {
	if err := validateID(id); err != nil || expectedHash == "" || encrypted == "" {
		return false, fmt.Errorf("%w: caller key capture fields", ErrInvalid)
	}
	result, err := s.db.Exec(`
UPDATE callers SET key_cipher = ?
WHERE id = ? AND key_hash = ? AND key_cipher = ''`, encrypted, id, expectedHash)
	if err != nil {
		return false, fmt.Errorf("capture caller key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("capture caller key result: %w", err)
	}
	return affected == 1, nil
}
