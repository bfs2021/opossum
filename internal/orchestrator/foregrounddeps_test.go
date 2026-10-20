package orchestrator_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// webOnDB is `web` depending on `db`, the shape of the README's
// `opossum up web --foreground`.
func webOnDB() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16"},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionStarted}},
		},
	})
}

func runsOf(log []string, name string) int {
	return countLines(log, "--name "+name+".demo.opossum")
}

// A dependency that is already running is not started by this `up`, so it is not
// a second service to hold in the foreground: web attaches, db is left alone.
func TestUpForegroundLeavesARunningDependencyOut(t *testing.T) {
	rt, log := fakeShim(t)
	var out bytes.Buffer
	if err := orchestrator.New(webOnDB(), rt, "opossum", &out).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	if err := orchestrator.New(webOnDB(), rt, "opossum", &out).Up(false, "web"); err != nil {
		t.Fatalf("up web --foreground with db running: %v", err)
	}
	if n := runsOf(log(), "db"); n != 1 {
		t.Errorf("db was started %d times, want once (the first up)", n)
	}
	var web string
	for _, l := range log() {
		if strings.HasPrefix(l, "run") && strings.Contains(l, "--name web.demo.opossum") {
			web = l
		}
	}
	if web == "" || strings.HasPrefix(web, "run -d") {
		t.Errorf("web should run attached, got %q", web)
	}
	if !strings.Contains(out.String(), "db is up to date") {
		t.Errorf("db should be reported up to date, got:\n%s", out.String())
	}
}

// A dependency that is not running would be started too, so the foreground still
// cannot hold both; the message names only what would start.
func TestUpForegroundStillRefusesADependencyThatWouldStart(t *testing.T) {
	rt, _ := fakeShim(t)
	err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(false, "web")
	if err == nil || !strings.Contains(err.Error(), "2 would start (db, web)") {
		t.Errorf("want the refusal naming db and web, got %v", err)
	}
}

// Two of three services named: the one that is running is left out of the count,
// the two that are not still make two.
func TestUpForegroundCountsEveryServiceThatWouldStart(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionStarted}}},
		"api": {Image: "api:latest", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionStarted}}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false, "web", "api")
	if err == nil || !strings.Contains(err.Error(), "2 would start (api, web)") {
		t.Errorf("want the refusal naming api and web only, got %v", err)
	}
}

// With no service named, the foreground up is of the whole project: nothing is a
// dependency to leave out, and the one service there is is recreated to attach.
func TestUpForegroundWithNoNameStillRecreatesTheService(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := o.Up(false); err != nil {
		t.Fatalf("up --foreground: %v", err)
	}
	if n := runsOf(log(), "web"); n != 2 {
		t.Errorf("web was started %d times, want 2 (recreated to attach)", n)
	}
}

// A running dependency whose configuration the file has changed is recreated, as
// `up db` would, and detached: it is not a second service to attach to, and
// refusing would leave the person to run `up db` first for nothing.
func TestUpForegroundRecreatesAChangedRunningDependencyDetached(t *testing.T) {
	rt, log := fakeShim(t)
	if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	changed := webOnDB()
	changed.Services["db"].Environment = compose.Environment{"NEW=1"}
	if err := orchestrator.New(changed, rt, "opossum", &bytes.Buffer{}).Up(false, "web"); err != nil {
		t.Fatalf("up web --foreground with db changed: %v", err)
	}
	if n := countLines(log(), "run -d --name db.demo.opossum"); n != 2 {
		t.Errorf("db was run detached %d times, want 2 (the first up, and the recreate)", n)
	}
	if n := countLines(log(), "run --name web.demo.opossum"); n != 1 {
		t.Errorf("web was run attached %d times, want once", n)
	}
}

// --build on a dependency that builds recreates it too, detached, and does so
// without calling it a change of configuration: the file is as it was.
func TestUpForegroundBuildsARunningDependencyDetached(t *testing.T) {
	rt, log := fakeShim(t)
	p := webOnDB()
	p.Services["db"] = &compose.Service{Build: &compose.Build{Context: "/ctx"}}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, true, false, false, false) // --build
	if err := o.Up(false, "web"); err != nil {
		t.Fatalf("up web --foreground --build: %v", err)
	}
	if n := countLines(log(), "run -d --name db.demo.opossum"); n != 2 {
		t.Errorf("db was run detached %d times, want 2", n)
	}
	if n := countLines(log(), "run --name web.demo.opossum"); n != 1 {
		t.Errorf("web was run attached %d times, want once", n)
	}
}

// Dependencies running at once are all left out, wherever they sit in the order
// and however many there are.
func TestUpForegroundLeavesSeveralRunningDependenciesOut(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"cache": {Image: "redis:7"},
		"db":    {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{
			{Name: "cache", Condition: compose.ConditionStarted},
			{Name: "db", Condition: compose.ConditionStarted},
		}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true, "cache", "db"); err != nil {
		t.Fatalf("up cache db: %v", err)
	}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false, "web"); err != nil {
		t.Fatalf("up web --foreground with both running: %v", err)
	}
	for _, n := range []string{"cache", "db"} {
		if c := runsOf(log(), n); c != 1 {
			t.Errorf("%s was started %d times, want once", n, c)
		}
	}
	// One of the two stopped: it would be started, so there are two to attach to.
	setShimEnv(rt, "INSPECT_STOPPED=cache.demo.opossum")
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false, "web")
	if err == nil || !strings.Contains(err.Error(), "2 would start (cache, web)") {
		t.Errorf("want the refusal naming cache and web only, got %v", err)
	}
}

// --force-recreate means every service is recreated, so a running dependency is
// one of the services that would start.
func TestUpForegroundForceRecreateCountsTheRunningDependency(t *testing.T) {
	rt, _ := fakeShim(t)
	if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	o := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(true, false, false, false, false) // --force-recreate
	err := o.Up(false, "web")
	if err == nil || !strings.Contains(err.Error(), "2 would start (db, web)") {
		t.Errorf("want the refusal naming db and web, got %v", err)
	}
}

// The service named is the one to attach to, so it is recreated even when it is
// running and unchanged, while its running dependency is left as it is.
func TestUpForegroundRecreatesTheNamedServiceNotItsDependency(t *testing.T) {
	rt, log := fakeShim(t)
	for _, detached := range [][]string{{"db"}, {"web"}} {
		if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(true, detached...); err != nil {
			t.Fatalf("up %v: %v", detached, err)
		}
	}
	if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(false, "web"); err != nil {
		t.Fatalf("up web --foreground: %v", err)
	}
	if n := runsOf(log(), "web"); n != 2 {
		t.Errorf("web was started %d times, want 2 (recreated to attach)", n)
	}
	if n := runsOf(log(), "db"); n != 1 {
		t.Errorf("db was started %d times, want once", n)
	}
}

// A dependency that exists but is stopped would be started, so it counts.
func TestUpForegroundCountsAStoppedDependency(t *testing.T) {
	rt, _ := fakeShim(t)
	if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	setShimEnv(rt, "INSPECT_STOPPED=db.demo.opossum")
	err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(false, "web")
	if err == nil || !strings.Contains(err.Error(), "2 would start (db, web)") {
		t.Errorf("want the refusal naming db and web, got %v", err)
	}
}

// A one-shot dependency is run to completion each time, running or not: it is not
// a service left running, so a second foreground `up` is not refused over it.
// (The fake answers "running" for the container a foreground run made; the real
// CLI answers "stopped" for one that has exited, so this is the fake's way of
// reaching the exclusion, not a state a real one-shot is found in.)
func TestUpForegroundRunsAOneShotDependencyAgain(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"migrate": {Image: "alpine:3.20"},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}},
		},
	})
	for i := 1; i <= 2; i++ {
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false, "web"); err != nil {
			t.Fatalf("foreground up %d: %v", i, err)
		}
	}
	if n := countLines(log(), "--name migrate.demo.opossum"); n != 2 {
		t.Errorf("migrate was run %d times, want 2", n)
	}
}

// A dependency recreated detached fails as a detached run does: its words are the
// runtime's alone, so the port conflict is named for what it is, not passed on as
// the output of a container that was attached to.
func TestUpForegroundADependencyThatFailsToStartIsDiagnosedAsDetached(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	changed := webOnDB()
	changed.Services["db"].Environment = compose.Environment{"NEW=1"}
	changed.Services["db"].Ports = []string{"5432:5432"}
	setShimEnv(rt, "RUN_FAIL=db.demo.opossum", "RUN_FAIL_STDERR=Error: failed to run container: Address already in use\n",
		"INSPECT_ABSENT_BEFORE_RUN=db.demo.opossum")
	err := orchestrator.New(changed, rt, "opossum", &bytes.Buffer{}).Up(false, "web")
	if err == nil || !strings.Contains(err.Error(), "[OPSM-201]") {
		t.Errorf("want the detached run's port-conflict diagnosis, got %v", err)
	}
}

// recreatedDB brings up web's db, then returns the project with db's
// configuration changed (and, when net is set, put on a network of its own), so
// that `up web --foreground` recreates it.
func recreatedDB(t *testing.T, rt *runtime.Runtime, net bool) *compose.Project {
	t.Helper()
	if err := orchestrator.New(webOnDB(), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	changed := webOnDB()
	changed.Services["db"].Environment = compose.Environment{"NEW=1"}
	if net {
		changed.Services["db"].Networks = compose.ServiceNetworks{"back"}
		changed.Networks = map[string]compose.NetworkDecl{"back": {}}
	}
	return changed
}

// stoppedAfterTheRecreate lists the stop and delete commands for db that come after
// the detached run that recreated it: the recreate itself removes the old one first.
func stoppedAfterTheRecreate(log []string) []string {
	var out []string
	runs := 0
	for _, l := range log {
		if strings.HasPrefix(l, "run -d --name db.demo.opossum") {
			runs++
			continue
		}
		if runs >= 2 && strings.Contains(l, "db.demo.opossum") && (strings.HasPrefix(l, "stop") || strings.HasPrefix(l, "delete")) {
			out = append(out, l)
		}
	}
	return out
}

// The dependency this up recreated stays up when the service attached to fails:
// it is up as it is after `up db`, and not part of what a failed foreground
// start takes back. The report says so, and does not say nothing is left running.
func TestUpForegroundKeepsARecreatedDependencyWhenTheServiceFails(t *testing.T) {
	rt, log := fakeShim(t)
	changed := recreatedDB(t, rt, false)
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum")
	var out bytes.Buffer
	o := orchestrator.New(changed, rt, "opossum", &out)
	if err := o.Up(false, "web"); err == nil {
		t.Fatal("web's failure should fail the up")
	}
	if n := countLines(log(), "run -d --name db.demo.opossum"); n != 2 {
		t.Fatalf("db was run detached %d times, want 2:\n%s", n, strings.Join(log(), "\n"))
	}
	if after := stoppedAfterTheRecreate(log()); len(after) > 0 {
		t.Errorf("the recreated db was taken back after it was up: %v", after)
	}
	if got := out.String(); !strings.Contains(got, "Rolled back web") || strings.Contains(got, "Rolled back web, db") || strings.Contains(got, "Rolled back db") {
		t.Errorf("the rollback should name web alone:\n%s", got)
	}
	if got := out.String(); strings.Contains(got, "tried to roll back") {
		t.Errorf("db was not rolled back, so there is nothing it failed to take down:\n%s", got)
	}
	if got := out.String(); strings.Contains(got, "nothing this `up` started is left running") || !strings.Contains(got, "db is left running — recreated in the background by this `up`") {
		t.Errorf("the report should say db is left running:\n%s", got)
	}
	if got := o.Started(); len(got) != 1 || got[0] != "db" {
		t.Errorf("Started() = %v, want [db]: it is up and the failed web is not", got)
	}
}

// The network the recreated dependency is on is not deleted under it.
func TestUpForegroundKeepsTheNetworkOfARecreatedDependency(t *testing.T) {
	rt, log := fakeShim(t)
	changed := recreatedDB(t, rt, true)
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum")
	if err := orchestrator.New(changed, rt, "opossum", &bytes.Buffer{}).Up(false, "web"); err == nil {
		t.Fatal("web's failure should fail the up")
	}
	if !hasLine(log(), "network create demo-back") {
		t.Fatalf("the up should have created demo-back:\n%s", strings.Join(log(), "\n"))
	}
	for _, l := range log() {
		if strings.HasPrefix(l, "network delete") && strings.Contains(l, "demo-back") {
			t.Errorf("the network db is on was deleted: %s", l)
		}
	}
}

// Ctrl-C ends the attached service and is rolled back like a failure of it, with
// the same exception: what the up left running in the background stays.
func TestUpForegroundInterruptedKeepsARecreatedDependency(t *testing.T) {
	rt, log := fakeShim(t)
	changed := recreatedDB(t, rt, false)
	setShimEnv(rt, "RUN_HANG=web.demo.opossum")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	o := orchestrator.New(changed, rt, "opossum", &out)
	o.OnSignal(ctx)
	done := make(chan error, 1)
	go func() { done <- o.Up(false, "web") }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && indexOf(log(), "run --name web.demo.opossum") < 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if indexOf(log(), "run --name web.demo.opossum") < 0 {
		t.Fatalf("the foreground run never started:\n%s", strings.Join(log(), "\n"))
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Errorf("want the interrupted verdict, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("up did not return")
	}
	if after := stoppedAfterTheRecreate(log()); len(after) > 0 {
		t.Errorf("the recreated db was taken back after it was up: %v", after)
	}
	if got := o.Started(); len(got) != 1 || got[0] != "db" {
		t.Errorf("Started() = %v, want [db]", got)
	}
}

// --build recreates a dependency that builds, and it stays too: what is left
// running does not depend on why it was recreated.
func TestUpForegroundKeepsADependencyRebuiltForTheBuildFlag(t *testing.T) {
	rt, log := fakeShim(t)
	p := webOnDB()
	p.Services["db"] = &compose.Service{Build: &compose.Build{Context: "/ctx"}}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum")
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, true, false, false, false) // --build
	if err := o.Up(false, "web"); err == nil {
		t.Fatal("web's failure should fail the up")
	}
	if after := stoppedAfterTheRecreate(log()); len(after) > 0 {
		t.Errorf("the rebuilt db was taken back after it was up: %v", after)
	}
}

// A dependency whose recreate fails is not up, so it is rolled back as any
// service that failed to start is.
func TestUpForegroundRollsBackADependencyWhoseRecreateFailed(t *testing.T) {
	rt, log := fakeShim(t)
	changed := recreatedDB(t, rt, false)
	setShimEnv(rt, "RUN_FAIL=db.demo.opossum")
	var out bytes.Buffer
	o := orchestrator.New(changed, rt, "opossum", &out)
	if err := o.Up(false, "web"); err == nil {
		t.Fatal("db's failure should fail the up")
	}
	if got := out.String(); strings.Contains(got, "db is left running") {
		t.Errorf("a db that failed to start is not left running:\n%s", got)
	}
	if got := out.String(); !strings.Contains(got, "Rolled back db — stopped and removed") {
		t.Errorf("the rollback should say it took db back:\n%s", got)
	}
	var stopped, deleted bool
	for _, l := range log() {
		stopped = stopped || strings.HasPrefix(l, "stop db.demo.opossum")
		deleted = deleted || (strings.HasPrefix(l, "delete") && strings.HasSuffix(l, "db.demo.opossum"))
	}
	if !stopped || !deleted {
		t.Errorf("db should be stopped and deleted by the rollback (stop=%v delete=%v):\n%s", stopped, deleted, strings.Join(log(), "\n"))
	}
}

// The network a dependency's failed recreate made is taken back with it: nothing
// is running on it to keep it for.
func TestUpForegroundRollsBackTheNetworkOfADependencyWhoseRecreateFailed(t *testing.T) {
	rt, log := fakeShim(t)
	changed := recreatedDB(t, rt, true)
	setShimEnv(rt, "RUN_FAIL=db.demo.opossum")
	if err := orchestrator.New(changed, rt, "opossum", &bytes.Buffer{}).Up(false, "web"); err == nil {
		t.Fatal("db's failure should fail the up")
	}
	if !hasLine(log(), "network create demo-back") {
		t.Fatalf("the up should have created demo-back:\n%s", strings.Join(log(), "\n"))
	}
	if !hasLine(log(), "network delete demo-back") {
		t.Errorf("the network of the db that never came up should be deleted:\n%s", strings.Join(log(), "\n"))
	}
}

// A dependency that is up but whose own healthcheck then does not pass stays up,
// as it does under docker compose: the failure is not the foreground service's
// and not a start, and the report says it is left running. (A detached `up web`
// rolls the same dependency back; the two differ here.)
func TestUpForegroundKeepsARecreatedDependencyWhoseHealthcheckFails(t *testing.T) {
	rt, log := fakeShim(t)
	build := func(changed bool) *compose.Project {
		p := webOnDB()
		p.Services["db"].Healthcheck = &compose.Healthcheck{
			Interval: time.Millisecond, Test: []string{"pg_isready"}, Retries: 2, Timeout: time.Second,
		}
		p.Services["web"].DependsOn = compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}
		if changed {
			p.Services["db"].Environment = compose.Environment{"NEW=1"}
		}
		return p
	}
	if err := orchestrator.New(build(false), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	setShimEnv(rt, "HEALTH_OK_AT=100000") // db never reports healthy from here on
	var out bytes.Buffer
	if err := orchestrator.New(build(true), rt, "opossum", &out).Up(false, "web"); err == nil || !strings.Contains(err.Error(), "healthcheck") {
		t.Fatalf("the dependency's healthcheck should fail the up, got %v", err)
	}
	if after := stoppedAfterTheRecreate(log()); len(after) > 0 {
		t.Errorf("the recreated db was taken back after it was up: %v", after)
	}
	if got := out.String(); !strings.Contains(got, "db is left running — recreated in the background by this `up`") {
		t.Errorf("the report should say db is left running:\n%s", got)
	}
}

// Two recreated dependencies are named together, in the plural.
func TestUpForegroundNamesSeveralRecreatedDependencies(t *testing.T) {
	rt, _ := fakeShim(t)
	build := func(changed bool) *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"cache": {Image: "redis:7"},
			"db":    {Image: "postgres:16"},
			"web": {Image: "web:latest", DependsOn: compose.DependsOn{
				{Name: "cache", Condition: compose.ConditionStarted},
				{Name: "db", Condition: compose.ConditionStarted},
			}},
		})
		if changed {
			p.Services["cache"].Environment = compose.Environment{"NEW=1"}
			p.Services["db"].Environment = compose.Environment{"NEW=1"}
		}
		return p
	}
	if err := orchestrator.New(build(false), rt, "opossum", &bytes.Buffer{}).Up(true, "cache", "db"); err != nil {
		t.Fatalf("up cache db: %v", err)
	}
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum")
	var out bytes.Buffer
	if err := orchestrator.New(build(true), rt, "opossum", &out).Up(false, "web"); err == nil {
		t.Fatal("web's failure should fail the up")
	}
	if got := out.String(); !strings.Contains(got, "cache, db are left running — recreated in the background by this `up`") {
		t.Errorf("want both named, in the plural:\n%s", got)
	}
}

// A recreated dependency that asks to be kept up (`restart:`) is left running for
// one reason, and named once: a foreground `up` starts no supervisor, so
// `restart:` is not what keeps it up.
func TestUpForegroundNamesARecreatedDependencyWithRestartOnce(t *testing.T) {
	rt, _ := fakeShim(t)
	build := func(changed bool) *compose.Project {
		p := webOnDB()
		p.Services["db"].Restart = "always"
		if changed {
			p.Services["db"].Environment = compose.Environment{"NEW=1"}
		}
		return p
	}
	if err := orchestrator.New(build(false), rt, "opossum", &bytes.Buffer{}).Up(true, "db"); err != nil {
		t.Fatalf("up db: %v", err)
	}
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum")
	var out bytes.Buffer
	if err := orchestrator.New(build(true), rt, "opossum", &out).Up(false, "web"); err == nil {
		t.Fatal("web's failure should fail the up")
	}
	got := out.String()
	if n := strings.Count(got, "db is left running"); n != 1 {
		t.Errorf("db should be named once, got %d times:\n%s", n, got)
	}
	if strings.Contains(got, "`restart:` keeps it up") {
		t.Errorf("no supervisor runs under --foreground, so `restart:` is not why db is up:\n%s", got)
	}
}
