package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// pointTimezoneAt makes the Mac's time zone link point at …/zoneinfo/<name>.
func pointTimezoneAt(t *testing.T, name string) {
	t.Helper()
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/var/db/timezone/zoneinfo/"+name, link); err != nil {
		t.Fatal(err)
	}
	old := localtimePath
	localtimePath = link
	t.Cleanup(func() { localtimePath = old })
}

// macHasThem is set by the one test that says the files are there.
var macHasThem bool

// macLacksTheFiles makes every path look absent on this machine. The tests are
// about what happens to a file the Mac does not have, and a CI machine is Linux,
// where /etc/timezone and /var/log/syslog usually exist.
func macLacksTheFiles(t *testing.T) {
	t.Helper()
	if macHasThem {
		return
	}
	old := hostLstat
	hostLstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { hostLstat = old })
}

func planWithVolumes(t *testing.T, vols ...string) (body string, changes []Adaptation, p *compose.Project, dir string) {
	t.Helper()
	macLacksTheFiles(t)
	dir = t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	src := "name: demo\nservices:\n  app:\n    image: someapp\n    volumes:\n"
	for _, v := range vols {
		src += "      - " + v + "\n"
	}
	writeFile(t, dir, "compose.yaml", src)
	var err error
	p, err = compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body, changes = New(p, nil, "opossum", io.Discard).PlanOverlay()
	return body, changes, p, dir
}

// Files a Linux host has and this Mac does not are bound from stand-ins, read-only
// ones only, and only the two kinds whose stand-in keeps the file's meaning.
func TestAMissingLinuxHostFileIsBoundFromAStandIn(t *testing.T) {
	pointTimezoneAt(t, "Asia/Tokyo")
	for _, tc := range []struct {
		name, vol string
		applied   bool
	}{
		{"time zone", "/etc/timezone:/etc/timezone:ro", true},
		{"time zone at another target", "/etc/timezone:/etc/tz-from-host:ro", true},
		{"auth log", "/var/log/auth.log:/var/log/auth.log:ro", true},
		{"syslog", "/var/log/syslog:/var/log/syslog:ro", true},
		{"messages", "/var/log/messages:/var/log/messages:ro", true},
		{"secure", "/var/log/secure:/var/log/secure:ro", true},
		{"a source spelt with a double slash", "/etc//timezone:/etc/timezone:ro", true},
		{"a source spelt through a parent", "/var/log/../log/syslog:/var/log/syslog:ro", true},
		{"a .log file", "/var/log/zz-not-on-a-mac.log:/logs/x.log:ro", true},
		// What stays refused: writable (the service is to write it), not a file
		// the two kinds name, a directory-shaped name, or a device and kernel path.
		{"time zone, writable", "/etc/timezone:/etc/timezone", false},
		{"time zone, explicitly rw", "/etc/timezone:/etc/timezone:rw", false},
		{"log, writable", "/var/log/auth.log:/var/log/auth.log", false},
		{"a log directory", "/var/log/nginx:/var/log/nginx:ro", false},
		{"nested under /var/log", "/var/log/app/app.log:/var/log/app.log:ro", false},
		{"a device", "/dev/dri:/dev/dri:ro", false},
		{"kernel modules", "/lib/modules:/lib/modules:ro", false},
		{"sources", "/usr/src:/usr/src:ro", false},
		{"a relative path", "./etc/timezone:/etc/timezone:ro", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, changes, _, dir := planWithVolumes(t, tc.vol)
			var applied []Adaptation
			for _, c := range changes {
				if c.Kind == "applied" {
					applied = append(applied, c)
				}
			}
			if !tc.applied {
				if len(applied) != 0 {
					t.Fatalf("%s must not be given a stand-in, got %+v\n%s", tc.vol, applied, body)
				}
				return
			}
			if len(applied) != 1 || applied[0].Code != string(codeBindDirCreate) {
				t.Fatalf("want one applied OPSM-104 entry, got %+v\n%s", changes, body)
			}
			// The overlay loads and the mount's target now comes from the state directory.
			ov := writeFile(t, dir, OverlayFileName, body)
			p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
			if err != nil {
				t.Fatalf("the overlay must load: %v\n%s", err, body)
			}
			vols := p.Services["app"].Volumes
			if len(vols) != 1 || !strings.Contains(vols[0], filepath.Join(dir, "state", "opossum", "demo", "placeholders", "app")) || !strings.HasSuffix(vols[0], ":ro") {
				t.Errorf("the mount is not the state directory's stand-in, read-only: %v", vols)
			}
			_, target, _, _ := splitMount(tc.vol)
			if _, tgt, _, _ := splitMount(vols[0]); tgt != target {
				t.Errorf("the stand-in is bound at %q, not at %q", tgt, target)
			}
		})
	}
}

// No time zone to give means no stand-in: an empty /etc/timezone is not the Mac's.
func TestNoStandInForTheTimezoneWhenTheMacsCannotBeRead(t *testing.T) {
	old := localtimePath
	localtimePath = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { localtimePath = old })
	body, changes, _, _ := planWithVolumes(t, "/etc/timezone:/etc/timezone:ro")
	for _, c := range changes {
		if c.Kind == "applied" {
			t.Fatalf("a stand-in was written with no time zone to put in it: %+v\n%s", c, body)
		}
	}
}

// The overlay can only name the file; it is made where a missing bind source
// would otherwise be made into a directory, holding the Mac's time zone or
// nothing — decided by what the SOURCE was, not by the name of the target.
func TestTheStandInFilesAreMadeBeforeTheServiceStarts(t *testing.T) {
	pointTimezoneAt(t, "Asia/Tokyo")
	for _, tc := range []struct{ name, vol, want string }{
		{"time zone", "/etc/timezone:/etc/timezone:ro", "Asia/Tokyo\n"},
		{"time zone at another target", "/etc/timezone:/etc/tz-from-host:ro", "Asia/Tokyo\n"},
		{"log", "/var/log/auth.log:/var/log/auth.log:ro", ""},
		{"log at the time zone's target", "/var/log/zz.log:/etc/timezone:ro", ""},
		{"log at a target named like the time zone's file", "/var/log/zz.log:/x/etc_timezone:ro", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _, _, dir := planWithVolumes(t, tc.vol)
			ov := writeFile(t, dir, OverlayFileName, body)
			p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
			if err != nil {
				t.Fatal(err)
			}
			vols := p.Services["app"].Volumes
			if len(vols) != 1 {
				t.Fatalf("volumes: %v", vols)
			}
			o := New(p, nil, "opossum", io.Discard)
			if err := o.ensureBindDirs("app", vols, "`opossum up`"); err != nil {
				t.Fatal(err)
			}
			src, _, _, _ := splitMount(vols[0])
			if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("the stand-in is not a file: %v", err)
			}
			if got, _ := os.ReadFile(src); string(got) != tc.want {
				t.Errorf("the stand-in holds %q, want %q", got, tc.want)
			}
		})
	}
}

// What the overlay names is where it was written. Read in another state
// directory — another XDG_STATE_HOME, another user — it is still a stand-in and
// is made as a file there, not as a directory.
func TestAStandInNamedByAnOverlayWrittenElsewhereIsStillAFile(t *testing.T) {
	macLacksTheFiles(t)
	p := loadProject(t, "name: demo\nservices:\n  app:\n    image: someapp\n")
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "mine"))
	o := New(p, nil, "opossum", io.Discard)
	elsewhere := filepath.Join(t.TempDir(), "opossum", "demo", "placeholders", "app", "log", "var_log_syslog")
	if err := o.ensureBindDirs("app", []string{elsewhere + ":/var/log/syslog:ro"}, "`opossum up`"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(elsewhere); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("not a file: %v", err)
	}
	// And a path that merely resembles one is not.
	for _, bad := range []string{"opossum/other/placeholders/app/log/x", "opossum/demo/placeholders/app/weird/x", "opossum/demo/placeholders/app/log/a/b", "demo/placeholders/app/log/x"} {
		if k := o.standInKind(filepath.Join(t.TempDir(), bad)); k != "" {
			t.Errorf("%s was taken for a stand-in (%s)", bad, k)
		}
	}
}

// The stand-in cannot be made: the reason is the error, not a directory bound
// where a file was wanted.
func TestAStandInThatCannotBeMadeStopsTheUpWithTheReason(t *testing.T) {
	pointTimezoneAt(t, "Asia/Tokyo")
	t.Run("the Mac's time zone cannot be read when the file is made", func(t *testing.T) {
		body, _, _, dir := planWithVolumes(t, "/etc/timezone:/etc/timezone:ro")
		ov := writeFile(t, dir, OverlayFileName, body)
		p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
		if err != nil {
			t.Fatal(err)
		}
		old := localtimePath
		localtimePath = filepath.Join(t.TempDir(), "absent")
		t.Cleanup(func() { localtimePath = old })
		err = New(p, nil, "opossum", io.Discard).ensureBindDirs("app", p.Services["app"].Volumes, "`opossum up`")
		if err == nil || !strings.Contains(err.Error(), "time zone could not be read") {
			t.Errorf("want the time zone as the reason, got %v", err)
		}
	})
	t.Run("the directory cannot be made", func(t *testing.T) {
		body, _, _, dir := planWithVolumes(t, "/var/log/auth.log:/var/log/auth.log:ro")
		ov := writeFile(t, dir, OverlayFileName, body)
		p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
		if err != nil {
			t.Fatal(err)
		}
		// a dangling link where the kind's directory has to be: the file is not
		// there, and the directory cannot be made
		src, _, _, _ := splitMount(p.Services["app"].Volumes[0])
		kindDir := filepath.Dir(src)
		if err := os.MkdirAll(filepath.Dir(kindDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "nowhere"), kindDir); err != nil {
			t.Fatal(err)
		}
		err = New(p, nil, "opossum", io.Discard).ensureBindDirs("app", p.Services["app"].Volumes, "`opossum up`")
		if err == nil || !strings.Contains(err.Error(), "could not make the stand-in file") || !strings.Contains(err.Error(), "OPSM-104") {
			t.Errorf("want the stand-in error naming OPSM-104, got %v", err)
		}
	})
}

// A state directory with a space and a `$` in it is written the way every value in
// the overlay is, and reads back as the same path.
func TestTheStandInPathSurvivesASpaceAndADollarInTheStateDirectory(t *testing.T) {
	macLacksTheFiles(t)
	dir := t.TempDir()
	state := filepath.Join(dir, "my $state")
	t.Setenv("XDG_STATE_HOME", state)
	writeFile(t, dir, "compose.yaml", "name: demo\nservices:\n  app:\n    image: someapp\n    volumes:\n      - /var/log/syslog:/var/log/syslog:ro\n")
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := New(p0, nil, "opossum", io.Discard).PlanOverlay()
	ov := writeFile(t, dir, OverlayFileName, body)
	p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if src, _, _, _ := splitMount(p.Services["app"].Volumes[0]); !strings.HasPrefix(src, state+string(filepath.Separator)) {
		t.Errorf("the path did not survive: %q, want it under %q", src, state)
	}
}

// The path decides it alone, so a service whose environment cannot be read (which
// stops every other decision) is still given its stand-in.
func TestTheStandInIsGivenWhenTheEnvironmentCannotBeRead(t *testing.T) {
	macLacksTheFiles(t)
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	writeFile(t, dir, "compose.yaml", "name: demo\nservices:\n  app:\n    image: someapp\n    env_file: ./nope.env\n    volumes:\n      - /var/log/syslog:/var/log/syslog:ro\n")
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, changes := New(p0, nil, "opossum", io.Discard).PlanOverlay()
	applied := 0
	for _, c := range changes {
		if c.Kind == "applied" {
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("want the stand-in entry, got %+v", changes)
	}
}

// A path outside the state directory's stand-ins is made the way it always was.
func TestAMissingPathOutsideTheStandInsIsStillMadeAsADirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	p := loadProject(t, "name: demo\nservices:\n  app:\n    image: someapp\n")
	o := New(p, nil, "opossum", io.Discard)
	missing := filepath.Join(dir, "data")
	if err := o.ensureBindDirs("app", []string{missing + ":/data"}, "`opossum up`"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(missing); err != nil || !fi.IsDir() {
		t.Errorf("the bind source is not a directory: %v", err)
	}
}

// A path that is on this Mac is its own: nothing is put in its place.
func TestAFileThatIsOnThisMacIsLeftAlone(t *testing.T) {
	pointTimezoneAt(t, "Asia/Tokyo")
	macHasThem = true
	t.Cleanup(func() { macHasThem = false })
	old := hostLstat
	hostLstat = func(string) (os.FileInfo, error) { return nil, nil } // present
	t.Cleanup(func() { hostLstat = old })
	_, changes, _, _ := planWithVolumes(t, "/etc/timezone:/etc/timezone:ro", "/var/log/auth.log:/var/log/auth.log:ro")
	for _, c := range changes {
		if c.Kind == "applied" {
			t.Errorf("a file that is there was replaced: %+v", c)
		}
	}
}

// A service may be called `..`, which the loader takes; its stand-in must still be
// a file under the stand-ins' directory, not one level up beside them.
func TestAServiceNamedWithDotsKeepsItsStandInUnderThePlaceholders(t *testing.T) {
	macLacksTheFiles(t)
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	writeFile(t, dir, "compose.yaml", "name: demo\nservices:\n  ..:\n    image: someapp\n    volumes:\n      - /var/log/syslog:/var/log/syslog:ro\n")
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Skipf("the loader does not take this name: %v", err)
	}
	body, _ := New(p0, nil, "opossum", io.Discard).PlanOverlay()
	ov := writeFile(t, dir, OverlayFileName, body)
	p, err := compose.LoadFiles([]string{filepath.Join(dir, "compose.yaml"), ov}, nil)
	if err != nil {
		t.Fatal(err)
	}
	src, _, _, _ := splitMount(p.Services[".."].Volumes[0])
	root := filepath.Join(dir, "state", "opossum", "demo", "placeholders") + string(filepath.Separator)
	if !strings.HasPrefix(src, root) {
		t.Fatalf("the stand-in %s is not under %s", src, root)
	}
	if err := New(p, nil, "opossum", io.Discard).ensureBindDirs("..", p.Services[".."].Volumes, "`opossum up`"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("not a file: %v", err)
	}
}
