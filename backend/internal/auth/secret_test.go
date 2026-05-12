package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveJWTSecret_UsesConfiguredWhenStrong(t *testing.T) {
	tmp := t.TempDir()
	configured := "this-is-a-fairly-long-random-string-xyz"
	got, generated, err := ResolveJWTSecret(configured, filepath.Join(tmp, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if generated {
		t.Error("should not have generated when configured secret is strong")
	}
	if got != configured {
		t.Errorf("got %q want %q", got, configured)
	}
}

func TestResolveJWTSecret_RejectsKnownPlaceholders(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "secret.key")

	for _, placeholder := range []string{
		"",
		"change-me-in-prod-please",
		"please-change-me-to-a-long-random-string",
		"short",
	} {
		// On the second iteration onwards the file already exists, so `generated`
		// may legitimately be false. We don't assert on it — the contract under
		// test is just "the returned secret is never the placeholder we passed
		// in" and "it's at least 32 bytes".
		got, _, err := ResolveJWTSecret(placeholder, path)
		if err != nil {
			t.Fatalf("placeholder=%q: %v", placeholder, err)
		}
		if got == placeholder {
			t.Errorf("placeholder %q was returned unchanged", placeholder)
		}
		if len(got) < 32 {
			t.Errorf("generated secret too short: %d bytes", len(got))
		}
	}
}

func TestResolveJWTSecret_PersistsBetweenCalls(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "secret.key")

	first, gen1, err := ResolveJWTSecret("", path)
	if err != nil {
		t.Fatal(err)
	}
	if !gen1 {
		t.Fatal("first call should have generated")
	}

	second, gen2, err := ResolveJWTSecret("", path)
	if err != nil {
		t.Fatal(err)
	}
	if gen2 {
		t.Fatal("second call should have reused the file")
	}
	if first != second {
		t.Errorf("not stable across calls: %q vs %q", first, second)
	}

	// Verify the file is mode 0600
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("secret file mode = %o, want 0600", mode)
	}
}

func TestIsInsecureSecret(t *testing.T) {
	for _, s := range []string{
		"",
		"change-me-in-prod-please",
		"short",
	} {
		if !IsInsecureSecret(s) {
			t.Errorf("expected insecure: %q", s)
		}
	}
	if IsInsecureSecret("this-is-a-fairly-long-random-string") {
		t.Error("strong secret marked insecure")
	}
}
