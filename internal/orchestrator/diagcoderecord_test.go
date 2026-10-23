package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// diagCodeRecord records, for every diagnostic code, its number, the constant that carries it and the situation it is
// shown in (the constant's trailing comment), as `OPSM-NNN constName: situation`. A line that is in it and no longer in
// diagnostics.go is a number removed, or one given to another constant or another situation, which a 1.x release does
// not do. A line the source has and the record lacks is a new code, which is allowed and only noted.
// OPOSSUM_UPDATE_DIAG_CODES=1 adds the new lines to the file. It does not take a line out: that is made by editing the
// file, so that it shows in the diff.
const diagCodeRecord = "testdata/diag-codes.txt"

// diagCodeLines reads the `diagCode` constants of diagnostics.go as record lines, sorted.
func diagCodeLines(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "diagnostics.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "diagCode" {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				t.Fatalf("%s is not a string literal", vs.Names[0].Name)
			}
			num, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			scene := ""
			if vs.Comment != nil {
				scene = strings.TrimSpace(vs.Comment.Text())
			}
			lines = append(lines, num+" "+vs.Names[0].Name+": "+scene)
		}
	}
	sort.Strings(lines)
	return lines
}

// mergedDiagCodes is the record with the source's new lines added, sorted, and the lines the source no longer has
// still in it: recording a source that lost a code would otherwise pass the loss on as the new record.
func mergedDiagCodes(recorded, got []string) (merged, added []string) {
	known := map[string]bool{}
	for _, l := range recorded {
		known[l] = true
	}
	for _, l := range got {
		if !known[l] {
			added = append(added, l)
		}
	}
	merged = append(append([]string(nil), recorded...), added...)
	sort.Strings(merged)
	return merged, added
}

func nonBlankLines(raw string) []string {
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// recordDiagCodes writes the merge to path, and nothing when there is nothing new.
func recordDiagCodes(path string, recorded, got []string) error {
	merged, added := mergedDiagCodes(recorded, got)
	if len(added) == 0 {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(merged, "\n")+"\n"), 0o644)
}

func TestDiagCodesAreTheRecordedOnes(t *testing.T) {
	got := diagCodeLines(t)
	raw, err := os.ReadFile(diagCodeRecord)
	if err != nil {
		t.Fatalf("the recorded diagnostic codes: %v", err)
	}
	have := map[string]bool{}
	for _, l := range got {
		have[l] = true
	}
	recorded := nonBlankLines(string(raw))
	for _, l := range recorded {
		if !have[l] {
			t.Errorf("%q was recorded and is not in diagnostics.go: a number removed, or given to another constant or another situation, breaks a script that matches it", l)
		}
	}
	_, added := mergedDiagCodes(recorded, got)
	for _, l := range added {
		t.Logf("new, not yet recorded (run with OPOSSUM_UPDATE_DIAG_CODES=1 to record it): %s", l)
	}
	if os.Getenv("OPOSSUM_UPDATE_DIAG_CODES") == "1" {
		if err := recordDiagCodes(diagCodeRecord, recorded, got); err != nil {
			t.Fatal(err)
		}
	}
}

// The record is read from the source, so a check that read nothing would pass anything: it holds one code per
// constant, and the lines carry number, name and situation.
func TestDiagCodeRecordHoldsWhatItIsMeantToHold(t *testing.T) {
	lines := diagCodeLines(t)
	if len(lines) < len(allDiagCodes) {
		t.Errorf("read %d codes, allDiagCodes lists %d", len(lines), len(allDiagCodes))
	}
	joined := "\n" + strings.Join(lines, "\n")
	for _, want := range []string{
		"\nOPSM-101 codePGDATADatadir: named volume mounted directly",
		"\nOPSM-216 codeHostNameNotYet: a running service's name cannot be looked up",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the lines lack %q", want)
		}
	}
}

// Recording adds what is new and drops nothing; and the file written is the merge, not the source alone.
func TestRecordingTheDiagCodesDropsNoLine(t *testing.T) {
	recorded := []string{"OPSM-101 a: x", "OPSM-102 b: y"}
	got := []string{"OPSM-101 a: x", "OPSM-103 c: z"} // 102 gone, 103 new
	merged, added := mergedDiagCodes(recorded, got)
	if want := []string{"OPSM-103 c: z"}; !reflect.DeepEqual(added, want) {
		t.Errorf("added = %v, want %v", added, want)
	}
	if want := []string{"OPSM-101 a: x", "OPSM-102 b: y", "OPSM-103 c: z"}; !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v: the code the source lost must stay in the record", merged, want)
	}
	path := t.TempDir() + "/codes.txt"
	if err := recordDiagCodes(path, recorded, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if want := "OPSM-101 a: x\nOPSM-102 b: y\nOPSM-103 c: z\n"; string(raw) != want {
		t.Errorf("the file holds %q, want %q", raw, want)
	}
	// Nothing new: the file is not written (it is absent, and stays so).
	missing := t.TempDir() + "/none.txt"
	if err := recordDiagCodes(missing, recorded, recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Errorf("a file was written when there was nothing new")
	}
}
