package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A key written as an alias (`*k : !override true`, `x-k: &k init`) is the key it names for the booleans a word may be written for: docker compose reads
// it as the same answer as `init: !override true` written out (measured, v5.5.1, `config -q`, #1962), where the key's own name was read — the alias's — and
// the value was left to the struct, which refused the tag.
func TestABooleanKeyWrittenAsAnAliasIsReadAsTheKeyItNames(t *testing.T) {
	for _, key := range []string{"init", "tty", "read_only"} {
		for _, v := range []struct {
			value   string
			refused bool // docker compose answers 1
		}{
			{"true", false}, {"'yes'", false}, {"\"True\"", false}, {"!override true", false}, {"!override false", false}, {"!override 'on'", false}, {"!reset null", false},
			{"maybe", true}, {"!override maybe", true}, {"5", true}, {"~", true},
		} {
			for _, form := range []struct{ name, head, key string }{
				{"written out", "", key},
				{"an alias", "x-k: &k " + key + "\n", "*k"},
			} {
				t.Run(key+"/"+form.name+"/"+v.value, func(t *testing.T) {
					p := filepath.Join(t.TempDir(), "c.yaml")
					body := form.head + "services:\n  a:\n    image: x\n    " + form.key + " : " + v.value + "\n"
					if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
					if _, err := Load(p); (err != nil) != v.refused {
						t.Errorf("refused = %v, want %v (docker compose answers %v): %v", err != nil, v.refused, map[bool]int{false: 0, true: 1}[v.refused], err)
					}
				})
			}
		}
	}
}

// What the alias is called is not the key: an anchor named like a boolean key (`x-k: &tty hostname`, then `*tty : maybe`) is the key it names, `hostname`, which takes
// a word (docker compose answers 0; the name of the alias was read for `tty` and the word refused), and the one that names a boolean key is read as that key whatever
// the alias is called (`x-k: &privileged tty`, `*privileged : !override true`).
func TestTheNameOfTheAliasIsNotTheKeyItStandsFor(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"an anchor named like a boolean key that stands for another", "x-k: &tty hostname\nservices:\n  a:\n    image: x\n    *tty : maybe\n"},
		{"an anchor named for another key that stands for a boolean one", "x-k: &privileged tty\nservices:\n  a:\n    image: x\n    *privileged : !override true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err != nil {
				t.Errorf("refused, docker compose answers 0: %v", err)
			}
		})
	}
}

// The same function reads the other booleans a word may be written for, and each reads the key an alias names (measured, v5.5.1, `config -q`, every row 0 with the key
// written out and written as an alias): a long-form volume's `read_only`, its `nocopy`, a network's `internal`, an `env_file` entry's `required` and a healthcheck's `disable`.
func TestTheOtherBooleansReadTheKeyAnAliasNames(t *testing.T) {
	for _, tc := range []struct{ name, key, body string }{
		{"volume read_only", "read_only", "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /d\n        *K : \"True\"\nvolumes:\n  v: {}\n"},
		{"volume nocopy", "nocopy", "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /d\n        volume:\n          *K : \"True\"\nvolumes:\n  v: {}\n"},
		{"network internal", "internal", "services:\n  a:\n    image: x\n    networks: [n]\nnetworks:\n  n:\n    *K : \"True\"\n"},
		{"healthcheck disable", "disable", "services:\n  a:\n    image: x\n    healthcheck:\n      *K : \"True\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			body := "x-k: &k " + tc.key + "\n" + strings.ReplaceAll(tc.body, "*K", "*k")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err != nil {
				t.Errorf("refused, docker compose answers 0: %v", err)
			}
		})
	}
}
