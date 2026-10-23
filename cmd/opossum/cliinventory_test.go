package main

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// inventoryFile records the commands and flags a script may write today. A line that is in it and no longer in the CLI
// is a command, alias or flag removed, renamed, retyped or given another default, which a 1.x release does not do; a
// line the CLI has and the file lacks is a new one, which is allowed and only noted. OPOSSUM_UPDATE_INVENTORY=1 adds the
// new lines to the file. It does not take a line out: a removal is made by editing the file, so that it shows in the diff.
const inventoryFile = "testdata/cli-inventory.txt"

// cliInventory lists, for every visible command, the command's path, each alias it answers to as `<path> alias <name>`,
// and each flag it defines, its own and the persistent ones, as `<path> --name[ -s] type = default`, one per line,
// sorted. The root's `--version` is added by cobra when the command runs, so it is asked for here.
func cliInventory(root *cobra.Command) []string {
	root.InitDefaultVersionFlag()
	var lines []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden {
			return
		}
		path := c.CommandPath()
		lines = append(lines, path)
		for _, a := range c.Aliases {
			lines = append(lines, path+" alias "+a)
		}
		// The flags a command defines, its own and the persistent ones it passes down (recorded once, where defined).
		record := func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			short := ""
			if f.Shorthand != "" {
				short = " -" + f.Shorthand
			}
			lines = append(lines, path+" --"+f.Name+short+" "+f.Value.Type()+" = "+f.DefValue)
		}
		c.LocalNonPersistentFlags().VisitAll(record)
		c.PersistentFlags().VisitAll(record)
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			walk(sub)
		}
	}
	walk(root)
	sort.Strings(lines)
	return lines
}

// recordedLines are the non-blank lines of the recording.
func recordedLines(raw string) []string {
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// mergedInventory is the recording with the CLI's new lines added, sorted, and the lines the CLI no longer has still
// in it: recording a CLI that lost a line would otherwise pass the loss on as the new record.
func mergedInventory(recorded, got []string) (merged, added []string) {
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

func TestCLICommandsAndFlagsAreTheRecordedOnes(t *testing.T) {
	got := cliInventory(newRootCmd())
	raw, err := os.ReadFile(inventoryFile)
	if err != nil {
		t.Fatalf("the recorded commands and flags: %v", err)
	}
	have := map[string]bool{}
	for _, l := range got {
		have[l] = true
	}
	recorded := recordedLines(string(raw))
	for _, l := range recorded {
		if !have[l] {
			t.Errorf("%q was in the recorded commands and flags and is not in the CLI: a removed, renamed, retyped or re-defaulted command, alias or flag breaks a script written for an earlier 1.x", l)
		}
	}
	_, added := mergedInventory(recorded, got)
	for _, l := range added {
		t.Logf("new, not yet recorded (run with OPOSSUM_UPDATE_INVENTORY=1 to record it): %s", l)
	}
	if os.Getenv("OPOSSUM_UPDATE_INVENTORY") == "1" {
		if err := recordInventory(inventoryFile, recorded, got); err != nil {
			t.Fatal(err)
		}
	}
}

// recordInventory writes the recording at path: what was recorded and the CLI's new lines, and nothing else. It writes
// nothing when there is nothing new.
func recordInventory(path string, recorded, got []string) error {
	merged, added := mergedInventory(recorded, got)
	if len(added) == 0 {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(merged, "\n")+"\n"), 0o644)
}

// Recording adds what is new and drops nothing. The run above fails for a line the CLI lost; if the update then
// rewrote the file from the CLI, running it once would make the loss the record and the next run green.
func TestRecordingTheInventoryDropsNoLine(t *testing.T) {
	recorded := []string{"opossum", "opossum destroy", "opossum --verbose bool = false"}
	got := []string{"opossum", "opossum --verbose bool = false", "opossum --quiet bool = false"} // destroy gone, --quiet new
	merged, added := mergedInventory(recorded, got)
	if want := []string{"opossum --quiet bool = false"}; !reflect.DeepEqual(added, want) {
		t.Errorf("added = %v, want %v", added, want)
	}
	if want := []string{"opossum", "opossum --quiet bool = false", "opossum --verbose bool = false", "opossum destroy"}; !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v: the line the CLI lost must stay in the record", merged, want)
	}
}

// The wiring from the merge to the file: what the update writes is the merge, so a file written from the CLI alone
// (the loss recorded as the new record, #2161) or one that is not written at all is seen here and not only in a
// hand-run of the update.
func TestRecordInventoryWritesTheMergeToTheFile(t *testing.T) {
	path := t.TempDir() + "/inventory.txt"
	recorded := []string{"opossum", "opossum destroy"}
	got := []string{"opossum", "opossum up"} // destroy gone, up new
	if err := os.WriteFile(path, []byte(strings.Join(recorded, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordInventory(path, recorded, got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "opossum\nopossum destroy\nopossum up\n"; string(raw) != want {
		t.Errorf("the file holds %q, want %q: the line the CLI lost stays, the new one is added", raw, want)
	}
	// Nothing new: the file is left as it is, so a run of the update over a recording that is complete changes nothing.
	// Written out of order on purpose: a write that happened anyway would put it in order.
	if err := os.WriteFile(path, []byte("opossum up\nopossum destroy\nopossum\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := recordInventory(path, recordedLines(string(before)), []string{"opossum"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("the file changed to %q when there was nothing new to record", after)
	}
}

// The recording is read from the CLI, so a check that read nothing would pass anything: it must hold the commands that
// exist, an alias, a default, and a flag from the root and from a command.
func TestCLIInventoryHoldsWhatItIsMeantToHold(t *testing.T) {
	got := strings.Join(cliInventory(newRootCmd()), "\n")
	for _, want := range []string{
		"opossum up", "opossum ws snapshot", "opossum ws ls alias list",
		"opossum --file -f stringArray = []", "opossum --version -v bool = false", "opossum ps --format string = table",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the inventory lacks %q", want)
		}
	}
}
