package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// Every suggestion sits under one `services:` and one `volumes:` of its own, so
// uncommenting the whole block next to an applied entry used to write those keys
// a second time and the overlay no longer loaded. The block is its own document.
func TestUncommentingEverySuggestionLoadsNextToAnAppliedEntry(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"db", "content"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := "name: demo\nservices:\n  blog:\n    image: ghost:6\n    volumes:\n      - ./content:/var/lib/ghost/content\n" +
		"  database:\n    image: mysql:8\n    volumes:\n      - ./db:/var/lib/mysql-x\n" +
		"  app:\n    image: lscr.io/linuxserver/radarr\n"
	writeFile(t, dir, "compose.yaml", src)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	o.crashHint("blog", "chown: changing ownership of '/var/lib/ghost/content': Operation not permitted")
	o.crashHint("database", "chown: changing ownership of '/var/lib/mysql-x': Operation not permitted")
	body, _ := o.PlanOverlay()

	// The overlay as written loads, and the applied entry is in effect.
	ov := writeFile(t, dir, OverlayFileName, body)
	p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
	if err != nil {
		t.Fatalf("the overlay as written must load: %v\n%s", err, body)
	}
	if got := p.Services["app"].Image; got != "ghcr.io/linuxserver/radarr" {
		t.Fatalf("the applied entry is not in effect: %q", got)
	}

	// With the suggestions uncommented it loads as well, and they are in effect.
	all := uncommentSuggestions(body)
	ov = writeFile(t, dir, OverlayFileName, all)
	p, err = compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
	if err != nil {
		t.Fatalf("the overlay with every suggestion uncommented must load: %v\n%s", err, all)
	}
	if got := p.Services["app"].Image; got != "ghcr.io/linuxserver/radarr" {
		t.Errorf("uncommenting the suggestions took the applied entry away: %q", got)
	}
	for svc, want := range map[string]string{"blog": "blog-content:/var/lib/ghost/content", "database": "database-mysql-x:/var/lib/mysql-x"} {
		found := false
		for _, v := range p.Services[svc].Volumes {
			found = found || v == want
		}
		if !found {
			t.Errorf("%s: the uncommented suggestion is not in effect, volumes %v", svc, p.Services[svc].Volumes)
		}
	}
	if _, ok := p.Volumes["blog-content"]; !ok {
		t.Errorf("the volume the suggestion declares is not declared: %v", p.Volumes)
	}
}

// With no applied entry the file has no live `services:`, and the block needs no
// second document: it is the first thing in the file once uncommented.
func TestTheSuggestionsBlockStartsADocumentOnlyBesideAnAppliedEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "compose.yaml", "name: demo\nservices:\n  blog:\n    image: ghost:6\n    volumes:\n      - ./content:/var/lib/ghost/content\n")
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	o.crashHint("blog", "chown: changing ownership of '/var/lib/ghost/content': Operation not permitted")
	body, _ := o.PlanOverlay()
	if strings.Contains(body, "# ---") {
		t.Errorf("a suggestion alone has no document to stand beside:\n%s", body)
	}
	ov := writeFile(t, dir, OverlayFileName, uncommentSuggestions(body))
	if _, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil); err != nil {
		t.Fatalf("a lone suggestion, uncommented, must load: %v", err)
	}
}

// The named volumes the applied entries introduce are declared in the first
// document with them. Declared after the suggestions, they landed in the second
// one beside the suggestions' own `volumes:`, and uncommenting wrote the key twice.
func TestAnAppliedVolumeAndASuggestedOneDoNotClashWhenTheBlockIsUncommented(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	holdData(t, dir, "content")
	src := "name: demo\nservices:\n  database:\n    image: mysql:8\n    volumes:\n      - ./db:/var/lib/mysql\n" +
		"  blog:\n    image: ghost:6\n    volumes:\n      - ./content:/var/lib/ghost/content\n"
	writeFile(t, dir, "compose.yaml", src)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	o.crashHint("blog", "chown: changing ownership of '/var/lib/ghost/content': Operation not permitted")
	body, changes := o.PlanOverlay()
	kinds := map[string]int{}
	for _, c := range changes {
		kinds[c.Kind]++
	}
	if kinds["applied"] != 1 || kinds["suggestion"] != 1 {
		t.Fatalf("want one applied volume swap and one suggestion, got %+v\n%s", changes, body)
	}
	for name, text := range map[string]string{"as written": body, "uncommented": uncommentSuggestions(body)} {
		ov := writeFile(t, dir, OverlayFileName, text)
		p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
		if err != nil {
			t.Fatalf("%s: the overlay must load: %v\n%s", name, err, text)
		}
		if _, ok := p.Volumes["database-data"]; !ok && len(p.Volumes) == 0 {
			t.Errorf("%s: no volume declared at all: %v", name, p.Volumes)
		}
	}
}
