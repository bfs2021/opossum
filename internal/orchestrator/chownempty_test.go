package orchestrator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// A service that died chowning its bind mount gets the named-volume swap applied
// when the host directory holds nothing — there is no data to leave behind — and
// keeps it as a suggestion whenever the directory holds anything, is shared, or
// cannot be judged. Each row is one reason to ask rather than act.
func TestChownSwapIsAppliedOnlyWhenTheHostDirectoryHoldsNothing(t *testing.T) {
	const composeYAML = `
name: demo
services:
  blog:
    image: ghost:6-alpine
    volumes:
      - %s:/var/lib/ghost/content
%s`
	const peer = `  backup:
    image: alpine
    volumes:
      - ./content:/backup-src
`
	for _, tc := range []struct {
		name  string
		src   string
		setup func(t *testing.T, dir string)
		peer  bool
		want  string // "applied" or "suggestion"
	}{
		{"empty directory", "./content", func(t *testing.T, d string) { mk(t, d, "content") }, false, "applied"},
		{"directory does not exist", "./content", func(t *testing.T, d string) {}, false, "applied"},
		{"a file inside", "./content", func(t *testing.T, d string) { holdData(t, d, "content") }, false, "suggestion"},
		{"only a hidden file inside", "./content", func(t *testing.T, d string) {
			mk(t, d, "content")
			if err := os.WriteFile(filepath.Join(d, "content", ".keep"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, false, "suggestion"},
		{"only an empty subdirectory inside", "./content", func(t *testing.T, d string) { mk(t, d, "content/sub") }, false, "suggestion"},
		{"the source is a file", "./content", func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "content"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, false, "suggestion"},
		{"empty but another service reads it", "./content", func(t *testing.T, d string) { mk(t, d, "content") }, true, "suggestion"},
		{"absolute source, empty", "$DIR/abs", func(t *testing.T, d string) { mk(t, d, "abs") }, false, "applied"},
		{"absolute source with a file inside", "$DIR/abs", func(t *testing.T, d string) { holdData(t, d, "abs") }, false, "suggestion"},
		{"dangling symlink (data may be elsewhere)", "./content", func(t *testing.T, d string) {
			if err := os.Symlink(filepath.Join(d, "gone"), filepath.Join(d, "content")); err != nil {
				t.Fatal(err)
			}
		}, false, "suggestion"},
		{"symlink to an empty directory", "./content", func(t *testing.T, d string) {
			mk(t, d, "real")
			if err := os.Symlink(filepath.Join(d, "real"), filepath.Join(d, "content")); err != nil {
				t.Fatal(err)
			}
		}, false, "suggestion"},
		{"parent is a file", "./content/sub", func(t *testing.T, d string) {
			if err := os.WriteFile(filepath.Join(d, "content"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, false, "suggestion"},
		{"home-relative source cannot be judged", "~/content", func(t *testing.T, d string) {}, false, "suggestion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			extra := ""
			if tc.peer {
				extra = peer
			}
			src := strings.ReplaceAll(tc.src, "$DIR", dir)
			writeFile(t, dir, "compose.yaml", fmt.Sprintf(composeYAML, src, extra))
			p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			o := New(p0, nil, "opossum", io.Discard)
			o.crashHint("blog", "chown: changing ownership of '/var/lib/ghost/content': Operation not permitted")
			body, changes := o.PlanOverlay()
			var got []string
			for _, c := range changes {
				if c.Service == "blog" {
					got = append(got, c.Kind)
				}
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("want one %s for blog, got %v\n%s", tc.want, got, body)
			}
			if tc.want == "applied" {
				if strings.Contains(body, suggestionMarker+" service") || !strings.Contains(body, "blog-content:/var/lib/ghost/content") {
					t.Errorf("an applied swap is live YAML under the overlay marker, with its volume:\n%s", body)
				}
				ov := writeFile(t, dir, OverlayFileName, body)
				p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
				if err != nil {
					t.Fatalf("the overlay must load: %v\n%s", err, body)
				}
				if _, ok := p.Volumes["blog-content"]; !ok {
					t.Errorf("the volume must be declared, got %v", p.Volumes)
				}
				if v := p.Services["blog"].Volumes; len(v) != 1 || v[0] != "blog-content:/var/lib/ghost/content" {
					t.Errorf("the mount should now be the volume, got %v", v)
				}
			}
		})
	}
}

func mk(t *testing.T, dir, sub string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
		t.Fatal(err)
	}
}
