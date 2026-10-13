package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// overlayKeysFile is where the entries opossum put in the overlay are kept, under
// `.opossum/`. An entry that was written and is no longer in the file was taken
// out by someone, and finding it again must not put it back.
const overlayKeysFile = "overlay-entries.json"

func (o *Orchestrator) overlayKeysPath() string {
	return filepath.Join(o.Project.BaseDir, ".opossum", overlayKeysFile)
}

// writtenKeys reads them back. known is false when there is no usable record: a
// file from before it existed, or one that cannot be read.
func (o *Orchestrator) writtenKeys() (keys map[string]bool, known bool) {
	if o.Project.BaseDir == "" {
		return nil, false
	}
	body, err := os.ReadFile(o.overlayKeysPath())
	if err != nil {
		return nil, false
	}
	var list []string
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, false
	}
	keys = map[string]bool{}
	for _, k := range list {
		keys[k] = true
	}
	return keys, true
}

// RecordWrittenKeys adds the keys of entries just put in the overlay. A failure
// to record is not an error worth stopping for: the next run then knows less and
// adds less, which is the safe way round.
func (o *Orchestrator) RecordWrittenKeys(changes []Adaptation) {
	if o.Project.BaseDir == "" {
		return
	}
	have, _ := o.writtenKeys()
	if have == nil {
		have = map[string]bool{}
	}
	for _, c := range changes {
		if c.Kind == "applied" && c.Key != "" {
			have[c.Key] = true
		}
	}
	list := make([]string, 0, len(have))
	for k := range have {
		list = append(list, k)
	}
	sort.Strings(list)
	body, err := json.Marshal(list)
	if err != nil {
		return
	}
	dir := filepath.Dir(o.overlayKeysPath())
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".overlay-entries.*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), o.overlayKeysPath()) != nil {
		_ = os.Remove(tmp.Name())
	}
}
