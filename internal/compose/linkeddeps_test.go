package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What `links:` and `network_mode`, `pid` and `ipc: service:<name>` name is a dependency of the service, as docker compose reads it (v5.5.1 `config`:
// `depends_on: {b: {condition: service_started, required: true}}`, the condition of a `depends_on` the file writes for the same service kept):
// the alias after the `:` of a link is not a service, one `network_mode` names one service, and a value that names none (`host`, `container:`)
// is no dependency. A target the file does not define is refused for a service with no `profiles:`, and left to the command that
// finds the service active for one that has them (#1802).
func TestLinksAndNetworkModeServiceAreDependenciesOfTheService(t *testing.T) {
	const bc = "  b:\n    image: y\n  c:\n    image: z\n"
	for _, tc := range []struct {
		name, body, want string // want: the services a depends on with their conditions, or "error", or "loads" with the linked names
	}{
		{"links with an alias", "services:\n  a:\n    image: x\n    links: [\"b:alias\", c]\n" + bc, "b=service_started c=service_started"},
		{"network_mode service", "services:\n  a:\n    image: x\n    network_mode: service:b\n" + bc, "b=service_started"},
		{"the condition a depends_on writes is kept", "services:\n  a:\n    image: x\n    links: [b]\n    depends_on:\n      b: {condition: service_healthy}\n  b:\n    image: y\n    healthcheck: {test: [CMD, \"true\"]}\n", "b=service_healthy"},
		{"one service named three ways is one dependency", "services:\n  a:\n    image: x\n    links: [b]\n    network_mode: service:b\n    volumes_from: [b]\n  b:\n    image: y\n", "b=service_started"},
		{"pid service", "services:\n  a:\n    image: x\n    pid: service:b\n" + bc, "b=service_started"},
		{"ipc service", "services:\n  a:\n    image: x\n    ipc: service:b\n" + bc, "b=service_started"},
		{"pid and ipc name two services", "services:\n  a:\n    image: x\n    pid: service:b\n    ipc: service:c\n" + bc, "b=service_started c=service_started"},
		{"pid, ipc and network_mode naming one service is one dependency", "services:\n  a:\n    image: x\n    pid: service:b\n    ipc: service:b\n    network_mode: service:b\n  b:\n    image: y\n", "b=service_started"},
		{"pid host is no dependency", "services:\n  a:\n    image: x\n    pid: host\n" + bc, ""},
		{"pid with a leading space is no dependency", "services:\n  a:\n    image: x\n    pid: \" service:b\"\n" + bc, ""},
		{"pid with a capital S is no dependency", "services:\n  a:\n    image: x\n    pid: \"Service:b\"\n" + bc, ""},
		{"pid container is no dependency", "services:\n  a:\n    image: x\n    pid: container:other\n" + bc, ""},
		{"ipc shareable is no dependency", "services:\n  a:\n    image: x\n    ipc: shareable\n" + bc, ""},
		{"a pid service with no name", "services:\n  a:\n    image: x\n    pid: \"service:\"\n", "error"},
		{"a pid service that is undefined", "services:\n  a:\n    image: x\n    pid: service:nosuch\n", "error"},
		{"an ipc service that is undefined", "services:\n  a:\n    image: x\n    ipc: service:nosuch\n", "error"},
		{"an ipc service that is undefined of a gated service", "services:\n  a:\n    image: x\n    profiles: [g]\n    ipc: service:nosuch\n", "loads"},
		{"network_mode host is no dependency", "services:\n  a:\n    image: x\n    network_mode: host\n" + bc, ""},
		{"network_mode container is no dependency", "services:\n  a:\n    image: x\n    network_mode: container:other\n" + bc, ""},
		{"a link with two colons is one name", "services:\n  a:\n    image: x\n    links: [\"b:c:d\"]\n" + bc, "error"},
		{"a link with no name", "services:\n  a:\n    image: x\n    links: [\"\"]\n", "error"},
		{"a network_mode service with no name", "services:\n  a:\n    image: x\n    network_mode: \"service:\"\n", "error"},
		{"a link to an undefined service", "services:\n  a:\n    image: x\n    links: [nosuch]\n", "error"},
		{"a network_mode service that is undefined", "services:\n  a:\n    image: x\n    network_mode: service:nosuch\n", "error"},
		{"a link to an undefined service of a gated service", "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [nosuch]\n", "loads"},
		{"a link to a gated service of a service that has none", "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n", "b=service_started"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := LoadFiles([]string{p}, nil)
			if tc.want == "error" {
				if err == nil || !strings.Contains(err.Error(), "depends on undefined service") {
					t.Fatalf("want it refused as an undefined dependency, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if tc.want == "loads" {
				return
			}
			var got []string
			for _, d := range project.Services["a"].DependsOn {
				got = append(got, d.Name+"="+d.Condition)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("a depends on %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}
}

// `pid: service:` and `ipc: service:` follow the file that wins: a later file that writes `pid: host` over a first file's `pid: service:b` leaves no dependency, and one
// that adds `pid: service:b` to a service that had none makes it (docker compose v5.5.1 `config` of the two files, measured, #2015).
func TestPidServiceFollowsTheFileThatWins(t *testing.T) {
	for _, tc := range []struct{ name, first, second, want string }{
		{"a later file writes host over a service", "services:\n  a:\n    image: x\n    pid: service:b\n  b:\n    image: y\n", "services:\n  a:\n    pid: host\n", ""},
		{"a later file adds a service", "services:\n  a:\n    image: x\n  b:\n    image: y\n", "services:\n  a:\n    pid: service:b\n", "b=service_started"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, body := range []string{tc.first, tc.second} {
				p := filepath.Join(dir, "c"+string(rune('1'+i))+".yaml")
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			project, err := LoadFiles(paths, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			var got []string
			for _, d := range project.Services["a"].DependsOn {
				got = append(got, d.Name+"="+d.Condition)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("a depends on %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}
}

// `pid` and `ipc` are fields now (their `service:<name>` is a dependency), and are still listed as ignored: the namespace is not shared, whatever it names
// (a value of any kind: the key is what is listed). A bare `ipc:` is refused in the words of the schema, as docker compose says it (#2015).
func TestPidAndIpcAreStillListedAsIgnored(t *testing.T) {
	for _, tc := range []struct{ name, pid, ipc string }{
		{"host and shareable", "host", "shareable"},
		{"a service", "service:b", "service:b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			body := "services:\n  a:\n    image: x\n    pid: " + tc.pid + "\n    ipc: " + tc.ipc + "\n  b:\n    image: y\n"
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := LoadFiles([]string{p}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(project.Services["a"].Unsupported, " "); got != "ipc pid" {
				t.Errorf("a ignores %q, want %q", got, "ipc pid")
			}
			if got := project.Services["b"].Unsupported; len(got) != 0 {
				t.Errorf("b ignores %v, want nothing", got)
			}
		})
	}
	t.Run("a bare ipc is refused in the schema's words", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.yaml")
		if err := os.WriteFile(p, []byte("services:\n  a:\n    image: x\n    ipc: ~\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFiles([]string{p}, nil); err == nil || !strings.Contains(err.Error(), "services.a.ipc must be a string") {
			t.Fatalf("want `services.a.ipc must be a string`, got %v", err)
		}
	})
}
