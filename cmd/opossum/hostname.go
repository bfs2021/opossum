package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// hostNameCheck is how `up` asks whether this Mac can look up the name of a service it started (OPSM-216). It is nil where
// the answer would say nothing about the runtime's names: off macOS, where `dscacheutil` is not, and with OPOSSUM_CONTAINER_BIN
// set, where the runtime is a stand-in that registers no name with the host's resolver.
// A variable so a test can see that `up` is given the check.
var hostNameCheck = func() func(name string) bool { return hostNameCheckOn(runtime.GOOS) }

func hostNameCheckOn(goos string) func(name string) bool {
	if goos != "darwin" || os.Getenv("OPOSSUM_CONTAINER_BIN") != "" {
		return nil
	}
	return hostCanLookUp
}

// hostCanLookUp asks the resolver the host's own programs use, through macOS's cache: `dscacheutil -q host` prints an
// `ip_address:` or `ipv6_address:` line for a name it finds and nothing for one it does not (rc 0 either way), which is the
// answer a browser or `curl` would get, negative answers a resolver is keeping included. A lookup that cannot be made counts
// as found: the note is for a name the resolver says is absent, not for a tool that could not be run.
func hostCanLookUp(name string) bool {
	out, err := dscacheutil(name)
	if err != nil {
		return true
	}
	return hostLookupFound(string(out))
}

// dscacheutil runs the lookup; a variable so a test can answer for it.
var dscacheutil = func(name string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "dscacheutil", "-q", "host", "-a", "name", name).Output()
}

// hostLookupFound reads what `dscacheutil -q host -a name <n>` printed.
func hostLookupFound(out string) bool {
	return strings.Contains(out, "ip_address:") || strings.Contains(out, "ipv6_address:")
}
