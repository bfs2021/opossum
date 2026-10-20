package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// psImageOf runs `ps` for a one-service project whose file names `written`,
// against a container the runtime says it made from `actual` ("" = the
// runtime gives no image), and returns what the IMAGE column says.
func psImageOf(t *testing.T, written, actual string) string {
	t.Helper()
	rt, _ := fakeShim(t)
	strictContainerWith(t, rt, "db.demo.opossum")
	if actual != "" {
		setShimEnv(rt, "INSPECT_IMAGE=db.demo.opossum="+actual)
	} else {
		setShimEnv(rt, "INSPECT_IMAGE_NONE=1")
	}
	p := project("demo", map[string]*compose.Service{"db": {Image: written}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Ps(orchestrator.PsOptions{Format: "json"}); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	var rows []orchestrator.ServiceStatus
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("want one row, got %q (%v)", out.String(), err)
	}
	return rows[0].Image
}

// `up` under TAGX=3.20 and `ps` from a shell without it are two readings of
// one file; the column says what is running, as docker compose's does.
func TestPsImageIsTheOneTheContainerRuns(t *testing.T) {
	cases := []struct{ name, written, actual, want string }{
		{"another tag of a Hub library image", "alpine:3.19", "docker.io/library/alpine:3.20", "alpine:3.20"},
		{"another Hub repository", "postgres:16", "docker.io/library/mariadb:11", "mariadb:11"},
		{"a Hub image of an organisation", "org/app:1", "docker.io/org/app:2", "org/app:2"},
		{"another registry keeps its host", "ghcr.io/o/app:1", "ghcr.io/o/app:2", "ghcr.io/o/app:2"},
		{"another registry, library path is not Hub's", "ghcr.io/o/app:1", "ghcr.io/library/app:1", "ghcr.io/library/app:1"},
		{"the file names no tag, the container runs another", "alpine", "docker.io/library/alpine:3.20", "alpine:3.20"},
		{"another digest", "alpine@sha256:abc", "docker.io/library/alpine@sha256:def", "alpine@sha256:def"},
		{"same digest, another repository", "alpine@sha256:abc", "docker.io/library/mirror@sha256:abc", "mirror@sha256:abc"},
		{"a tag in the file, a digest running", "alpine:3.20", "docker.io/library/alpine@sha256:abc", "alpine@sha256:abc"},
		{"Hub host in the file, another tag running", "docker.io/alpine:3.19", "docker.io/library/alpine:3.20", "alpine:3.20"},
		{"a path under library/ is not a library image", "app:1", "docker.io/library/foo/bar:2", "library/foo/bar:2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := psImageOf(t, c.written, c.actual); got != c.want {
				t.Errorf("IMAGE = %q, want %q", got, c.want)
			}
		})
	}
}

// The same image spelled another way is not a change: what the file wrote
// stays, rather than the runtime's fully written out form.
func TestPsImageKeepsTheFilesSpellingOfTheSameImage(t *testing.T) {
	cases := []struct{ name, written, actual string }{
		{"no tag is latest", "alpine", "docker.io/library/alpine:latest"},
		{"library path spelled out", "library/alpine:3.20", "docker.io/library/alpine:3.20"},
		{"Hub host spelled out", "docker.io/org/app:2", "docker.io/org/app:2"},
		{"Hub host and library spelled out", "docker.io/library/alpine:3.20", "docker.io/library/alpine:3.20"},
		{"organisation image without host", "org/app", "docker.io/org/app:latest"},
		{"another registry", "ghcr.io/o/app", "ghcr.io/o/app:latest"},
		{"a registry with a port", "localhost:5000/app:1", "localhost:5000/app:1"},
		{"a registry with a port, no tag", "localhost:5000/app", "localhost:5000/app:latest"},
		// The runtime keeps a digest reference without its tag (measured on
		// container 1.5.0, testdata/real-cli-output.md), so that is the form it
		// answers with, whichever way the file wrote it.
		{"a digest", "alpine@sha256:abc", "docker.io/library/alpine@sha256:abc"},
		{"a tag and a digest", "alpine:3.20@sha256:abc", "docker.io/library/alpine@sha256:abc"},
		{"a tag and a digest, Hub host spelled out", "docker.io/library/alpine:3.20@sha256:abc", "docker.io/library/alpine@sha256:abc"},
		{"Hub host without library", "docker.io/alpine:3.20", "docker.io/library/alpine:3.20"},
		{"Hub host without library, no tag", "docker.io/alpine", "docker.io/library/alpine:latest"},
		{"Hub host without library, a tag and a digest", "docker.io/alpine:3.20@sha256:abc", "docker.io/library/alpine@sha256:abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := psImageOf(t, c.written, c.actual); got != c.written {
				t.Errorf("IMAGE = %q, want what the file wrote, %q", got, c.written)
			}
		})
	}
}

// A runtime that gives no image for the container leaves what the file says,
// as before.
func TestPsImageIsTheFilesWhenTheRuntimeGivesNone(t *testing.T) {
	if got := psImageOf(t, "alpine:3.19", ""); got != "alpine:3.19" {
		t.Errorf("IMAGE = %q, want alpine:3.19", got)
	}
}

// canonicalImageRef itself, spelling by spelling: what it makes of a reference is
// what two references are compared by, and an answer the column does not show
// (an image written one way, run as another that happens to print the same) is
// only seen here.
func TestCanonicalImageRef(t *testing.T) {
	for _, c := range []struct{ ref, want string }{
		{"alpine", "docker.io/library/alpine:latest"},
		{"alpine:3.20", "docker.io/library/alpine:3.20"},
		{"library/alpine", "docker.io/library/alpine:latest"},
		{"docker.io/alpine", "docker.io/library/alpine:latest"},
		{"docker.io/library/alpine:3.20", "docker.io/library/alpine:3.20"},
		{"org/app", "docker.io/org/app:latest"},
		{"docker.io/org/app:2", "docker.io/org/app:2"},
		{"docker.io/library/foo/bar", "docker.io/library/foo/bar:latest"},
		{"ghcr.io/o/app", "ghcr.io/o/app:latest"},
		{"ghcr.io/o/app:1", "ghcr.io/o/app:1"},
		{"localhost/app", "localhost/app:latest"},
		{"localhost:5000/app", "localhost:5000/app:latest"},
		{"localhost:5000/app:1", "localhost:5000/app:1"},
		{"alpine@sha256:abc", "docker.io/library/alpine@sha256:abc"},
		{"alpine:3.20@sha256:abc", "docker.io/library/alpine@sha256:abc"},
		{"docker.io/alpine:3.20@sha256:abc", "docker.io/library/alpine@sha256:abc"},
		{"org/app@sha256:abc", "docker.io/org/app@sha256:abc"},
		{"localhost:5000/app:1@sha256:abc", "localhost:5000/app@sha256:abc"},
	} {
		if got := orchestrator.CanonicalImageRefForTest(c.ref); got != c.want {
			t.Errorf("canonicalImageRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

// The whole path with nothing staged: `up` under one reading of the file runs the
// container, the fake keeps the image it was run from, and a `ps` under another
// reading (the one in #2118) reports that image.
func TestPsImageFollowsTheImageAnUpRan(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	up := project("demo", map[string]*compose.Service{"db": {Image: "alpine:3.20"}})
	if err := orchestrator.New(up, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	ps := project("demo", map[string]*compose.Service{"db": {Image: "alpine:3.19"}})
	var out bytes.Buffer
	if err := orchestrator.New(ps, rt, "opossum", &out).Ps(orchestrator.PsOptions{Format: "json"}); err != nil {
		t.Fatalf("ps: %v", err)
	}
	var rows []orchestrator.ServiceStatus
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("want one row, got %q (%v)", out.String(), err)
	}
	if rows[0].Image != "alpine:3.20" {
		t.Errorf("IMAGE = %q, want alpine:3.20, the image the up ran", rows[0].Image)
	}
}
