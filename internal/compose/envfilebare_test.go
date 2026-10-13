package compose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A line of an env file with no separator is a name on its own, which docker
// compose (v5.5.1) reads as "take it from the environment": the shell, then the
// project's `.env`, and nothing is passed when neither has it. A name with a space
// or punctuation in it is refused, as any other is. Each row is a file measured
// against `docker compose config`.
func TestAnEnvFileLineWithNoSeparatorIsANameTakenFromTheProjectEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		envFile string
		dotenv  string
		shell   map[string]string
		want    string // the service's environment, sorted and space-joined
		refused bool
	}{
		{"a name in .env", "T_A\n", "T_A=fromdotenv\n", nil, "T_A=fromdotenv", false},
		{"a name nothing sets is passed as nothing", "T_A\nT_NONE\n", "T_A=fromdotenv\n", nil, "T_A=fromdotenv", false},
		{"the shell is above .env", "T_A\n", "T_A=fromdotenv\n", map[string]string{"T_A": "shellv"}, "T_A=shellv", false},
		{"a name only the shell has", "T_SH\n", "", map[string]string{"T_SH": "shellv"}, "T_SH=shellv", false},
		{"export in front", "export T_A\n", "T_A=fromdotenv\n", nil, "T_A=fromdotenv", false},
		{"blanks around the name", "  T_A   \n", "T_A=fromdotenv\n", nil, "T_A=fromdotenv", false},
		{"a name nothing sets leaves the value before it", "T_A=first\nT_A\n", "", nil, "T_A=first", false},
		{"the gateway name is not offered", "OPOSSUM_HOST_GATEWAY\nT_B=v\n", "", nil, "T_B=v", false},
		{"export on a line of its own names nothing", "export\nT_B=v\n", "", map[string]string{"export": "x"}, "T_B=v", false},
		{"a later line with a value wins", "T_A\nT_A=over\n", "T_A=fromdotenv\n", nil, "T_A=over", false},
		{"the name alone after its value is read again", "T_A=first\nT_A\n", "T_A=fromdotenv\n", nil, "T_A=fromdotenv", false},
		{"a colon is a separator, with an empty value", "T_A:\n", "T_A=fromdotenv\n", nil, "T_A=", false},
		{"a token on a line of its own is a name nothing sets", "ghp_abc123\nT_B=v\n", "", nil, "T_B=v", false},
		{"dots and dashes are in a name", "T-B.x\nT_B=v\n", "", nil, "T_B=v", false},
		// What docker compose refuses, and what is not read back.
		{"a comment after the name is part of it", "T_A # note\n", "T_A=fromdotenv\n", nil, "", true},
		{"a space inside", "A B\n", "", nil, "", true},
		{"quotes around the name", "\"T_A\"\n", "T_A=fromdotenv\n", nil, "", true},
		{"a slash", "T/A\n", "", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"e.env": tc.envFile, ".env": tc.dotenv,
				"compose.yaml": "services:\n  s:\n    image: x\n    env_file: e.env\n"}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, v := range []string{"T_A", "T_B", "T_NONE", "T_SH", "export"} {
				withoutHostVar(t, v)
			}
			for k, v := range tc.shell {
				t.Setenv(k, v)
			}
			p, err := Load(filepath.Join(dir, "compose.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			env, err := p.Services["s"].ResolvedEnv()
			if tc.refused {
				if err == nil {
					t.Fatalf("the file was accepted, environment %v", env)
				}
				if strings.Contains(err.Error(), strings.TrimSpace(tc.envFile)) && len(strings.TrimSpace(tc.envFile)) > 4 {
					t.Errorf("the error reads the line back: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := append([]string(nil), env...)
			sort.Strings(got)
			if joined := strings.Join(got, " "); joined != tc.want {
				t.Errorf("environment is %q, want %q", joined, tc.want)
			}
		})
	}
}

// The project's own `.env` reads a line of this shape the same way: from the
// shell, and nothing when the shell has none.
func TestADotEnvLineWithNoSeparatorIsANameTakenFromTheShell(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("T_SH\nT_NONE\nT_B=v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  s:\n    image: x\n    environment:\n      A: ${T_SH}\n      B: ${T_B}\n      C: ${T_NONE:-unset}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"T_B", "T_NONE"} {
		withoutHostVar(t, v)
	}
	t.Setenv("T_SH", "shellv")
	p, err := Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := p.Services["s"].ResolvedEnv()
	got := append([]string(nil), env...)
	sort.Strings(got)
	if joined := strings.Join(got, " "); joined != "A=shellv B=v C=unset" {
		t.Errorf("environment is %q", joined)
	}
}

// The project's own `.env`, read the soft way the commands that take a project
// down read it, refuses a name on its own that a name cannot be: no project was
// ever started on one, so it is not read past as other refused names are.
func TestASoftReadRefusesALineThatIsOnlyAnImpossibleName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("services:\n  s:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFilesEnvDirSoft([]string{path}, nil, dir); err == nil || !strings.Contains(err.Error(), "key cannot contain a space") {
		t.Errorf("want the name refused, got %v", err)
	}
}
