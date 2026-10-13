package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
)

// Files a Linux host has and this Mac does not.
//
// A compose file written for a Linux host sometimes binds a file that only
// exists there, read-only, for the container to look at: the host's time zone
// name, or a log file a tool is to read. docker compose runs those on a Mac
// because Docker Desktop mounts from a Linux VM that has the file. Here the path
// is on the Mac, it is not there, and its parent (`/etc`, `/var/log`) is not
// the user's to write in, so `up` stops with OPSM-104 before the container is
// made.
//
// Two of these have an answer that keeps what the file means. The time zone is
// the Mac's own, and a log file nothing has written to is an empty one. Both are
// put in the project's state directory and bound in the path's place. Anything
// else that is missing — a device, a kernel path, a directory — is left to the
// refusal: a stand-in would make the service run on something it is not.

// localtimePath is the Mac's time zone link; a variable so a test can point it
// somewhere.
var localtimePath = "/etc/localtime"

// hostLstat asks whether a path is on this Mac; a variable so a test can say it is.
var hostLstat = os.Lstat

// placeholderDir is where the stand-in files are kept, under the project's state
// directory (outside the project's own directory, so nothing is added to a
// repository).
func placeholderDir(project string) (string, error) {
	dir, err := projectStateDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "placeholders"), nil
}

// placeholderKind says whether src is a missing host file this file can stand in
// for: "timezone" for /etc/timezone, "log" for a log file directly under
// /var/log (`*.log`, or syslog, messages or secure), "" for anything else.
func placeholderKind(src string) string {
	if src == "/etc/timezone" {
		return "timezone"
	}
	if filepath.Dir(src) == "/var/log" {
		switch base := filepath.Base(src); {
		case strings.HasSuffix(base, ".log"), base == "syslog", base == "messages", base == "secure":
			return "log"
		}
	}
	return ""
}

// macTimezone is the IANA name of the Mac's time zone, read from the link
// /etc/localtime points through (`…/zoneinfo/Asia/Tokyo`).
func macTimezone() (string, bool) {
	target, err := os.Readlink(localtimePath)
	if err != nil {
		return "", false
	}
	_, name, ok := strings.Cut(target, "zoneinfo/")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// adaptMissingHostFiles rewrites each read-only bind of a file that the Mac lacks
// (see placeholderKind) to a stand-in in the state directory.
func (o *Orchestrator) adaptMissingHostFiles(name string, svc *compose.Service) []serviceAdaptation {
	var out []serviceAdaptation
	dir, err := placeholderDir(o.Project.Name)
	if err != nil {
		return nil
	}
	for _, v := range svc.Volumes {
		src, target, mode, ok := splitMount(v)
		if !ok || !readOnlyMount(mode) {
			continue
		}
		src = filepath.Clean(src) // `/etc//timezone` and `/var/log/../log/syslog` are the same files
		kind := placeholderKind(src)
		if kind == "" {
			continue
		}
		if _, err := hostLstat(src); !os.IsNotExist(err) {
			continue // there on this Mac, or not readable for another reason: not this
		}
		var what string
		switch kind {
		case "timezone":
			tz, ok := macTimezone()
			if !ok {
				continue // no honest content to give it
			}
			what = fmt.Sprintf("a file holding this Mac's time zone (%s)", tz)
		default:
			what = "an empty file"
		}
		// The kind is a directory of its own: what goes in the file is decided
		// from where it is, never from a name made out of the target (which is
		// the compose file's to choose, and not the source's).
		stand := filepath.Join(dir, compose.SanitizeName(name), kind, strings.ReplaceAll(strings.Trim(target, "/"), "/", "_"))
		out = append(out, serviceAdaptation{
			Adaptation: Adaptation{
				Service: name,
				Code:    string(codeBindDirCreate),
				Summary: fmt.Sprintf("service %q: %s bound from %s, which this Mac does not have", name, target, what),
			},
			comment: commentBlock(
				fmt.Sprintf("%s service %q: %s bound from a stand-in file.", overlayMarker, esc(name), esc(target)),
				[]string{
					fmt.Sprintf("Your file binds %s, read-only. That file exists on a Linux host (and in", esc(src)),
					"Docker Desktop's VM) but not on this Mac, and its folder is not one opossum can",
					fmt.Sprintf("create it in, so `up` would stop with %s. The service reads it and", codeBindDirCreate),
					fmt.Sprintf("does not write it, so it gets %s, kept in opossum's state directory.", what),
					"Nothing on your Mac outside that directory is touched. The file is made once;",
					"delete it to have it made again (after changing the Mac's time zone, say).",
				},
				[]string{
					fmt.Sprintf("after `opossum up`, `opossum exec %s cat %s` shows the stand-in's content.", name, esc(target)),
				},
				[]string{
					fmt.Sprintf("if the service needs the real file's content, bind a file of your own at %s", esc(target)),
					"in this entry's place.",
				},
			),
			block:   "volumes",
			entries: []string{"      - " + esc(stand+":"+target+":ro")},
		})
	}
	return out
}

// standInKind says whether src is a stand-in file adaptMissingHostFiles hands out
// for this project, and of which kind: the path runs
// `…/opossum/<project>/placeholders/<service>/<kind>/<name>`. It is read from the
// path, and not from the state directory this process would pick, because the
// overlay names the path as it was when it was written — a different
// XDG_STATE_HOME, or another user, must not turn the stand-in into a directory.
func (o *Orchestrator) standInKind(src string) string {
	parts := strings.Split(filepath.Clean(src), string(filepath.Separator))
	project := compose.SanitizeName(o.Project.Name)
	i := len(parts) - 4 // …/placeholders/<service>/<kind>/<name>
	if i < 2 || parts[i] != "placeholders" || parts[i-1] != project || parts[i-2] != "opossum" {
		return ""
	}
	if kind := parts[i+2]; (kind == "timezone" || kind == "log") && parts[i+1] != "" && parts[i+3] != "" {
		return kind
	}
	return ""
}

// materializePlaceholder writes the stand-in file when src is one of the paths
// adaptMissingHostFiles hands out, and reports whether it was one. It is called
// where a missing bind source would otherwise be made as a directory, because a
// file is what these are, and the overlay can only name the path: the file does
// not exist until the service is about to start.
func (o *Orchestrator) materializePlaceholder(src string) (bool, error) {
	kind := o.standInKind(src)
	if kind == "" {
		return false, nil
	}
	body := ""
	if kind == "timezone" {
		tz, ok := macTimezone()
		if !ok {
			return true, fmt.Errorf("the Mac's time zone could not be read from %s", localtimePath)
		}
		body = tz + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		return true, err
	}
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		return true, err
	}
	o.logf("Created %s as a stand-in for a bind mount\n", src)
	return true, nil
}
