package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// A service that did not start has no running container to look up: when verifyStarted fails the up, nothing is said about
// names (OPSM-216 says "though the service is running"). The same project with the service running is the control.
func TestNoHostNameNoteWhenTheStartedServiceDidNotStay(t *testing.T) {
	const system = "  system) if [ \"$2\" = dns ]; then echo DOMAIN; echo opossum; else echo 'status running'; fi ;;\n"
	run := func(t *testing.T, inspect string) (asked int, out string, err error) {
		t.Helper()
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		shim := scriptShim(t, system+"  inspect) echo '"+inspect+"' ;;\n  logs) echo boom ;;\n")
		var buf bytes.Buffer
		o := New(webProject(), shim, "opossum", &buf)
		o.crashGrace = time.Millisecond
		o.SetHostNameCheck(func(string) bool { asked++; return false })
		err = o.Up(true)
		return asked, buf.String(), err
	}
	t.Run("a service that is running is asked about (the control)", func(t *testing.T) {
		asked, out, err := run(t, runningInspect)
		if err != nil {
			t.Fatalf("Up: %v\n%s", err, out)
		}
		if asked != 1 || !strings.Contains(out, "[OPSM-216]") {
			t.Errorf("the control should ask once and note; asked %d, printed:\n%s", asked, out)
		}
	})
	t.Run("a service that exited right after starting is not", func(t *testing.T) {
		asked, out, err := run(t, stoppedInspect)
		if err == nil {
			t.Fatalf("a service that exited should fail the up, got nil\n%s", out)
		}
		if asked != 0 || strings.Contains(out, "[OPSM-216]") {
			t.Errorf("a failed up says nothing about names; asked %d, printed:\n%s", asked, out)
		}
	})
}

// After a Ctrl-C nothing is asked: the second verifyStarted waits is skipped, so this would be the first look at a name
// registered a moment ago, and the person is not reading a note.
func TestNoHostNameAskedAfterAnInterrupt(t *testing.T) {
	shim := scriptShim(t, "  system) if [ \"$2\" = dns ]; then echo DOMAIN; echo opossum; else echo 'status running'; fi ;;\n")
	for _, c := range []struct {
		name      string
		interrupt bool
		want      int
	}{{"not interrupted (the control)", false, 1}, {"interrupted", true, 0}} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := New(webProject(), shim, "opossum", &buf)
			asked := 0
			o.SetHostNameCheck(func(string) bool { asked++; return true })
			if c.interrupt {
				// Only the orchestrator's own context: OnSignal also hands it to the runtime, whose commands then all fail and
				// the DNS domain would read as unknown, which would stop the ask for another reason.
				ctx, cancel := context.WithCancel(context.Background())
				o.ctx = ctx
				cancel()
			}
			o.noteHostNames([]string{"web"}, nil)
			if asked != c.want {
				t.Errorf("asked %d, want %d", asked, c.want)
			}
		})
	}
}
