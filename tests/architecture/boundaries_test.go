// Package architecture checks the dependency rules between the parts of the
// system (ADR 0001: the Data Plane must keep running without the Control
// Plane, so it must not even link its code).
package architecture

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const module = "nexostream/"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// deps returns every package (of this module) that the given packages depend
// on, directly or not, including the packages themselves.
func deps(t *testing.T, patterns ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not found in PATH")
	}
	cmd := exec.Command("go", append([]string{"list", "-deps", "-f", "{{.ImportPath}}"}, patterns...)...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", patterns, err, out)
	}
	var res []string
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, module) {
			res = append(res, strings.TrimPrefix(p, module))
		}
	}
	return res
}

// forbid fails for every dependency of roots that starts with one of the
// forbidden prefixes.
func forbid(t *testing.T, what string, roots []string, forbidden ...string) {
	t.Helper()
	for _, d := range deps(t, roots...) {
		for _, f := range forbidden {
			if d == strings.TrimSuffix(f, "/") || strings.HasPrefix(d, f) {
				t.Errorf("%s must not depend on %s (found %s)", what, f, d)
			}
		}
	}
}

// The Media Node binary and its libraries must not link the Control Plane:
// that is what lets channels keep running when the API or controller is down.
func TestDataPlaneDoesNotDependOnControlPlane(t *testing.T) {
	forbid(t, "the worker binary", []string{"./cmd/nexostream-worker"}, "control-plane/")
	forbid(t, "data-plane/agent", []string{"./data-plane/..."}, "control-plane/")
	forbid(t, "media-engine", []string{"./media-engine/..."}, "control-plane/", "data-plane/")
}

// The Media Node must not carry database code either.
func TestWorkerBinaryHasNoDatabaseDriver(t *testing.T) {
	for _, d := range deps(t, "./cmd/nexostream-worker") {
		if strings.Contains(d, "postgres") {
			t.Errorf("worker binary links %s", d)
		}
	}
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/nexostream-worker")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/jackc/pgx") || strings.HasPrefix(p, "github.com/redis/") {
			t.Errorf("worker binary links %s", p)
		}
	}
}

// The Control Plane drives workers over the bus only; it never imports the
// agent. The shared vocabulary lives in internal/.
func TestControlPlaneDoesNotDependOnTheAgent(t *testing.T) {
	forbid(t, "control-plane", []string{"./control-plane/..."}, "data-plane/")
}

// internal/ is the shared vocabulary: it must stay below both planes.
func TestInternalStaysBelowThePlanes(t *testing.T) {
	// internal/runtime is only used by the binaries and is checked too: it is
	// what used to drag the database into the Media Node.
	forbid(t, "internal/", []string{"./internal/..."}, "control-plane/", "data-plane/", "media-engine/")
}
