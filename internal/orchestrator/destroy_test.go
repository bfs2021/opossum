package orchestrator_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

// The orphan sweep is the one part of destroy that acts on containers nobody
// named: anything the runtime reports for this project that no current service
// claims. What keeps it inside the project is the label — and this is the shim
// that can model a foreign container, so this is where that has to be proved.
func TestDestroyPlanOrphansAreScopedByLabel(t *testing.T) {
	rt, _ := fakeShim(t)
	// Keep supervisor state out of the developer's real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt,
		"LS_CONTAINERS=old.demo.opossum", // ours, but no service claims it any more
		"LS_PROJECT=demo",
		"LS_FOREIGN=web.other.opossum",    // labelled for a different project
		"LS_UNLABELED=stray.demo.opossum", // no project label: made outside opossum
		"INSPECT_PROJECT=demo",
	)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	joined := strings.Join(plan.Containers, " ")
	if !strings.Contains(joined, "old.demo.opossum") {
		t.Errorf("a container left behind by this project is an orphan and should be removed, got %v", plan.Containers)
	}
	if strings.Contains(joined, "other") {
		t.Errorf("another project's container must never be in the plan, got %v", plan.Containers)
	}
	if strings.Contains(joined, "stray") {
		t.Errorf("a container with no project label was not made by this project and must never be in the plan, got %v", plan.Containers)
	}
}

// The service's own name too: a container of it that carries no project
// label was made outside opossum, and `destroy` — which removes everything
// opossum created — must not plan to remove it.
func TestDestroyPlanLeavesAServiceContainerWithNoProjectLabel(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "INSPECT_UNLABELED=web.demo.opossum")
	strictContainers(t, rt, "web.demo.opossum", "db.demo.opossum")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}, "db": {Image: "db"}})
	plan, err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	joined := strings.Join(plan.Containers, " ")
	if !strings.Contains(joined, "db.demo.opossum") {
		t.Errorf("this project's own container should be in the plan, got %v", plan.Containers)
	}
	if strings.Contains(joined, "web.demo.opossum") {
		t.Errorf("a service container with no project label must not be in the plan, got %v", plan.Containers)
	}
}

// A plan is shown to someone about to approve it. Listing things that are not
// there teaches them not to read it — and it is how a plan starts to drift from
// what the removal does.
func TestDestroyPlanOnlyListsWhatExists(t *testing.T) {
	rt, _ := fakeShim(t)
	// Keep supervisor state out of the developer's real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt,
		"INSPECT_PROJECT=demo",
		"INSPECT_ABSENT=web.demo.opossum web-run.demo.opossum",
		"IMAGE_ABSENT=demo-web:latest",
		"NETWORK_ABSENT=demo-net",
		"VOLUME_LS=NAME", // no volumes at all
	)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "", Build: &compose.Build{Context: "."}, Volumes: []string{"data:/d"}},
	})
	p.Volumes = map[string]compose.VolumeDecl{"data": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(plan.Containers) != 0 || len(plan.Images) != 0 || len(plan.Networks) != 0 || len(plan.Volumes) != 0 {
		t.Errorf("nothing exists, so the plan should be empty of runtime objects, got %+v", plan)
	}
}

// --keep-images is for the case the default gets wrong: a pulled image shared
// with other projects, minutes to fetch again.
func TestDestroyPlanKeepImages(t *testing.T) {
	rt, _ := fakeShim(t)
	// Keep supervisor state out of the developer's real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	strictContainers(t, rt, "db.demo.opossum")
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	with, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(with.Images) == 0 {
		t.Fatal("by default destroy removes the images it pulled")
	}
	without, err := o.DestroyPlanFor(false, true, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(without.Images) != 0 {
		t.Errorf("--keep-images should leave images out of the plan, got %v", without.Images)
	}
	if len(without.Containers) == 0 {
		t.Error("--keep-images is about images alone; the containers still go")
	}
}

// Started() is measured, not remembered: it says what is running when Up
// finishes, so a container that disappeared between two calls is not reported
// just because the first call started it. The caller decides what to supervise
// from this, and supervising something that is gone means announcing a watcher
// for a container nobody can see.
func TestStartedIsMeasuredNotRemembered(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	state := t.TempDir()
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+state)
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "db",
			Healthcheck: &compose.Healthcheck{
				Interval: time.Millisecond, // a test never waits the 30 s a healthcheck without one gets
				Test:     []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(o.Started()) != 2 {
		t.Fatalf("the first up should have started both services, got %v", o.Started())
	}

	// web goes away between the calls — someone removed it by hand. Done through the
	// shim's own `delete`, so this exercises the path the runtime would take rather
	// than hand-writing a marker whose format only this test knows.
	rt.Delete("web.demo.opossum")
	if rt.Inspect("web.demo.opossum").Exists {
		t.Fatal("this test needs web to be gone; the shim still reports it")
	}

	setShimEnv(rt, "HEALTH_HANG=1")
	if err := o.Up(true); err == nil {
		t.Fatal("the second up should fail when its dependency never becomes healthy")
	}
	for _, name := range o.Started() {
		if name == "web" {
			t.Errorf("Started() = %v, but web is gone — this answer is the first call's memory, "+
				"not a measurement", o.Started())
		}
	}
}

// A service left alone because it was already up to date is not in the rollback
// list, so it is still running when a later service fails the bring-up. Reporting
// nothing there would leave it running with nobody watching it.
//
// The survivor is named so it sorts *after* the service that fails: the startup
// order is alphabetical, so a survivor earlier in the order is one the loop
// happened to reach. An implementation that collects survivors as it walks passes
// with the earlier name and fails with this one — which is how the first version
// of this test passed while the set it measured was wrong.
func TestStartedListsWhatSurvivedARollback(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// STATE_DIR makes the shim remember deletions, so "still running" can be asked.
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "db",
			Healthcheck: &compose.Healthcheck{
				Interval: time.Millisecond, // a test never waits the 30 s a healthcheck without one gets
				Test:     []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
		"zcache": {Image: "cache", Restart: "always"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	// First: everything comes up, so the config hashes are recorded.
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}

	// Second: db's healthcheck never passes, so the bring-up fails and rolls back.
	// db and zcache are unchanged, so they are skipped — and survive.
	setShimEnv(rt, "HEALTH_HANG=1")
	if err := o.Up(true); err == nil {
		t.Fatal("the second up should fail")
	}
	// The exact set, not membership: a version that returned the previous call's
	// answer, or the whole order, would satisfy a membership test.
	got := append([]string(nil), o.Started()...)
	sort.Strings(got)
	want := []string{"db", "web", "zcache"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Started() = %v, want %v — all three were left alone by the failed call (it "+
			"never got past db's healthcheck) and are still running from the first up", got, want)
	}
}

// A dry run starts nothing, so it must not leave the previous run's answer in
// place. This is the one path where the reset at the top of Up is observable:
// every other exit either assigns the set or rolls back and assigns what
// survived, so without a test here the reset looks redundant and would be
// removed — taking the "describes the last Up and nothing before it" guarantee
// with it.
func TestStartedIsEmptyAfterADryRun(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web", Restart: "always"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(o.Started()) == 0 {
		t.Fatal("the first up should have started something for this test to mean anything")
	}

	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := o.Started(); len(got) != 0 {
		t.Errorf("a dry run starts nothing, so Started() should be empty; got %v — that is the "+
			"previous run's answer", got)
	}
}

// A rollback whose removal did not take is the one case where "what is running"
// and "what should be supervised" come apart. The container is still there, but
// opossum was trying to delete it — handing it to the supervisor would have the
// supervisor bring back what the teardown was removing.
func TestStartedExcludesWhatTheRollbackTriedToRemove(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		// db is created by this call, and its removal quietly does nothing.
		"DELETE_STICKY=db.demo.opossum",
		"HEALTH_HANG=1",
		"INSPECT_ABSENT=web.demo.opossum")
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image:   "db",
			Restart: "always",
			Healthcheck: &compose.Healthcheck{
				Interval: time.Millisecond, // a test never waits the 30 s a healthcheck without one gets
				Test:     []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			Restart:   "always",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatal("up should fail when a dependency never becomes healthy")
	}
	// Assert the premise. The whole point is a container that is *still there* after
	// the teardown tried to remove it — if the shim's sticky-delete knob stopped
	// working, db would simply be gone and this test would pass while proving
	// nothing about the exclusion.
	if !rt.Inspect("db.demo.opossum").Exists {
		t.Fatal("this test needs db to survive its own removal; the shim reports it as gone")
	}
	for _, name := range o.Started() {
		if name == "db" {
			t.Errorf("Started() = %v — db is still there only because its removal failed, and "+
				"supervising it would undo the rollback", o.Started())
		}
	}
}

// The reset at the top of Up has to run before the checks that can return early.
// Without that, a failure that never measures anything — the runtime is gone —
// leaves the previous call's answer in place, and the caller supervises a stack
// it was never told about.
func TestStartedIsEmptyWhenTheRuntimeIsGone(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web", Restart: "always"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(o.Started()) == 0 {
		t.Fatal("the first up should have started something for this test to mean anything")
	}

	rt.Bin = filepath.Join(t.TempDir(), "no-such-container-binary")
	if err := o.Up(true); err == nil {
		t.Fatal("expected the runtime-absent error")
	}
	if got := o.Started(); len(got) != 0 {
		t.Errorf("Started() = %v — the previous call's answer survived a failure that returned "+
			"before anything was measured", got)
	}
}

// Presence, not liveness. A service that crashed while nobody was watching is
// what `restart:` exists for, so a rollback elsewhere in the project must not
// quietly drop it from the set the supervisor is given.
func TestStartedListsAStoppedSurvivor(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "db",
			Healthcheck: &compose.Healthcheck{
				Interval: time.Millisecond, // a test never waits the 30 s a healthcheck without one gets
				Test:     []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
		// After web, which is where the second up stops: a service the stop
		// never reached. (Its name no longer puts it last; its place does.)
		"cache": {
			Image: "cache", Restart: "always",
			DependsOn: compose.DependsOn{{Name: "web", Condition: compose.ConditionStarted}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	// Between the calls cache crashes: still there, no longer running.
	setShimEnv(rt, "HEALTH_HANG=1", "INSPECT_STOPPED=cache.demo.opossum")
	if err := o.Up(true); err == nil {
		t.Fatal("the second up should fail")
	}
	var saw bool
	for _, name := range o.Started() {
		if name == "cache" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("cache exists but is stopped — that is what `restart:` is for, so it belongs "+
			"in the set; got %v", o.Started())
	}
}

// Started() answers for the services this Up was asked about. `up web` says
// nothing about db, and reporting it here would hand the supervisor a set the
// user never named — the rollback counterpart of watching only what `up` started.
func TestStartedAfterARollbackStaysWithinTheSelectedServices(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	p := project("demo", map[string]*compose.Service{
		"db":    {Image: "db", Restart: "always"},
		"probe": {Image: "probe", Healthcheck: &compose.Healthcheck{Interval: time.Millisecond, Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1}},
		"web":   {Image: "web", Restart: "always", DependsOn: compose.DependsOn{{Name: "probe", Condition: compose.ConditionHealthy}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}

	// Only web (and its dependency) is named; db is running but out of scope.
	setShimEnv(rt, "HEALTH_HANG=1")
	if err := o.Up(true, "web"); err == nil {
		t.Fatal("the partial up should fail")
	}
	for _, name := range o.Started() {
		if name == "db" {
			t.Errorf("Started() = %v — db was never named on this call, so it is not this "+
				"call's to report", o.Started())
		}
	}
}

// Workspace snapshots are the one thing `ws` leaves on disk, and they are usually
// the largest. destroy must not remove them — a snapshot belongs to the directory
// that was snapshotted, and the same directory outlives any number of projects —
// but it must say they are there, or a command that promises to leave no trace is
// the reason someone finds gigabytes a month later.
func TestDestroyPlanNamesWorkspaceSnapshotsWithoutRemovingThem(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Nothing runtime-side exists, so the removal has only the on-disk work to do —
	// which is the half this test is about.
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		"INSPECT_ABSENT=web.demo.opossum web-run.demo.opossum",
		"NETWORK_ABSENT=demo-net", "IMAGE_ABSENT=web", "VOLUME_LS=NAME")

	base := t.TempDir()
	here := filepath.Join(base, workspace.SnapshotDirName)              // ws --path ./work
	nested := filepath.Join(base, "sandbox", workspace.SnapshotDirName) // ws --path ./sandbox/work
	for _, dir := range []string{here, nested} {
		if err := os.MkdirAll(filepath.Join(dir, "before-rollback-1"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	p.BaseDir = base
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if got := plan.SnapshotDirs; !reflect.DeepEqual(got, []string{here, nested}) {
		t.Errorf("SnapshotDirs = %v, want %v", got, []string{here, nested})
	}
	// The reason they are reported at all is that they are not removed. If one ever
	// meets the removal list — in either direction of containment — the report
	// becomes a lie in the worst direction: a path named as kept, with an `rm -rf`
	// for something that will not be there.
	for _, path := range plan.Paths {
		for _, snap := range plan.SnapshotDirs {
			if path == snap ||
				strings.HasPrefix(path, snap+string(filepath.Separator)) ||
				strings.HasPrefix(snap, path+string(filepath.Separator)) {
				t.Errorf("destroy would remove %q, and %q is reported as left alone — one of "+
					"those two statements is false", path, snap)
			}
		}
	}
	// And running it really does leave them alone.
	if err := o.Destroy(plan); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, snap := range []string{here, nested} {
		if _, err := os.Stat(snap); err != nil {
			t.Errorf("%s is gone after destroy: %v", snap, err)
		}
	}
}

// A project that never used `ws` should not be told about snapshots it does not
// have: a leftovers list that names things that are not there is one people stop
// reading.
func TestDestroyPlanHasNoSnapshotsWhenNoneWereTaken(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	p.BaseDir = base
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(plan.SnapshotDirs) != 0 {
		t.Errorf("nothing was snapshotted, so nothing should be reported, got %v", plan.SnapshotDirs)
	}
}

// A workspace can live inside `.opossum/`, and then its snapshots do too — and
// `.opossum/` is one of the things destroy removes. Reporting those snapshots as
// left alone would be the worst version of this report: a path named as kept,
// with an `rm -rf` for something that is already gone.
func TestDestroyPlanDoesNotReportSnapshotsItIsAboutToRemove(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		"INSPECT_ABSENT=web.demo.opossum web-run.demo.opossum",
		"NETWORK_ABSENT=demo-net", "IMAGE_ABSENT=web", "VOLUME_LS=NAME")

	base := t.TempDir()
	doomed := filepath.Join(base, ".opossum", workspace.SnapshotDirName)
	safe := filepath.Join(base, workspace.SnapshotDirName)
	for _, dir := range []string{doomed, safe} {
		if err := os.MkdirAll(filepath.Join(dir, "try-1"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	p.BaseDir = base
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	// `.opossum/` is in the removal list, so the snapshots under it are not kept.
	if !reflect.DeepEqual(plan.SnapshotDirs, []string{safe}) {
		t.Errorf("SnapshotDirs = %v, want only %v — the ones under .opossum/ are being "+
			"removed, so reporting them as left alone is a lie", plan.SnapshotDirs, safe)
	}
	if err := o.Destroy(plan); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// And the claim holds both ways: what was reported survived, what was not
	// reported is genuinely gone rather than quietly kept.
	if _, err := os.Stat(safe); err != nil {
		t.Errorf("%s was reported as left alone but is gone: %v", safe, err)
	}
	if _, err := os.Stat(doomed); !os.IsNotExist(err) {
		t.Errorf("%s was not reported, so it should have gone with .opossum/, stat err = %v", doomed, err)
	}
}

// A container the runtime cannot be asked about after the teardown is reported
// as exactly that — not as gone (a clean sweep it cannot vouch for), not as
// still there (a survivor it did not see).
func TestDestroyReportsAContainerTheRuntimeCannotBeAskedAbout(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		"NETWORK_ABSENT=demo-net", "IMAGE_ABSENT=web", "VOLUME_LS=NAME")
	strictContainers(t, rt, "web.demo.opossum")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(plan.Containers) == 0 {
		t.Fatalf("the plan must include the container for the check to mean anything, got %+v", plan)
	}
	// The runtime stops answering once the removals have been issued.
	setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum")
	err = o.Destroy(plan)
	// Said as what it is — not counted among what "could not be removed", which
	// nobody knows.
	if want := "the runtime could not be asked whether 1 container(s) are gone: container web.demo.opossum — `container ls -a` shows them"; err == nil || err.Error() != want {
		t.Errorf("an unanswered inspect after the teardown:\n got %v\nwant %q", err, want)
	}
}

// Both kinds at once, each with more than one name: a network still there and
// two containers the runtime could not be asked about. The error carries both
// sentences whole — a join that kept one and dropped the other, or a list
// that named only its first entry, shows as a different line.
func TestDestroySaysBothWhatIsLeftAndWhatCouldNotBeAskedAbout(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		"IMAGE_ABSENT=web db", "VOLUME_LS=NAME", "NETWORK_DELETE_STICKY=demo-net")
	strictContainers(t, rt, "web.demo.opossum", "db.demo.opossum")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}, "db": {Image: "db"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	setShimEnv(rt, "INSPECT_FAIL=web.demo.opossum db.demo.opossum")
	err = o.Destroy(plan)
	want := "1 thing(s) could not be removed: network demo-net — run `opossum destroy` again (a container elsewhere may still be holding one), or remove them by hand; " +
		"and the runtime could not be asked whether 2 container(s) are gone: container db.demo.opossum and container web.demo.opossum — `container ls -a` shows them"
	if err == nil || err.Error() != want {
		t.Errorf("destroy error:\n got %v\nwant %q", err, want)
	}
}

// A failed `up` leaves alone a service that was there before, asks to be kept
// alive (`restart:`) and is running again because this `up` brought it back —
// as docker compose does (measured on v5.5.1: a stopped `restart: always` service
// is started during an `up` whose db never turns healthy, and is still running
// after). Tearing it down would leave it absent, with nothing for the supervisor
// to keep up, over a failure it had no part in. What is new in this call, and what
// has no `restart:`, is rolled back as before.
//
// cache has no depends_on and sorts ahead of db, so it is started before the health wait that
// fails.
func TestFailedUpKeepsARestartServiceItBroughtBack(t *testing.T) {
	for _, tc := range []struct {
		name        string
		restart     string
		stopBefore  bool // it was there and had stopped before the failing up
		stuck       bool // and is still not running after the run that replaced it
		changeDB    bool // db's configuration changed, so this up replaces it too
		wantKept    bool
		wantMessage string
	}{
		{"there before, stopped, with restart", "always", true, false, false, true, "cache is left running"},
		{"there before, stopped, with on-failure", "on-failure", true, false, false, true, "cache is left running"},
		// What else this up rolled back is still said, ahead of what it left.
		{"kept, with another service rolled back", "always", true, false, true, true, "Rolled back db"},
		{"kept, with another service rolled back, saying what is left", "always", true, false, true, true, "cache is left running"},
		// Each condition is its own row: dropping one must turn one red.
		{"there before, stopped, with no restart", "", true, false, false, false, "Rolled back"},
		{"there before, stopped, with restart: no", "no", true, false, false, false, "Rolled back"},
		{"new in this up, with restart", "always", false, false, false, false, "Rolled back"},
		{"there before, but not running after it was run", "always", true, true, false, false, "Rolled back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
			dbEnv := compose.Environment{"V=1"}
			svcs := func() map[string]*compose.Service {
				return map[string]*compose.Service{
					"db": {
						Image:       "db",
						Environment: dbEnv,
						Healthcheck: &compose.Healthcheck{
							Interval: time.Millisecond,
							Test:     []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
						},
					},
					"web":   {Image: "web", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}},
					"cache": {Image: "cache", Restart: tc.restart},
				}
			}
			var out bytes.Buffer
			o := orchestrator.New(project("demo", svcs()), rt, "opossum", &out)
			if tc.stopBefore {
				if err := o.Up(true); err != nil {
					t.Fatalf("first up: %v", err)
				}
				// Between the calls cache crashes: still there, no longer running.
				rt.Stop("cache.demo.opossum")
				o = orchestrator.New(project("demo", svcs()), rt, "opossum", &out)
			}
			out.Reset()
			setShimEnv(rt, "HEALTH_HANG=1")
			if tc.stuck {
				setShimEnv(rt, "INSPECT_STOPPED=cache.demo.opossum")
			}
			if tc.changeDB {
				dbEnv = compose.Environment{"V=2"}
				o = orchestrator.New(project("demo", svcs()), rt, "opossum", &out)
			}
			if err := o.Up(true); err == nil {
				t.Fatal("the up should fail")
			}
			var saw bool
			for _, n := range o.Started() {
				if n == "cache" {
					saw = true
				}
			}
			if saw != tc.wantKept {
				t.Errorf("cache in Started() = %v, want %v; got %v", saw, tc.wantKept, o.Started())
			}
			if !strings.Contains(out.String(), tc.wantMessage) {
				t.Errorf("the report should say %q, got:\n%s", tc.wantMessage, out.String())
			}
			// Kept means not stopped or deleted after the run that brought it back.
			lines := log()
			last := -1
			for i, l := range lines {
				if strings.HasPrefix(l, "run -d --name cache.demo.opossum") {
					last = i
				}
			}
			var tornDown bool
			if last >= 0 {
				for _, l := range lines[last+1:] {
					if l == "stop cache.demo.opossum" || l == "delete --force cache.demo.opossum" {
						tornDown = true
					}
				}
			}
			if tornDown == tc.wantKept {
				t.Errorf("cache torn down after it was run = %v, want %v:\n%s", tornDown, !tc.wantKept, strings.Join(lines, "\n"))
			}
		})
	}
}

// What a failed `up` leaves alone has to be something the supervisor will keep up.
// The mark `opossum stop` leaves makes the supervisor leave a service alone, and a
// successful `up` clears it; a service the failed one brought back running with the
// mark still on would be kept and never restarted.
func TestFailedUpClearsTheStopMarkOfAServiceItKeeps(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	svcs := func() map[string]*compose.Service {
		return map[string]*compose.Service{
			"db": {Image: "db", Healthcheck: &compose.Healthcheck{
				Interval: time.Millisecond, Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1}},
			"web":   {Image: "web", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}},
			"cache": {Image: "cache", Restart: "always"},
		}
	}
	o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := o.Stop([]string{"cache"}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !o.WasStoppedByUsForTest("cache") {
		t.Fatal("`stop` should leave its mark, or this test says nothing")
	}
	setShimEnv(rt, "HEALTH_HANG=1")
	o = orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatal("the up should fail")
	}
	if o.WasStoppedByUsForTest("cache") {
		t.Error("cache was left running by the failed up with the stop mark still on: the supervisor would leave it alone")
	}
}

// Who is left running is told by what keeps it up: not a foreground `up` or an
// interrupted one (everything this call started is to come down; a foreground one
// starts no supervisor), not a run-to-completion service, not a service whose
// dependency this call is rolling back, and a network a kept service is on stays.
func TestFailedUpDoesNotKeepWhatNothingWouldKeepUp(t *testing.T) {
	healthyDB := func() *compose.Service {
		return &compose.Service{Image: "db", Healthcheck: &compose.Healthcheck{
			Interval: time.Millisecond, Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1}}
	}
	tornDownAfterRun := func(lines []string, cname string) bool {
		last := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "run ") && strings.Contains(l, "--name "+cname) {
				last = i
			}
		}
		if last < 0 {
			return false
		}
		for _, l := range lines[last+1:] {
			if l == "stop "+cname || l == "delete --force "+cname {
				return true
			}
		}
		return false
	}

	t.Run("a Ctrl-C during a detached up", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"db":    healthyDB(),
				"web":   {Image: "web", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}},
				"cache": {Image: "cache", Restart: "always"},
			}
		}
		if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rt.Stop("cache.demo.opossum")
		setShimEnv(rt, "HEALTH_HANG=1")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.Up(true) }()
		// The second run of cache: the first up's is already in the log, and
		// cancelling before this one would interrupt an up that has done nothing.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && countLinePrefix(log(), "run -d --name cache.demo.opossum") < 2 {
			time.Sleep(2 * time.Millisecond)
		}
		if countLinePrefix(log(), "run -d --name cache.demo.opossum") < 2 {
			t.Fatalf("the second up never reached cache, got %v", log())
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the up should be interrupted")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the interrupted up never returned")
		}
		if !tornDownAfterRun(log(), "cache.demo.opossum") {
			t.Errorf("a Ctrl-C asks for what this call started to come down; cache was left:\n%s", strings.Join(log(), "\n"))
		}
	})

	t.Run("a foreground up that is interrupted", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func() map[string]*compose.Service {
			return map[string]*compose.Service{"web": {Image: "web", Restart: "always"}}
		}
		if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rt.Stop("web.demo.opossum")
		setShimEnv(rt, "RUN_HANG=web.demo.opossum")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.Up(false) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && !hasLinePrefixAfter(log(), "run --name web.demo.opossum") {
			time.Sleep(2 * time.Millisecond)
		}
		if !hasLinePrefixAfter(log(), "run --name web.demo.opossum") {
			t.Fatalf("the foreground run never started, got %v", log())
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the interrupted up never returned")
		}
		if !tornDownAfterRun(log(), "web.demo.opossum") {
			t.Errorf("an interrupted foreground up starts no supervisor and asks for everything to come down; web was left:\n%s", strings.Join(log(), "\n"))
		}
	})

	t.Run("a service whose dependency this call rolls back", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"base":  {Image: "base"},
				"cache": {Image: "cache", Restart: "always", DependsOn: compose.DependsOn{{Name: "base", Condition: compose.ConditionStarted}}},
				"zdb":   healthyDB(),
				"zweb":  {Image: "web", DependsOn: compose.DependsOn{{Name: "zdb", Condition: compose.ConditionHealthy}}},
			}
		}
		o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rt.Stop("base.demo.opossum")
		rt.Stop("cache.demo.opossum")
		setShimEnv(rt, "HEALTH_HANG=1")
		o = orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err == nil {
			t.Fatal("the up should fail")
		}
		if !tornDownAfterRun(log(), "base.demo.opossum") {
			t.Fatalf("base has no restart: and is rolled back, or this test says nothing:\n%s", strings.Join(log(), "\n"))
		}
		if !tornDownAfterRun(log(), "cache.demo.opossum") {
			t.Errorf("cache depends on base, which was rolled back: it must not be left running on nothing:\n%s", strings.Join(log(), "\n"))
		}
	})

	t.Run("a service whose dependency this call never touched", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"base":  {Image: "base"},
				"cache": {Image: "cache", Restart: "always", DependsOn: compose.DependsOn{{Name: "base", Condition: compose.ConditionStarted}}},
				"zdb":   healthyDB(),
				"zweb":  {Image: "web", DependsOn: compose.DependsOn{{Name: "zdb", Condition: compose.ConditionHealthy}}},
			}
		}
		if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		// Only cache stopped: base is still running and unchanged, so this call
		// leaves it up to date and does not touch it.
		rt.Stop("cache.demo.opossum")
		setShimEnv(rt, "HEALTH_HANG=1")
		var out bytes.Buffer
		if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &out).Up(true); err == nil {
			t.Fatal("the up should fail")
		}
		if !strings.Contains(out.String(), "base is up to date") {
			t.Fatalf("base should be left alone by this call, or this test says nothing:\n%s", out.String())
		}
		if tornDownAfterRun(log(), "cache.demo.opossum") {
			t.Errorf("cache depends on base, which this call did not create and did not roll back: cache stays:\n%s", strings.Join(log(), "\n"))
		}
	})

	t.Run("a service whose dependency is kept as well", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"base":  {Image: "base", Restart: "always"},
				"cache": {Image: "cache", Restart: "always", DependsOn: compose.DependsOn{{Name: "base", Condition: compose.ConditionStarted}}},
				"zdb":   healthyDB(),
				"zweb":  {Image: "web", DependsOn: compose.DependsOn{{Name: "zdb", Condition: compose.ConditionHealthy}}},
			}
		}
		if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rt.Stop("base.demo.opossum")
		rt.Stop("cache.demo.opossum")
		setShimEnv(rt, "HEALTH_HANG=1")
		o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err == nil {
			t.Fatal("the up should fail")
		}
		for _, c := range []string{"base.demo.opossum", "cache.demo.opossum"} {
			if tornDownAfterRun(log(), c) {
				t.Errorf("%s asks to be kept up and so does everything it depends on: both stay:\n%s", c, strings.Join(log(), "\n"))
			}
		}
	})

	t.Run("a service whose dependency is a finished run-to-completion one", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"migrate": {Image: "migrate"},
				"app": {Image: "app", Restart: "always",
					DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}}},
				"zdb":  healthyDB(),
				"zweb": {Image: "web", DependsOn: compose.DependsOn{{Name: "zdb", Condition: compose.ConditionHealthy}}},
			}
		}
		if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rt.Stop("app.demo.opossum")
		setShimEnv(rt, "HEALTH_HANG=1")
		o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err == nil {
			t.Fatal("the up should fail")
		}
		if tornDownAfterRun(log(), "app.demo.opossum") {
			t.Errorf("app only waited for migrate to finish, which loses it nothing: app stays:\n%s", strings.Join(log(), "\n"))
		}
	})

	t.Run("a network a kept service is on", func(t *testing.T) {
		rt, log := fakeShim(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
		svcs := func(cacheNets compose.ServiceNetworks) map[string]*compose.Service {
			return map[string]*compose.Service{
				"db":    healthyDB(),
				"web":   {Image: "web", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}},
				"cache": {Image: "cache", Restart: "always", Networks: cacheNets},
			}
		}
		if err := orchestrator.New(project("demo", svcs(nil)), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		rt.Stop("cache.demo.opossum")
		setShimEnv(rt, "HEALTH_HANG=1")
		p := project("demo", svcs(compose.ServiceNetworks{"back"}))
		p.Networks = map[string]compose.NetworkDecl{"back": {}}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err == nil {
			t.Fatal("the up should fail")
		}
		lines := log()
		if !hasLinePrefixAfter(lines, "network create demo-back") {
			t.Fatalf("this call should have created the network cache is moved to:\n%s", strings.Join(lines, "\n"))
		}
		if hasLinePrefixAfter(lines, "network delete demo-back") {
			t.Errorf("cache is left running on demo-back; the network stays:\n%s", strings.Join(lines, "\n"))
		}
	})
}

func countLinePrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func hasLinePrefixAfter(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// The report tells what was left on purpose apart from everything else: a service
// this call never touched (up to date) is not named as one it left running; two
// that were kept are named in the plural; and a rollback that could not finish
// still says what was kept.
func TestFailedUpReportsWhatItKept(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stopBefore []string // restart: services that stopped before the failing up
		env        []string
		changeDB   bool
		dbNew      bool // db and web are added by the failing up
		want       []string
		wantNot    []string
	}{
		{"an up-to-date restart service is not one it left running", nil, nil, true, false,
			[]string{"Rolled back db", "nothing this `up` started is left running"}, []string{"cache is left running", "cache are left running"}},
		{"two kept", []string{"cache", "cache2"}, nil, false, false,
			[]string{"cache, cache2 are left running — they were there before"}, []string{"is left running", "but the container is still there"}},
		{"one that could not be rolled back and one kept", []string{"cache"}, []string{"DELETE_STICKY=db.demo.opossum"}, false, true,
			[]string{"still there", "cache is left running"},
			// What was kept on purpose is not also reported as a rollback that did not
			// finish. Names are joined into one sentence ("db, cache, but the container
			// is still there"), so it is the end of the list that is looked for.
			[]string{"cache, but the container is still there"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
			dbEnv := compose.Environment{"V=1"}
			svcs := func(withDB bool) map[string]*compose.Service {
				m := map[string]*compose.Service{
					"cache":  {Image: "cache", Restart: "always"},
					"cache2": {Image: "cache", Restart: "always"},
				}
				if withDB {
					m["db"] = &compose.Service{Image: "db", Environment: dbEnv, Healthcheck: &compose.Healthcheck{
						Interval: time.Millisecond, Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1}}
					m["web"] = &compose.Service{Image: "web", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}}
				}
				return m
			}
			var out bytes.Buffer
			if err := orchestrator.New(project("demo", svcs(!tc.dbNew)), rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("first up: %v", err)
			}
			for _, n := range tc.stopBefore {
				rt.Stop(n + ".demo.opossum")
			}
			out.Reset()
			setShimEnv(rt, append([]string{"HEALTH_HANG=1"}, tc.env...)...)
			if tc.changeDB {
				dbEnv = compose.Environment{"V=2"}
			}
			if err := orchestrator.New(project("demo", svcs(true)), rt, "opossum", &out).Up(true); err == nil {
				t.Fatal("the up should fail")
			}
			for _, w := range tc.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("the report should say %q, got:\n%s", w, out.String())
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(out.String(), w) {
					t.Errorf("the report should not say %q, got:\n%s", w, out.String())
				}
			}
		})
	}
}

// A dry run changes nothing, so it does not clear the mark `opossum stop` left,
// even when it fails with a service that looks kept.
func TestFailedDryRunUpLeavesTheStopMarkAlone(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	svcs := func(withBuild bool) map[string]*compose.Service {
		m := map[string]*compose.Service{"cache": {Image: "cache", Restart: "always"}}
		if withBuild {
			m["zz"] = &compose.Service{Image: "zz", Build: &compose.Build{Context: "."}}
		}
		return m
	}
	o := orchestrator.New(project("demo", svcs(false)), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	o.MarkStopped("cache")
	setShimEnv(rt, "IMAGE_ABSENT=zz")
	o = orchestrator.New(project("demo", svcs(true)), rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(true, false, true, false, false)
	o.SetDryRun(true)
	if err := o.Up(true); err == nil {
		t.Fatal("the dry run should fail (zz is not built and --no-build was given)")
	}
	if !o.WasStoppedByUsForTest("cache") {
		t.Error("a dry run cleared the mark `opossum stop` left")
	}
}
