package orchestrator

import (
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// effectiveImage merges the overlay the plan wrote over the file it was planned
// from, through the load path `up` uses, and returns each service's image.
func effectiveImage(t *testing.T, src, overlay string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	base := writeFile(t, dir, "compose.yaml", src)
	ov := writeFile(t, dir, OverlayFileName, overlay)
	p, err := compose.LoadFiles([]string{base, ov}, nil)
	if err != nil {
		t.Fatalf("the overlay must load over the file it was planned from: %v\n%s", err, overlay)
	}
	out := map[string]string{}
	for n, s := range p.Services {
		out[n] = s.Image
	}
	return out
}

// Apple container refuses to pull from lscr.io, where docker compose does not.
// The same image is served from ghcr.io, so the overlay reads the name that way.
func TestAnLscrImageIsReadFromGhcrInTheOverlay(t *testing.T) {
	for _, tc := range []struct {
		name, image, want string // want "" = the overlay says nothing about it
	}{
		{"plain", "lscr.io/linuxserver/radarr", "ghcr.io/linuxserver/radarr"},
		{"tag", "lscr.io/linuxserver/radarr:latest", "ghcr.io/linuxserver/radarr:latest"},
		{"version tag", "lscr.io/linuxserver/radarr:5.1.3-ls200", "ghcr.io/linuxserver/radarr:5.1.3-ls200"},
		{"digest", "lscr.io/linuxserver/radarr@sha256:" + strings.Repeat("a", 64), "ghcr.io/linuxserver/radarr@sha256:" + strings.Repeat("a", 64)},
		{"tag and digest", "lscr.io/linuxserver/radarr:latest@sha256:" + strings.Repeat("b", 64), "ghcr.io/linuxserver/radarr:latest@sha256:" + strings.Repeat("b", 64)},
		{"upper-case host", "LSCR.IO/linuxserver/radarr", "ghcr.io/linuxserver/radarr"},
		{"nested name", "lscr.io/linuxserver/mods:code-server-python", "ghcr.io/linuxserver/mods:code-server-python"},
		// What it must not touch: another namespace on the same host is not a name
		// ghcr.io is known to serve; an image already on ghcr.io, docker hub or a
		// private registry is not refused; a name that only contains the host is not it.
		{"another namespace", "lscr.io/other/radarr", ""},
		{"bare host", "lscr.io/radarr", ""},
		{"already ghcr", "ghcr.io/linuxserver/radarr:latest", ""},
		{"docker hub", "linuxserver/radarr", ""},
		{"host in the middle", "registry.example/lscr.io/linuxserver/radarr", ""},
		{"namespace prefix only", "lscr.io/linuxserver-fork/radarr", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "name: demo\nservices:\n  app:\n    image: " + tc.image + "\n"
			body, changes := planFor(t, src)
			if tc.want == "" {
				if body != "" || len(changes) != 0 {
					t.Fatalf("%q is not a name the runtime refuses, but the plan wrote %v:\n%s", tc.image, changes, body)
				}
				return
			}
			if len(changes) != 1 || changes[0].Kind != "applied" || changes[0].Service != "app" || changes[0].Code != string(codeRegistryRefused) {
				t.Fatalf("want one applied entry for app with %s, got %+v", codeRegistryRefused, changes)
			}
			if got := effectiveImage(t, src, body)["app"]; got != tc.want {
				t.Errorf("after the overlay the image is %q, want %q\n%s", got, tc.want, body)
			}
		})
	}
}

// A service that builds names what the build produces in `image:`; reading that
// as an address to fetch from would rename the result of the build.
func TestABuiltServiceKeepsItsLscrImageName(t *testing.T) {
	body, changes := planFor(t, "name: demo\nservices:\n  app:\n    build: .\n    image: lscr.io/linuxserver/radarr:mine\n")
	if body != "" || len(changes) != 0 {
		t.Fatalf("a service with a build was renamed: %v\n%s", changes, body)
	}
}

// The name is decided from the image and nothing else, so the environment that
// could not be read (which stops every other decision) is no reason to leave it,
// and a second entry on the same service shares the service's block with it.
func TestTheLscrRenameSurvivesAnUnreadableEnvAndSharesTheServiceWithOtherEntries(t *testing.T) {
	t.Run("env_file missing", func(t *testing.T) {
		src := "name: demo\nservices:\n  app:\n    image: lscr.io/linuxserver/radarr\n    env_file: ./nope.env\n"
		body, changes := planFor(t, src)
		if len(changes) != 1 || changes[0].Kind != "applied" {
			t.Fatalf("want the rename alone, got %+v\n%s", changes, body)
		}
		if got := effectiveImage(t, src, body)["app"]; got != "ghcr.io/linuxserver/radarr" {
			t.Errorf("image after the overlay: %q", got)
		}
	})
	t.Run("with a note on the same service", func(t *testing.T) {
		src := "name: demo\nservices:\n  app:\n    image: lscr.io/linuxserver/radarr\n    restart: on-failure\n"
		body, changes := planFor(t, src)
		kinds := map[string]int{}
		for _, c := range changes {
			kinds[c.Kind]++
		}
		if kinds["applied"] != 1 || kinds["note"] != 1 {
			t.Fatalf("want one applied and one note, got %+v\n%s", changes, body)
		}
		if got := effectiveImage(t, src, body)["app"]; got != "ghcr.io/linuxserver/radarr" {
			t.Errorf("image after the overlay: %q\n%s", got, body)
		}
	})
}

// A `$` in an image reference would be read as interpolation when the overlay
// loads, so it is written doubled, as every other value the overlay writes is.
func TestTheRenamedImageIsWrittenWithItsDollarsDoubled(t *testing.T) {
	src := "name: demo\nservices:\n  app:\n    image: lscr.io/linuxserver/radarr:a$$b\n"
	body, _ := planFor(t, src)
	if !strings.Contains(body, "image: ghcr.io/linuxserver/radarr:a$$b") {
		t.Fatalf("the image line does not carry the doubled $:\n%s", body)
	}
	if got := effectiveImage(t, src, body)["app"]; got != "ghcr.io/linuxserver/radarr:a$b" {
		t.Errorf("after the overlay the image is %q", got)
	}
}

// Another service that builds the very name makes it a local tag: this one runs
// what that build produces, so the name is not an address to fetch from.
func TestAnLscrNameAnotherServiceBuildsIsNotRenamed(t *testing.T) {
	src := "name: demo\nservices:\n  a:\n    build: .\n    image: lscr.io/linuxserver/foo:mine\n  b:\n    image: lscr.io/linuxserver/foo:mine\n"
	if body, changes := planFor(t, src); body != "" || len(changes) != 0 {
		t.Fatalf("a name a sibling builds was renamed: %v\n%s", changes, body)
	}
	// A different tag of the same repository is a different name: still renamed.
	src = "name: demo\nservices:\n  a:\n    build: .\n    image: lscr.io/linuxserver/foo:mine\n  b:\n    image: lscr.io/linuxserver/foo:other\n"
	body, changes := planFor(t, src)
	if len(changes) != 1 || changes[0].Service != "b" {
		t.Fatalf("want b alone renamed, got %+v\n%s", changes, body)
	}
}

// A variable in the name is written out with the value it has now; the entry
// says so, because the overlay is not rewritten when the variable changes.
func TestTheRenameSaysAVariableInTheNameIsFixedAtItsCurrentValue(t *testing.T) {
	body, _ := planFor(t, "name: demo\nservices:\n  app:\n    image: lscr.io/linuxserver/radarr:${TAG:-5.1}\n")
	if !strings.Contains(body, "image: ghcr.io/linuxserver/radarr:5.1") || !strings.Contains(body, "a variable in it is written out with its current value") {
		t.Fatalf("the overlay does not say the tag is fixed at its current value:\n%s", body)
	}
}
