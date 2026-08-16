package entitycompat

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPasswordVerifierGate is the static contract gate for password handling
// (Task 3). It enforces two invariants:
//
//  1. Every stored-password check in production API code routes through
//     internal/auth/password.Verify. A direct bcrypt.CompareHashAndPassword
//     call rejects migrated CherryPick accounts whose profile password is an
//     Argon2id hash, so it must not appear anywhere under internal/api.
//  2. Production password writes stay bcrypt: argon2id.CreateHash is
//     verification-only migration compatibility and must not be called from
//     any production file (the sole allowed reference is
//     internal/auth/password/verify.go, which only decodes/comparces hashes).
func TestPasswordVerifierGate(t *testing.T) {
	root := repoRoot(t)

	var directCompare, argonGen []string

	err := filepath.WalkDir(filepath.Join(root, "internal/api"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "bcrypt.CompareHashAndPassword") {
			directCompare = append(directCompare, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/api: %v", err)
	}

	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "internal/auth/password/verify.go")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "argon2id.CreateHash") {
			argonGen = append(argonGen, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}

	if len(directCompare) == 0 && len(argonGen) == 0 {
		return
	}

	var b strings.Builder
	b.WriteString("stored-password checks must call internal/auth/password.Verify; direct bcrypt comparison rejects migrated Argon2id accounts\n")
	b.WriteString("production password writes must remain bcrypt; Argon2id is verification-only migration compatibility\n")
	for _, p := range directCompare {
		b.WriteString("  direct bcrypt comparison: " + p + "\n")
	}
	for _, p := range argonGen {
		b.WriteString("  production Argon2id generation: " + p + "\n")
	}
	t.Fatal(b.String())
}
