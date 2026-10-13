package orchestrator

import (
	"io"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// An entry is told apart by what it changes. The volume it names is opossum's
// choice and moves (`db-data`, `db-data-2`) when another is already taken, so a
// run that picks a different name still finds the entry it wrote before.
func TestAnEntryKeyIsWhatItChangesNotTheNameOpossumChose(t *testing.T) {
	mk := func(vol, target string) serviceAdaptation {
		return serviceAdaptation{
			Adaptation: Adaptation{Service: "db", Code: string(codeBindDataDirChown)},
			block:      "volumes",
			entries:    []string{"      - " + vol + ":" + target},
		}
	}
	for _, tc := range []struct {
		name string
		a, b serviceAdaptation
		same bool
	}{
		{"same mount, another volume name", mk("db-data", "/var/lib/mysql"), mk("db-data-2", "/var/lib/mysql"), true},
		{"another mount", mk("db-data", "/var/lib/mysql"), mk("db-data", "/data"), false},
		{"another service", mk("db-data", "/data"), func() serviceAdaptation { x := mk("db-data", "/data"); x.Service = "web"; return x }(), false},
		{"another code", mk("db-data", "/data"), func() serviceAdaptation { x := mk("db-data", "/data"); x.Code = "OPSM-101"; return x }(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := entryKey(tc.a) == entryKey(tc.b); got != tc.same {
				t.Errorf("same=%v want %v: %q vs %q", got, tc.same, entryKey(tc.a), entryKey(tc.b))
			}
		})
	}
}

// What was written is remembered across runs, only for entries that were applied,
// and an unreadable record is the same as none.
func TestWrittenKeysAreRememberedAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	o := New(&compose.Project{Name: "demo", BaseDir: dir}, nil, "opossum", io.Discard)
	if _, known := o.writtenKeys(); known {
		t.Fatal("nothing has been recorded yet")
	}
	o.RecordWrittenKeys([]Adaptation{{Kind: "applied", Key: "a"}, {Kind: "suggestion", Key: "s"}})
	o.RecordWrittenKeys([]Adaptation{{Kind: "applied", Key: "b"}})
	got, known := o.writtenKeys()
	if !known || !got["a"] || !got["b"] || got["s"] {
		t.Errorf("both applied keys and no suggestion, got %v (known=%v)", got, known)
	}
}
