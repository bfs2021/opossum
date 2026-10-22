package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A detached `up` says which of the services it started cannot be looked up by name on this host (OPSM-216, #2146): the runtime
// answers a name it does not know yet with NXDOMAIN and no SOA, macOS keeps that answer for about 75 seconds, and a peer that
// asked for the name before it was registered leaves it behind. The services are up, so it is a note and `up` returns nil.
func TestUpNotesAServiceNameTheHostCannotLookUp(t *testing.T) {
	const code = "[OPSM-216]"
	// run brings a project up with a host check that answers by name from `resolves` (a name not in the map does not resolve), and
	// returns what was asked, in order, and what `up` printed.
	registered := "DNS_DOMAINS=opossum"
	run := func(t *testing.T, svcs map[string]*compose.Service, resolves map[string]int, detach, dry bool, extraEnv ...string) (asked []string, out string, err error) {
		t.Helper()
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		rt.Env = append(rt.Env, extraEnv...)
		var buf bytes.Buffer
		o := orchestrator.New(project("demo", svcs), rt, "opossum", &buf)
		o.SetDryRun(dry)
		seen := map[string]int{}
		o.SetHostNameCheck(func(name string) bool {
			asked = append(asked, name)
			seen[name]++
			after, ok := resolves[name]
			return ok && seen[name] > after
		})
		err = o.Up(detach)
		return asked, buf.String(), err
	}
	two := func() map[string]*compose.Service {
		return map[string]*compose.Service{"db": {Image: "alpine:3.20"}, "web": {Image: "alpine:3.20"}}
	}

	t.Run("nothing is said when every name resolves", func(t *testing.T) {
		asked, out, err := run(t, two(), map[string]int{"db.demo.opossum": 0, "web.demo.opossum": 0}, true, false, registered)
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if strings.Contains(out, code) {
			t.Errorf("every name resolves, so nothing should be noted, got:\n%s", out)
		}
		if len(asked) != 2 {
			t.Errorf("each service's name is asked once when it resolves, got %v", asked)
		}
	})
	t.Run("a name that does not resolve is noted by its full name, and up still succeeds", func(t *testing.T) {
		_, out, err := run(t, two(), map[string]int{"web.demo.opossum": 0}, true, false, registered)
		if err != nil {
			t.Fatalf("a note must not fail the up: %v", err)
		}
		if strings.Count(out, code) != 1 || !strings.Contains(out, "db.demo.opossum") {
			t.Errorf("want one %s note naming db.demo.opossum, got:\n%s", code, out)
		}
	})
	t.Run("every service whose name does not resolve is named, in one note", func(t *testing.T) {
		_, out, _ := run(t, two(), nil, true, false, registered)
		if strings.Count(out, code) != 1 || !strings.Contains(out, "db.demo.opossum") || !strings.Contains(out, "web.demo.opossum") {
			t.Errorf("want one %s note naming both db.demo.opossum and web.demo.opossum, got:\n%s", code, out)
		}
	})
	t.Run("only the services whose names do not resolve are named", func(t *testing.T) {
		_, out, _ := run(t, two(), map[string]int{"web.demo.opossum": 0}, true, false, registered)
		if strings.Contains(out, "web.demo.opossum") {
			t.Errorf("web resolves and must not be named, got:\n%s", out)
		}
	})
	t.Run("a name that does not resolve is asked once, not again", func(t *testing.T) {
		asked, out, _ := run(t, map[string]*compose.Service{"db": {Image: "alpine:3.20"}}, nil, true, false, registered)
		if n := countNames(asked, "db.demo.opossum"); n != 1 {
			t.Errorf("want one ask (a second would get the answer macOS is keeping, or leave one behind), got %d: %v", n, asked)
		}
		if !strings.Contains(out, code) {
			t.Errorf("the name never resolved, so it should be noted, got:\n%s", out)
		}
	})
	t.Run("nothing is asked where the DNS domain is not registered on the host", func(t *testing.T) {
		asked, out, err := run(t, two(), nil, true, false)
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if len(asked) != 0 || strings.Contains(out, code) {
			t.Errorf("without the domain no name resolves and OPSM-202 says so; asked %v, printed:\n%s", asked, out)
		}
		if !strings.Contains(out, "OPSM-202") {
			t.Errorf("the fixture should be the one where the domain is absent (OPSM-202), got:\n%s", out)
		}
	})
	t.Run("nothing is asked where the DNS domains could not be listed", func(t *testing.T) {
		asked, out, _ := run(t, two(), nil, true, false, "DNS_LIST_FAIL=1")
		if len(asked) != 0 || strings.Contains(out, code) {
			t.Errorf("a listing that failed does not say the domain is there; asked %v, printed:\n%s", asked, out)
		}
	})
	t.Run("a name with upper case or a dot is not asked about", func(t *testing.T) {
		svcs := map[string]*compose.Service{"Web": {Image: "alpine:3.20"}, "a.b": {Image: "alpine:3.20"}, "db": {Image: "alpine:3.20"}}
		asked, _, _ := run(t, svcs, map[string]int{"db.demo.opossum": 0}, true, false, registered)
		if len(asked) != 1 || asked[0] != "db.demo.opossum" {
			t.Errorf("only db has a name this note is about (the others are OPSM-209's), asked %v", asked)
		}
	})
	t.Run("a run-to-completion service is not asked about", func(t *testing.T) {
		svcs := map[string]*compose.Service{
			"job": {Image: "alpine:3.20"},
			"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "job", Condition: "service_completed_successfully"}}},
		}
		asked, _, _ := run(t, svcs, map[string]int{"web.demo.opossum": 0}, true, false, registered)
		if countNames(asked, "job.demo.opossum") != 0 {
			t.Errorf("job exits when it is done and has no name to look up, but it was asked: %v", asked)
		}
	})
	t.Run("a service on network_mode none is not asked about", func(t *testing.T) {
		svcs := map[string]*compose.Service{
			"iso": {Image: "alpine:3.20", NetworkMode: compose.NetworkModeNone},
			"web": {Image: "alpine:3.20"},
		}
		asked, out, _ := run(t, svcs, map[string]int{"web.demo.opossum": 0}, true, false, registered)
		if countNames(asked, "iso.demo.opossum") != 0 || strings.Contains(out, code) {
			t.Errorf("a service with no network has no name to look up; asked %v, printed:\n%s", asked, out)
		}
	})
	t.Run("a dry run asks nothing", func(t *testing.T) {
		asked, out, _ := run(t, two(), nil, true, true, registered)
		if len(asked) != 0 || strings.Contains(out, code) {
			t.Errorf("a dry run starts nothing; asked %v, printed:\n%s", asked, out)
		}
	})
	t.Run("a foreground up asks nothing", func(t *testing.T) {
		asked, out, _ := run(t, map[string]*compose.Service{"db": {Image: "alpine:3.20"}}, nil, false, false, registered)
		if len(asked) != 0 || strings.Contains(out, code) {
			t.Errorf("a foreground up is watched by its caller; asked %v, printed:\n%s", asked, out)
		}
	})
	t.Run("with no check set nothing is said", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		rt.Env = append(rt.Env, registered)
		var buf bytes.Buffer
		o := orchestrator.New(project("demo", two()), rt, "opossum", &buf)
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if strings.Contains(buf.String(), code) {
			t.Errorf("no check was set, got:\n%s", buf.String())
		}
	})
	t.Run("the DNS domain asked about is the one the project uses", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		rt.Env = append(rt.Env, "DNS_DOMAINS=test")
		var buf bytes.Buffer
		o := orchestrator.New(project("demo", two()), rt, "test", &buf)
		var asked []string
		o.SetHostNameCheck(func(name string) bool { asked = append(asked, name); return true })
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if len(asked) != 2 || asked[0] != "db.demo.test" {
			t.Errorf("the domain is registered as \"test\", so the names asked are under it; asked %v\n%s", asked, buf.String())
		}
	})
	t.Run("with no DNS domain there is no name to ask", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		var buf bytes.Buffer
		o := orchestrator.New(project("demo", two()), rt, "", &buf)
		asked := 0
		o.SetHostNameCheck(func(string) bool { asked++; return false })
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if asked != 0 || strings.Contains(buf.String(), code) {
			t.Errorf("no DNS domain, so no names; asked %d, printed:\n%s", asked, buf.String())
		}
	})
}

func countNames(asked []string, name string) int {
	n := 0
	for _, a := range asked {
		if a == name {
			n++
		}
	}
	return n
}
