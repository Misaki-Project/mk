package password

import (
	"strings"

	"github.com/alexedwards/argon2id"
	"golang.org/x/crypto/bcrypt"
)

const (
	maxArgon2MemoryKiB   = 256 * 1024
	maxArgon2TotalWork   = 256 * 1024 // memory(KiB) * iterations のwork上限
	maxArgon2Parallelism = 16
	maxArgon2SaltBytes   = 64
	maxArgon2KeyBytes    = 64
)

// Verify reports whether plainText matches encodedHash without rewriting it.
func Verify(plainText, encodedHash string) bool {
	switch {
	case strings.HasPrefix(encodedHash, "$argon2id$"):
		params, _, _, err := argon2id.DecodeHash(encodedHash)
		if err != nil ||
			params.Memory == 0 || params.Memory > maxArgon2MemoryKiB ||
			params.Iterations == 0 ||
			uint64(params.Memory)*uint64(params.Iterations) > maxArgon2TotalWork ||
			params.Parallelism == 0 || params.Parallelism > maxArgon2Parallelism ||
			params.SaltLength == 0 || params.SaltLength > maxArgon2SaltBytes ||
			params.KeyLength == 0 || params.KeyLength > maxArgon2KeyBytes {
			return false
		}
		matched, err := argon2id.ComparePasswordAndHash(plainText, encodedHash)
		return err == nil && matched
	case strings.HasPrefix(encodedHash, "$2a$"), strings.HasPrefix(encodedHash, "$2b$"), strings.HasPrefix(encodedHash, "$2y$"):
		return bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(plainText)) == nil
	default:
		return false
	}
}
