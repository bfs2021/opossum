package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Planning the overlay reads an image's builds, and an image that is not here is
// pulled for it. That is a download, so it is done only for a plan that is acted
// on: not under --dry-run, and not where an overlay is already in place. (The unit
// tests set the runtime's dry-run by hand, which is the order the command does
// not run in: the plan comes before the run's own dry-run is set.)
func TestPlanningTheOverlayPullsOnlyWhereThePlanIsActedOn(t *testing.T) {
	pulls := func(calls []string) int {
		n := 0
		for _, c := range calls {
			if strings.HasPrefix(c, "image pull") && strings.HasSuffix(c, " vendor/app:1") {
				n++
			}
		}
		return n
	}
	project := func(t *testing.T, overlay bool) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("name: p\nservices:\n  app:\n    image: vendor/app:1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if overlay {
			// the overlay's header is what marks it as opossum's own
			if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yaml"), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Chdir(dir)
	}

	t.Run("a real run pulls to read the image", func(t *testing.T) {
		calls := fakeShim(t)
		project(t, false)
		if _, err := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor"); err != nil {
			t.Fatalf("up: %v", err)
		}
		if pulls(calls()) == 0 {
			t.Errorf("the control did not pull, so the other two prove nothing; calls:\n%s", strings.Join(calls(), "\n"))
		}
	})
	t.Run("--dry-run pulls nothing", func(t *testing.T) {
		calls := fakeShim(t)
		project(t, false)
		if _, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run"); err != nil {
			t.Fatalf("up --dry-run: %v", err)
		}
		if n := pulls(calls()); n != 0 {
			t.Errorf("a dry run pulled %d times:\n%s", n, strings.Join(calls(), "\n"))
		}
	})
	t.Run("-f names the file: the overlay is only shown, and nothing is pulled for it", func(t *testing.T) {
		calls := fakeShim(t)
		project(t, false)
		if _, err := run(t, "-f", "compose.yaml", "up", "--from-docker-compose", "--no-build", "--no-supervisor"); err != nil {
			t.Fatalf("up -f: %v", err)
		}
		if n := pulls(calls()); n != 0 {
			t.Errorf("planned with %d pulls though the overlay is only shown:\n%s", n, strings.Join(calls(), "\n"))
		}
	})
	t.Run("an overlay already in place is not planned with pulls", func(t *testing.T) {
		calls := fakeShim(t)
		project(t, true)
		if _, err := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor"); err != nil {
			t.Fatalf("up: %v", err)
		}
		if n := pulls(calls()); n != 0 {
			t.Errorf("planned with %d pulls though nothing will be written:\n%s", n, strings.Join(calls(), "\n"))
		}
	})
}
