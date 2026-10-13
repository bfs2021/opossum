package compose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A name in `environment:` written with no value is taken from the project's
// environment — the shell over the project's `.env` — as docker compose does
// (v5.5.1, `config`). Each row is a form measured there.
func TestABareEnvironmentNameIsTakenFromTheProjectEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string // besides compose.yaml's service block
		envs   string            // the service's keys below `image:`
		shell  map[string]string
		envArg bool // read the file as --env-file instead of .env
		want   string
	}{
		{"list form, value in .env", map[string]string{".env": "FOO=fromdotenv\n"}, "environment:\n      - FOO\n", nil, false, "FOO=fromdotenv"},
		{"map form, null value, value in .env", map[string]string{".env": "FOO=fromdotenv\n"}, "environment:\n      FOO:\n", nil, false, "FOO=fromdotenv"},
		{"the shell is above .env: left bare, for the shell to answer", map[string]string{".env": "FOO=fromdotenv\n"}, "environment:\n      - FOO\n", map[string]string{"FOO": "shellv"}, false, "FOO"},
		{"a value in .env beats the env_file's", map[string]string{".env": "FOO=fromdotenv\n", "f.env": "FOO=fromfile\nBAR=barfile\n"}, "env_file: f.env\n    environment:\n      - FOO\n", nil, false, "BAR=barfile FOO=fromdotenv"},
		{"nothing sets it: stays bare and hides the env_file's", map[string]string{"f.env": "FOO=fromfile\nBAR=barfile\n"}, "env_file: f.env\n    environment:\n      - FOO\n", nil, false, "BAR=barfile FOO"},
		{"nothing sets it: stays bare", nil, "environment:\n      - FOO\n", nil, false, "FOO"},
		{"a written value is left as it is", map[string]string{".env": "FOO=fromdotenv\n"}, "environment:\n      - FOO=written\n", nil, false, "FOO=written"},
		{"a written empty value is left as it is", map[string]string{".env": "FOO=fromdotenv\n"}, "environment:\n      - FOO=\n", nil, false, "FOO="},
		{"an empty value in .env is a value", map[string]string{".env": "FOO=\n"}, "environment:\n      - FOO\n", nil, false, "FOO="},
		{"a value in .env that expands another", map[string]string{".env": "BASE=/srv\nFOO=${BASE}/x\n"}, "environment:\n      - FOO\n", nil, false, "FOO=/srv/x"},
		{"an empty value in the shell beats .env", map[string]string{".env": "FOO=fromdotenv\n"}, "environment:\n      - FOO\n", map[string]string{"FOO": ""}, false, "FOO"},
		{"each bare name is read, not only the first", map[string]string{".env": "FOO=fromdotenv\nBAR=bardotenv\n"}, "environment:\n      - QUX\n      - FOO\n      - BAR\n", nil, false, "BAR=bardotenv FOO=fromdotenv QUX"},
		{"the gateway name is not offered to a bare name", nil, "environment:\n      - OPOSSUM_HOST_GATEWAY\n", nil, false, "OPOSSUM_HOST_GATEWAY"},
		{"--env-file stands where .env would", map[string]string{"other.env": "FOO=fromother\n"}, "environment:\n      - FOO\n", nil, true, "FOO=fromother"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte("services:\n  a:\n    image: x\n    "+tc.envs), 0o644); err != nil {
				t.Fatal(err)
			}
			withoutHostVar(t, "FOO")
			for k, v := range tc.shell {
				t.Setenv(k, v)
			}
			var p *Project
			var err error
			if tc.envArg {
				p, err = Load(path, filepath.Join(dir, "other.env"))
			} else {
				p, err = Load(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			env, err := p.Services["a"].ResolvedEnv()
			if err != nil {
				t.Fatal(err)
			}
			got := append([]string(nil), env...)
			sort.Strings(got)
			if joined := strings.Join(got, " "); joined != tc.want {
				t.Errorf("environment is %q, want %q", joined, tc.want)
			}
			// What `config` shows is the same reading docker compose gives: a name
			// the shell sets shows the shell's value (an empty one included), over `.env`.
			if want, ok := tc.shell["FOO"]; ok {
				out, err := RenderConfig(p)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out, "FOO="+want) {
					t.Errorf("config does not show the shell's value %q over .env:\n%s", want, out)
				}
			}
		})
	}
}
