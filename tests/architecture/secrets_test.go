package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// secretPatterns are shapes that only real credentials have. Placeholders and
// documentation use obviously fake values, which these do not match.
var secretPatterns = map[string]*regexp.Regexp{
	"private key":                regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`),
	"NexoStream integration key": regexp.MustCompile(`\bnxs_[0-9a-f]{32,}\b`),
	"JWT":                        regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{15,}\.eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{10,}`),
	"AWS access key id":          regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	"GitHub token":               regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	"Slack token":                regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	"Google API key":             regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	"URL with a password":        regexp.MustCompile(`\b(?:postgres|postgresql|redis|rediss|nats|amqp|mysql)://[^/\s:@]+:[^/\s:@${]{6,}@`),
}

// skipDirs hold generated or third-party files, or local development state.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "bin": true, "var": true, "vendor": true, ".vscode": true, ".idea": true,
}

// textExt limits the scan to files that can hold a credential.
var textExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".json": true, ".md": true, ".yml": true, ".yaml": true, ".env": true,
	".example": true, ".conf": true, ".sh": true, ".service": true, ".sql": true, ".toml": true, ".txt": true, ".ini": true, ".html": true,
	".pem": true, ".key": true, "": true,
}

// throwawayMarkers label a value as a public, development-only credential
// (the local compose stack and CI). TestDevStackIsLoopbackOnly keeps that
// honest: such a value is only acceptable where nothing outside the machine
// can reach it.
var throwawayMarkers = []string{"dev-only", "ci-only", "example", "changeme", "placeholder"}

func declaredThrowaway(match string) bool {
	m := strings.ToLower(match)
	for _, mark := range throwawayMarkers {
		if strings.Contains(m, mark) {
			return true
		}
	}
	return false
}

// No credential may be committed: secrets come from files named by *_FILE
// variables, never from the code or the repository (project rule).
func TestNoSecretsInTheRepository(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !textExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 2<<20 {
			return nil
		}
		// This file lists the patterns themselves.
		if filepath.Base(path) == "secrets_test.go" {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // walking the repository on purpose
		if err != nil {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		for name, re := range secretPatterns {
			for _, loc := range re.FindAllIndex(b, -1) {
				if declaredThrowaway(string(b[loc[0]:loc[1]])) {
					continue
				}
				line := 1 + strings.Count(string(b[:loc[0]]), "\n")
				t.Errorf("%s:%d looks like a committed %s. Move it to a *_FILE secret and rotate it.", filepath.ToSlash(rel), line, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("only %d files scanned: the walk is not reaching the repository", scanned)
	}
}

// The patterns must really detect what they claim to, or the test above
// proves nothing.
func TestSecretPatternsDetect(t *testing.T) {
	samples := map[string]string{
		"private key":                "-----BEGIN PRIVATE KEY-----\nMIIE",
		"NexoStream integration key": "nxs_0123456789abcdef0123456789abcdef",
		"JWT":                        "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r",
		"AWS access key id":          "AKIAIOSFODNN7EXAMPLE",
		"GitHub token":               "ghp_" + strings.Repeat("a", 36),
		"Slack token":                "xoxb-1234567890-abcdefghij",
		"Google API key":             "AIza" + strings.Repeat("A", 35),
		"URL with a password":        "postgres://nexo:S3cretPass@db.internal:5432/nexo",
	}
	for name, sample := range samples {
		if !secretPatterns[name].MatchString(sample) {
			t.Errorf("the %s pattern does not match its sample", name)
		}
	}
	// Placeholders in the documentation must not trip it.
	for _, ok := range []string{
		"postgres://user:${PASSWORD}@host/db", "postgres://127.0.0.1:5432/nexo", "redis://127.0.0.1:6379/0",
		"nxs_<token>", "Authorization: Bearer <access_token>", "postgres://nexostream@localhost/nexostream",
	} {
		for name, re := range secretPatterns {
			if re.MatchString(ok) {
				t.Errorf("%q wrongly matches the %s pattern", ok, name)
			}
		}
	}
}

// The development stack uses public passwords, so it must not be reachable
// from the network: every published port is bound to the loopback address.
func TestDevStackIsLoopbackOnly(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "docker-compose.dev.yml"))
	if err != nil {
		t.Skip("no dev compose file")
	}
	published := regexp.MustCompile(`ports:\s*\[([^\]]*)\]`)
	found := 0
	for _, m := range published.FindAllStringSubmatch(string(b), -1) {
		for _, p := range strings.Split(m[1], ",") {
			p = strings.Trim(strings.TrimSpace(p), `"'`)
			if p == "" {
				continue
			}
			found++
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("docker-compose.dev.yml publishes %q on every interface; bind it to 127.0.0.1 (its passwords are public)", p)
			}
		}
	}
	if found == 0 {
		t.Fatal("no published ports found: the check is not reading the file")
	}
}
