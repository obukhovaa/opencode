package task

import (
	"crypto/rand"
	"encoding/base32"
)

// NewTaskID generates a task ID of the form `<kind-prefix>_<base32(16-byte-random)>`.
// The random component is 16 cryptographically random bytes encoded as
// unpadded uppercase base32 (26 characters). The result matches the
// background-tasks spec regex `^(shell|agent|monitor|cron)_[A-Z2-7]{26}$`.
//
// Collisions are astronomically unlikely (2^128 random space) but the
// registry-side Register call still defensively rejects a duplicate.
func NewTaskID(kind Kind) string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:])
	return kind.IDPrefix() + "_" + enc
}
