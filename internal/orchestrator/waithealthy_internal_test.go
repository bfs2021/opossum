package orchestrator

// Internal test (package orchestrator, not _test): it reaches the unexported
// `sleep` seam to observe the healthcheck timing without waiting in real time.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// probeShim is a `container` stand-in whose `exec` (the healthcheck) fails its first
// `fails` calls and passes after, counting the calls in the file it returns.
func probeShim(t *testing.T, fails int) (rt *runtime.Runtime, count func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "probes")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = exec ]; then\n  n=$(cat %[1]s 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]s\n  [ $n -gt %[2]d ]; exit\nfi\nexit 1\n", counter, fails)
	shim := filepath.Join(dir, "c.sh")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}, func() int {
		b, _ := os.ReadFile(counter)
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
}

// waitHealthy probes from the start, as docker compose does: a probe that passes is
// healthy at once, also inside the StartPeriod, which is a grace for failures only
// and is probed every five seconds (the interval, if shorter) while it lasts. Each
// row is a healthcheck, how many probes fail first and what a probe costs in time,
// and what the wait does: how many probes it makes, the sleeps between them, and
// whether it ends healthy. The clock is a fake one that moves with the sleeps and
// the probes, so the boundaries are exact. The old behaviour slept the whole
// StartPeriod first, so a service ready in seconds was waited on for a minute.
func TestWaitHealthyProbesFromTheStartAndStartPeriodIsAGraceForFailures(t *testing.T) {
	const s = time.Second
	rep := func(d time.Duration, n int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = d
		}
		return out
	}
	for _, tc := range []struct {
		name                  string
		startPeriod, interval time.Duration
		retries, fails        int
		probeCost             time.Duration
		wantProbes            int
		wantSleeps            []time.Duration
		wantHealthy           bool
	}{
		{"passes at once: no sleep, whatever the start period", 7 * s, 3 * s, 3, 0, 0, 1, nil, true},
		{"passes inside the start period after failing", 10 * s, 3 * s, 2, 3, 0, 4, rep(3*s, 3), true},
		// probes at 0, 3, 6, 9 s fail inside a 10 s grace and do not count; the one at 12 passes
		{"failures inside the grace do not count against retries", 10 * s, 3 * s, 2, 4, 0, 5, rep(3*s, 4), true},
		// grace 5 s: probes at 0 and 3 are free; 6 and 9 fail and are the two retries
		{"after the grace, retries failures in a row fail it", 5 * s, 3 * s, 2, 99, 0, 4, rep(3*s, 3), false},
		{"no start period: retries probes, as before", 0, 3 * s, 3, 99, 0, 3, rep(3*s, 2), false},
		{"no start period, passes on the second", 0, 3 * s, 3, 1, 0, 2, rep(3*s, 1), true},
		// the probe that fails exactly when the grace runs out is the first that counts
		{"the probe at the end of the grace counts", 6 * s, 3 * s, 2, 99, 0, 4, rep(3*s, 3), false},
		// an interval longer than the grace: inside the grace the probes still come every 5 s,
		// so a service ready after a few seconds is not waited on for the interval
		{"a long interval does not stretch the grace", 10 * s, 30 * s, 2, 1, 0, 2, rep(5*s, 1), true},
		{"a long interval, failing: the grace ends at its own end", 10 * s, 30 * s, 1, 99, 0, 3, rep(5*s, 2), false},
		// the five-second pace ends with the grace: after it the probes are an interval apart
		{"the interval comes back after the grace", 10 * s, 30 * s, 2, 99, 0, 4, []time.Duration{5 * s, 5 * s, 30 * s}, false},
		// no interval is docker's thirty seconds, not a loop without sleeps
		{"no interval is thirty seconds", 0, 0, 2, 99, 0, 2, rep(30*s, 1), false},
		{"no interval, with a grace", 12 * s, 0, 1, 99, 0, 4, rep(5*s, 3), false},
		// a probe that takes time uses the grace up as time does
		{"time spent in probes counts toward the grace", 10 * s, 3 * s, 1, 99, 4 * s, 2, rep(3*s, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, probes := probeShim(t, tc.fails)
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"db": {Image: "x"}}}
			o := New(p, rt, "opossum", io.Discard)
			var slept []time.Duration
			var asleep time.Duration
			o.sleep = func(d time.Duration) { slept = append(slept, d); asleep += d }
			epoch := time.Unix(1_000_000, 0)
			o.now = func() time.Time { return epoch.Add(asleep + time.Duration(probes())*tc.probeCost) }
			hc := &compose.Healthcheck{Test: []string{"true"}, StartPeriod: tc.startPeriod, Interval: tc.interval, Retries: tc.retries}
			err := o.waitHealthy("db", hc)
			if (err == nil) != tc.wantHealthy {
				t.Fatalf("healthy = %v, want %v (err %v)", err == nil, tc.wantHealthy, err)
			}
			if got := probes(); got != tc.wantProbes {
				t.Errorf("%d probes, want %d", got, tc.wantProbes)
			}
			if fmt.Sprint(slept) != fmt.Sprint(tc.wantSleeps) {
				t.Errorf("sleeps %v, want %v", slept, tc.wantSleeps)
			}
		})
	}
}
