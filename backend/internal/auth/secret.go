package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// knownInsecureSecrets is a deny-list of obvious placeholder secrets that we
// refuse to accept silently. These shipped in earlier README snippets and
// example configs; if anyone copies them verbatim into production we want to
// loudly upgrade them to a real random key.
var knownInsecureSecrets = map[string]struct{}{
	"change-me-in-prod-please":                 {},
	"please-change-me-to-a-long-random-string": {},
	"":     {},
	"test": {},
}

// ResolveJWTSecret returns a usable, persistent JWT signing secret.
//
// Resolution order:
//  1. If `configured` is non-empty AND not in the well-known placeholder
//     deny-list AND >= 16 chars, use it as-is.
//  2. Otherwise try reading `secretFilePath`. If present and non-empty, use it.
//  3. Otherwise generate 32 cryptographically-random bytes (hex-encoded),
//     persist them to `secretFilePath` with mode 0600, and return.
//
// The returned `generated` flag is true when we had to fall back to step 3, so
// the caller can log a one-time hint like "auto-generated; back up data/secret.key".
func ResolveJWTSecret(configured, secretFilePath string) (secret string, generated bool, err error) {
	trimmed := strings.TrimSpace(configured)
	if _, bad := knownInsecureSecrets[trimmed]; !bad && len(trimmed) >= 16 {
		return trimmed, false, nil
	}

	if secretFilePath != "" {
		buf, readErr := os.ReadFile(secretFilePath)
		if readErr == nil {
			existing := strings.TrimSpace(string(buf))
			if len(existing) >= 16 {
				return existing, false, nil
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return "", false, fmt.Errorf("read jwt secret file: %w", readErr)
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", false, fmt.Errorf("generate jwt secret: %w", err)
	}
	generatedSecret := hex.EncodeToString(raw)

	if secretFilePath != "" {
		if dir := filepath.Dir(secretFilePath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", false, fmt.Errorf("create secret dir: %w", err)
			}
		}
		if err := os.WriteFile(secretFilePath, []byte(generatedSecret), 0o600); err != nil {
			return "", false, fmt.Errorf("write jwt secret: %w", err)
		}
	}
	return generatedSecret, true, nil
}

// IsInsecureSecret reports whether the given secret is a well-known placeholder
// or too short to be safe. Useful for emitting a warning at startup.
func IsInsecureSecret(s string) bool {
	t := strings.TrimSpace(s)
	if _, bad := knownInsecureSecrets[t]; bad {
		return true
	}
	return len(t) < 16
}
