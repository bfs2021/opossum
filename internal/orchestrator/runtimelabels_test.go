package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A label the runtime refuses (no piece, or more than two, read as container 1.5.0 reads one — split at the first two `=`s, empty pieces left out — which answers `invalid label format`, measured) is left out of `container run`, named in a
// note, and the service starts; the labels it takes, and opossum's own, are passed. Each row holds one side of the rule.
func TestUpLeavesOutALabelTheRuntimeRefusesAndNamesIt(t *testing.T) {
	for _, tc := range []struct {
		label   string
		refused bool
	}{
		{"traefik.http.middlewares.a.forwardauth.address=http://authelia:9091/api/verify?rd=https://auth.example.com", true},
		{"a.b=x=y", true},
		{"a.b=x?y=z", true},
		{"a.b=/p?r=1", true},
		{"a.b=x=y=z", true},
		{"a.b=1=2", true},
		{"a=b=c", true},
		{"a.b=x =y", true},
		{"a.b= =y", true},
		{"a.b=x= ", true},
		{"a.b=é=ü", true},
		{"ラベル=値=二", true},
		{"a.b=x==y", true},
		{"a.b=x=y=", true},
		{"k=v==w", true},
		{"a.b=dGVzdA==", true},
		{"a.b=x==", true},
		{"a.b=x===", true},
		{"a.b=x====", true},
		{"=", true},
		{"==", true},
		{"===", true},
		{"a.b=https://y", false},
		{"a.b=x=", false},
		{"a.b==y", false},
		{"a.b===y", false},
		{"a.b====y", false},
		{"a===y", false},
		{"a.b==", false},
		{"a.b=", false},
		{"a.b", false},
		{"=x", false},
		{"==x", false},
		{"=x=y", false},
		{"=x=", false},
		{"a.b=x y", false},
		{"a===", false},
		{"x=y=", false},
		{"a.b=%3D=", false},
		{"a.b=dGVzdDE=", false},
		{"a.b=%3D", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"web": {Image: "web:latest", Labels: compose.Labels{tc.label, "keep.me=1"}},
			})
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			if err := o.Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			var run string
			for _, l := range log() {
				if strings.Contains(l, "run -d --name web.demo.opossum") {
					run = l
				}
			}
			if run == "" {
				t.Fatalf("no run for web: %v", log())
			}
			if got := strings.Contains(run, "--label="+tc.label); got == tc.refused {
				t.Errorf("label %q passed = %v, want %v: %s", tc.label, got, !tc.refused, run)
			}
			for _, own := range []string{"--label=keep.me=1", "--label=opossum.project=demo"} {
				if !strings.Contains(run, own) {
					t.Errorf("%s missing from: %s", own, run)
				}
			}
			if got := strings.Contains(out.String(), "web: label "); got != tc.refused {
				t.Errorf("note about the label = %v, want %v: %s", got, tc.refused, out.String())
			}
		})
	}
}

// `run` passes the same labels as `up`, so it leaves out the same one.
func TestRunLeavesOutALabelTheRuntimeRefusesAndNamesIt(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Labels: compose.Labels{"a.b=x=y", "keep.me=1"}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.RunOneOff("web", []string{"echo", "hi"}, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	var run string
	for _, l := range log() {
		if strings.Contains(l, "run ") && strings.Contains(l, "web:latest") {
			run = l
		}
	}
	if run == "" {
		t.Fatalf("no run: %v", log())
	}
	if strings.Contains(run, "a.b=x=y") || !strings.Contains(run, "--label=keep.me=1") {
		t.Errorf("run labels wrong: %s", run)
	}
	if !strings.Contains(out.String(), `web: label "a.b"`) {
		t.Errorf("no note naming the key: %s", out.String())
	}
}

// The note names the label by its key and leaves the value out: a value can be a token, and a log keeps what a note prints.
func TestTheNoteOfALeftOutLabelDoesNotPrintItsValue(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Labels: compose.Labels{"auth.token=s3cr3t=more"}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !strings.Contains(out.String(), `web: label "auth.token"`) || strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("the note should name auth.token and not print its value: %s", out.String())
	}
}
