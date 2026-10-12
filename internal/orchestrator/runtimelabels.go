package orchestrator

import "strings"

// labelsTheRuntimeTakes is the service's labels without the ones `container run -l` refuses, and says so for each one it leaves out.
//
// container 1.5.0 reads a label as at most two pieces and refuses it (`invalid label format`) when it has none or more than two: it splits at the first two `=`s, leaves out the
// empty pieces, and what is still left after those two is a third piece. So `a.b=x=y`, `a.b=x?y=z`, `a.b=/p?r=1` and a value ending in `==` (`a.b=dGVzdA==`, base64's padding) are
// refused, while `a.b=https://y`, `a.b=x=`, `a.b==y`, `a.b` and `=x` are taken (measured, every row in the test). docker takes them all, and a label like a reverse proxy's
// `forwardauth.address=http://auth/verify?rd=https://…` is the common way to meet the rule. A label is only metadata here — nothing in the runtime acts on it — so one it will not
// take is left out and named, and the service starts, rather than `up` stopping on it.
func (o *Orchestrator) labelsTheRuntimeTakes(service string, labels []string) []string {
	kept := make([]string, 0, len(labels))
	for _, l := range labels {
		if labelRuntimeRefuses(l) {
			o.logf("Note: %s: label %q is not passed to the runtime — it refuses a label with more than a key and one value after it; the service starts without it\n", service, labelKey(l))
			continue
		}
		kept = append(kept, l)
	}
	return kept
}

// labelKey is the part of the label before its first `=`: a note names the label by it and leaves the value out, which can be a token a log should not keep.
func labelKey(label string) string {
	key, _, _ := strings.Cut(label, "=")
	return key
}

// labelRuntimeRefuses reports whether the label has no piece or more than two, reading it as the runtime does: split at `=`, a split that gives an empty piece does not count and
// the piece is left out, and once two pieces are cut what is left of the label is a piece of its own.
func labelRuntimeRefuses(label string) bool {
	pieces := 0
	rest := label
	for pieces < 2 {
		i := strings.IndexByte(rest, '=')
		if i < 0 {
			break
		}
		if i > 0 {
			pieces++
		}
		rest = rest[i+1:]
	}
	if rest != "" {
		pieces++
	}
	return pieces == 0 || pieces > 2
}
