package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// A release reads what an earlier one wrote: the files `up` of v0.49.0 left for a project with a supervisor, kept as
// they were (testdata/state-v0.49.0, taken from a real `up`). A change that renames a file, or reads the pid file in
// another shape, passes every test that writes its own state with the new code, and leaves a project `up` made with
// the old release unreadable by the new one.
func TestAStateTheEarlierReleaseWroteIsStillRead(t *testing.T) {
	const project = "fixt"
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dest := filepath.Join(state, "opossum", project)
	copyTree(t, filepath.Join("testdata", "state-v0.49.0"), dest)

	t.Run("the files are where the earlier release put them", func(t *testing.T) {
		// The check below that a stale pid is not taken for a live supervisor passes just as well when the file is
		// not found at all, so the place is asked for by name.
		for name, find := range map[string]func(string) (string, error){"supervisor.pid": supervisorPidFile, "supervised": watchedFile} {
			path, err := find(project)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(dest, name); path != want {
				t.Errorf("the file the reader looks for is %s, want %s", path, want)
			}
		}
	})
	t.Run("the lock file is the one the earlier release took", func(t *testing.T) {
		before, err := os.ReadFile(filepath.Join(dest, "lock"))
		if err != nil {
			t.Fatal(err)
		}
		l, err := lockProject(project)
		if err != nil {
			t.Fatalf("lockProject over the earlier release's project: %v", err)
		}
		defer l.f.Close()
		// The pid it notes is written to the file it opened, which is the one that was there.
		after, err := os.ReadFile(filepath.Join(dest, "lock"))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != strconv.Itoa(os.Getpid()) || string(after) == string(before) {
			t.Errorf("the lock file holds %q after the lock was taken, want this process's pid (it held %q): the lock was taken on another file", after, before)
		}
	})
	t.Run("a config file is looked for where the earlier release wrote it", func(t *testing.T) {
		content := "hello"
		o := &Orchestrator{Project: &compose.Project{
			Name:    project,
			Configs: map[string]compose.Config{"app": {Content: &content}},
		}}
		mounts, err := o.configMounts("web", &compose.Service{Configs: compose.ConfigRefs{{Source: "app", Target: "/etc/app.conf"}}}, false)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(dest, "configs", "web", "app") + ":/etc/app.conf:ro"
		if len(mounts) != 1 || mounts[0] != want {
			t.Errorf("configMounts = %v, want [%s]", mounts, want)
		}
		if _, err := os.Stat(filepath.Join(dest, "configs", "web", "app")); err != nil {
			t.Errorf("the file the earlier release wrote is not where the reader looks: %v", err)
		}
	})
	t.Run("the pid file is read as a pid and its start marker", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(dest, "supervisor.pid"))
		if err != nil {
			t.Fatal(err)
		}
		pid, started, ok := parsePidFile(string(b))
		if !ok || pid != 60010 || started != "Thu-Oct-22-23:21:37-2026" {
			t.Errorf("parsePidFile(%q) = %d, %q, %v; want 60010 and the start marker the earlier release wrote", b, pid, started, ok)
		}
	})
	t.Run("a supervisor that has since exited is not taken for a live one", func(t *testing.T) {
		if pid := SupervisorPID(project); pid != 0 {
			t.Errorf("SupervisorPID = %d for a pid file whose process is not that supervisor now, want 0", pid)
		}
	})
	t.Run("the set it was watching is read", func(t *testing.T) {
		if got := Watched(project); !reflect.DeepEqual(got, []string{"web"}) {
			t.Errorf("Watched = %v, want [web]", got)
		}
		if !WatchedMatches(project, []string{"web"}) {
			t.Error("WatchedMatches(web) = false for the set the earlier release recorded")
		}
		if WatchedMatches(project, []string{"web", "db"}) {
			t.Error("WatchedMatches(web, db) = true for a record of web alone")
		}
	})
}

// copyTree copies the files under src to dst, keeping the layout.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A supervisor the earlier release started and that is still running is found by the later one: its pid file holds
// the pid and the start time as `ps -o lstart=` prints it with its fields joined by "-" (testdata/state-v0.49.0 holds
// one whose process has gone), and the reader compares that with what `ps` says now. The marker here is made the way
// the earlier release made it, not by the code under test, so a change of the format or of the comparison shows.
func TestALiveSupervisorTheEarlierReleaseStartedIsStillFound(t *testing.T) {
	const project = "fixtlive"
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir(t, project)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a process to stand for the supervisor: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
	if err != nil {
		t.Skipf("ps cannot say when the process started: %v", err)
	}
	marker := strings.Join(strings.Fields(string(out)), "-")
	// The shape of the earlier release's marker, read off the fixture: "Thu-Oct-22-23:21:37-2026".
	if !regexp.MustCompile(`^[A-Z][a-z]{2}-[A-Z][a-z]{2}-\d{1,2}-\d\d:\d\d:\d\d-\d{4}$`).MatchString(marker) {
		t.Fatalf("the start marker %q is not in the shape the earlier release wrote", marker)
	}
	path, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	write := func(started string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)+" "+started+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(marker)
	if got := SupervisorPID(project); got != cmd.Process.Pid {
		t.Errorf("SupervisorPID = %d for the live process the pid file names with its own start marker, want %d", got, cmd.Process.Pid)
	}
	write("Mon-Jan-1-00:00:00-2001") // the same pid taken by another process since
	if got := SupervisorPID(project); got != 0 {
		t.Errorf("SupervisorPID = %d for a pid file whose marker is another start time, want 0", got)
	}
}

// A pid file with no start marker (an older release wrote none) cannot be tied to a process, and the pid in it may be
// another process's by now. It is not taken for the supervisor, and `down` does not signal that process: stopping a
// supervisor escalates to SIGKILL, and the process behind the pid here is not the project's.
func TestAPidFileWithoutAMarkerDoesNotNameAProcessToStop(t *testing.T) {
	const project = "fixtnomarker"
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir(t, project)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a process to stand for the one the pid names: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	path, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	psFails := func(t *testing.T) {
		t.Helper()
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		oldWait := psWait
		psWait = 0
		t.Cleanup(func() { psWait = oldWait })
	}
	for _, tc := range []struct {
		name, body string
		setup      func(*testing.T)
	}{
		{"no marker", strconv.Itoa(cmd.Process.Pid) + "\n", func(*testing.T) {}},
		{"only the pid", strconv.Itoa(cmd.Process.Pid), func(*testing.T) {}},
		// What the marker's absence decides, apart from the comparison that would also call it stale: with `ps` unable to
		// say anything, a pid file with no marker is not "a live process whose start time cannot be read" either, which
		// is the state `down` reports as unchecked and leaves its file for.
		{"no marker, and ps cannot say", strconv.Itoa(cmd.Process.Pid) + "\n", psFails},
	} {
		name, body := tc.name, tc.body
		t.Run(name, func(t *testing.T) {
			tc.setup(t)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := SupervisorPID(project); got != 0 {
				t.Errorf("SupervisorPID = %d for a pid file with no start marker, want 0", got)
			}
			if stopped, attempted := StopSupervisor(project); stopped || attempted {
				t.Errorf("StopSupervisor = stopped %v, attempted %v for a pid file with no start marker, want neither", stopped, attempted)
			}
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Errorf("the process the pid file named was signalled and is gone: %v", err)
			}
		})
	}
}
