package main

import (
	"errors"
	"strings"
	"testing"
)

// What `dscacheutil -q host -a name <n>` prints is read for an address line; a name the resolver does not know prints nothing
// at all (rc 0), which is the answer OPSM-216 is about. The outputs are copied from a Mac (2026-10-22, macOS 26).
func TestHostLookupFoundReadsWhatDscacheutilPrints(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      bool
	}{
		{"an IPv4 address", "name: db.demo.opossum\nip_address: 192.168.66.3\n\n", true},
		{"an IPv6 address only", "name: localhost\nipv6_address: ::1\n\n", true},
		{"both", "name: localhost\nipv6_address: ::1\n\nname: localhost\nip_address: 127.0.0.1\n\n", true},
		{"nothing printed", "", false},
		{"a name line with no address", "name: db.demo.opossum\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hostLookupFound(c.out); got != c.want {
				t.Errorf("hostLookupFound(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

// With OPOSSUM_CONTAINER_BIN set the runtime is a stand-in that registers no name with the host's resolver, so the host's answer
// says nothing about it and no check is made.
func TestNoHostNameCheckForAStandInRuntime(t *testing.T) {
	t.Setenv("OPOSSUM_CONTAINER_BIN", "/tmp/fake-container")
	if hostNameCheckOn("darwin") != nil {
		t.Error("a stand-in runtime registers no name with the host, so no check should be set")
	}
	t.Setenv("OPOSSUM_CONTAINER_BIN", "")
	if hostNameCheckOn("darwin") == nil {
		t.Error("the real runtime on macOS should check, so the stand-in case above is told apart by the variable")
	}
}

// The check is made on macOS only, and not against a stand-in runtime.
func TestHostNameCheckIsSetOnMacOSWithTheRealRuntimeOnly(t *testing.T) {
	t.Setenv("OPOSSUM_CONTAINER_BIN", "")
	if hostNameCheckOn("darwin") == nil {
		t.Error("macOS with the real runtime should check")
	}
	if hostNameCheckOn("linux") != nil {
		t.Error("`dscacheutil` is macOS's, so another OS should not check")
	}
}

// A lookup that cannot be made is not a missing name: the note is for a name the resolver says is absent.
func TestHostCanLookUpAnswersFromDscacheutil(t *testing.T) {
	old := dscacheutil
	t.Cleanup(func() { dscacheutil = old })
	for _, c := range []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"an address", "name: x\nip_address: 10.0.0.2\n", nil, true},
		{"no answer", "", nil, false},
		{"the tool could not be run", "", errors.New("exec: not found"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dscacheutil = func(string) ([]byte, error) { return []byte(c.out), c.err }
			if got := hostCanLookUp("db.demo.opossum"); got != c.want {
				t.Errorf("hostCanLookUp = %v, want %v", got, c.want)
			}
		})
	}
}

// `up` is given the check: with a host that finds no name, the note is printed, and a host that finds it prints none.
func TestUpIsGivenTheHostNameCheck(t *testing.T) {
	old := hostNameCheck
	t.Cleanup(func() { hostNameCheck = old })
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeShim(t)
	cf := writeCompose(t, "name: demo\nservices:\n  web:\n    image: alpine:3.20\n")
	for _, c := range []struct {
		name  string
		found bool
		note  bool
	}{{"no name is found", false, true}, {"every name is found", true, false}} {
		t.Run(c.name, func(t *testing.T) {
			asked := 0
			hostNameCheck = func() func(string) bool { return func(string) bool { asked++; return c.found } }
			out, err := run(t, "-f", cf, "up", "--no-build", "--no-supervisor")
			if err != nil {
				t.Fatalf("up: %v\n%s", err, out)
			}
			if asked != 1 {
				t.Errorf("the check should be asked once for the one service, got %d\n%s", asked, out)
			}
			if got := strings.Contains(out, "[OPSM-216]"); got != c.note {
				t.Errorf("note printed = %v, want %v\n%s", got, c.note, out)
			}
		})
	}
}
