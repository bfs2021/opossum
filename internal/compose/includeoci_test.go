package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An `oci://` include names a compose file kept in a registry, which docker compose
// fetches (the labspace projects, measured on 18 of them) and opossum does not. It is
// said so, in the include's own words, rather than as a file that is not there: read
// as a path `oci://org/name` came out as `<dir>/oci:/org/name`, "no such file or
// directory", and the reader went looking for a file nobody had.
func TestAnOCIIncludeIsRefusedAsOneAndNotAsAMissingFile(t *testing.T) {
	const ref = "oci://dockersamples/labspace-content-dev"
	const svc = "services:\n  a: {image: x}\n"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"a string entry", "include:\n  - " + ref + "\n" + svc},
		{"a string entry after a path", "include:\n  - ./other.yaml\n  - " + ref + "\n" + svc},
		{"a mapping with path", "include:\n  - path: " + ref + "\n" + svc},
		{"a mapping with a list of paths", "include:\n  - path:\n      - ./other.yaml\n      - " + ref + "\n" + svc},
		{"an included file that includes one", "include:\n  - ./nested.yaml\n" + svc},
		// Said before the entry's env files are read: a missing one is not what is wrong.
		{"a mapping with an env_file that is not there", "include:\n  - path: " + ref + "\n    env_file: ./missing.env\n" + svc},
		{"a mapping with a project_directory", "include:\n  - path: " + ref + "\n    project_directory: ./sub\n" + svc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "other.yaml"), []byte("services:\n  b: {image: y}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "nested.yaml"), []byte("include:\n  - "+ref+"\nservices:\n  c: {image: z}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("an OCI include is refused")
			}
			for _, want := range []string{ref, "OCI registry", "does not fetch", "(docker compose does)", "local copy", ", or remove the include"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal should say %q, got: %v", want, err)
				}
			}
			for _, not := range []string{"no such file", "cannot be read", "relative path"} {
				if strings.Contains(err.Error(), not) {
					t.Errorf("the refusal reads as a missing file (%q), got: %v", not, err)
				}
			}
		})
	}
}

// The refusal is for the reference `oci://` begins and for nothing a file can be
// named: an include written with no `./` in front, as docker compose files write them,
// is read as the path it is. Each name is tried twice — where the file is there (it
// loads, and its service comes in) and where it is not (the old "cannot be read",
// not the OCI sentence). `oci://` is matched exactly, lower case and from the first
// character; how docker compose treats another spelling (`OCI://`, a leading space,
// `oci:/`) was not measured: only what was a path before stays one (`oci:/x.yaml` is
// still the directory `oci:` and a file in it), and no other spelling is pinned.
func TestAnIncludeThatOnlyLooksLikeOCIIsStillAPath(t *testing.T) {
	for _, name := range []string{"oci", "oci.yaml", "oci-content.yaml", "oci_x/n.yaml", "ocix.yaml", "./oci.yaml", "oci:/x.yaml", "x-oci://not-a-scheme.yaml"} {
		write := func(t *testing.T, withFile bool) string {
			dir := t.TempDir()
			if withFile {
				target := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Skipf("%q cannot be a file name here: %v", name, err)
				}
				if err := os.WriteFile(target, []byte("services:\n  b: {image: y}\n"), 0o644); err != nil {
					t.Skipf("%q cannot be a file name here: %v", name, err)
				}
			}
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte("include:\n  - "+name+"\nservices:\n  a: {image: x}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		t.Run(name+" that is there", func(t *testing.T) {
			p, err := Load(write(t, true))
			if err != nil {
				t.Fatalf("%q is a file that is there, so it loads, got: %v", name, err)
			}
			if p.Services["b"] == nil {
				t.Errorf("%q was read, so its service b comes in, got %v", name, p.Services)
			}
		})
		t.Run(name+" that is not there", func(t *testing.T) {
			_, err := Load(write(t, false))
			if err == nil {
				t.Fatal("the file is not there, so reading it fails")
			}
			if strings.Contains(err.Error(), "OCI registry") {
				t.Errorf("%q is a path, not an OCI reference, got: %v", name, err)
			}
			if !strings.Contains(err.Error(), "cannot be read") {
				t.Errorf("a path that is not there is still said as one, got: %v", err)
			}
		})
	}
}

// Which file's include it is, said so: in a nested one the refusal names the included
// file, not the project's own.
func TestAnOCIIncludeInAnIncludedFileNamesThatFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inner.yaml"), []byte("include:\n  - oci://org/n\nservices:\n  c: {image: z}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("include:\n  - ./inner.yaml\nservices:\n  a: {image: x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("an OCI include is refused")
	}
	if want := "compose file " + filepath.Join(dir, "inner.yaml") + ": include oci://org/n"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal should name the file that has the include (%q), got: %v", want, err)
	}
}
