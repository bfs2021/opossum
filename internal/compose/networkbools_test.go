package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A network's `attachable`, `enable_ipv4` and `enable_ipv6` take a boolean, as its `internal` does: a word that is not one, a number, nothing, a list or a mapping is refused,
// and `'yes'`, `"True"` and `!override true` are read (measured, v5.5.1, `config -q`, every row, written out, written as an alias and written by a second file, #2047). Each key
// holds the guards on its own: the type the declaration is decoded into, the word read as a boolean and the key with nothing after it.
func TestANetworksBooleansAreHeldToTheTypeOfABoolean(t *testing.T) {
	for _, key := range []string{"attachable", "enable_ipv4", "enable_ipv6"} {
		for _, v := range []struct {
			value   string
			refused bool // docker compose answers 1
		}{
			{"true", false}, {"'yes'", false}, {"\"True\"", false}, {"!override true", false}, {"!reset null", false},
			{"maybe", true}, {"!override maybe", true}, {"5", true}, {"1.5", true}, {"~", true}, {"[]", true}, {"{}", true},
		} {
			for _, form := range []struct {
				name  string
				files func(key, value string) []string
			}{
				{"written out", func(key, value string) []string {
					return []string{"services:\n  a:\n    image: x\nnetworks:\n  n:\n    " + key + ": " + value + "\n"}
				}},
				{"an alias", func(key, value string) []string {
					return []string{"x-k: &k " + key + "\nservices:\n  a:\n    image: x\nnetworks:\n  n:\n    *k : " + value + "\n"}
				}},
				{"a second file", func(key, value string) []string {
					return []string{"services:\n  a:\n    image: x\nnetworks:\n  n:\n    name: nn\n", "networks:\n  n:\n    " + key + ": " + value + "\n"}
				}},
				{"a value that is an alias", func(key, value string) []string {
					return []string{"x-v: &v " + value + "\nservices:\n  a:\n    image: x\nnetworks:\n  n:\n    " + key + ": *v\n"}
				}},
				{"a mapping a merge key brings in", func(key, value string) []string {
					return []string{"x-m: &m\n  " + key + ": " + value + "\nservices:\n  a:\n    image: x\nnetworks:\n  n:\n    <<: *m\n"}
				}},
				{"a list of mappings a merge key brings in", func(key, value string) []string {
					return []string{"x-m: &m\n  " + key + ": " + value + "\nservices:\n  a:\n    image: x\nnetworks:\n  n:\n    <<: [*m]\n"}
				}},
				{"a network no service joins", func(key, value string) []string {
					return []string{"services:\n  a:\n    image: x\nnetworks:\n  unused:\n    " + key + ": " + value + "\n"}
				}},
			} {
				t.Run(key+"/"+form.name+"/"+v.value, func(t *testing.T) {
					dir := t.TempDir()
					var paths []string
					for i, body := range form.files(key, v.value) {
						p := filepath.Join(dir, "c"+string(rune('1'+i))+".yaml")
						if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
						paths = append(paths, p)
					}
					if _, err := LoadFiles(paths, nil); (err != nil) != v.refused {
						t.Errorf("refused = %v, want %v (docker compose answers %v): %v", err != nil, v.refused, map[bool]int{false: 0, true: 1}[v.refused], err)
					}
				})
			}
		}
	}
}

// The word an anchor holds is read as a boolean where a boolean key takes it, and is the word it is where it is read elsewhere: `x-v: &v "yes"` is `true` for a network's
// `attachable` and `yes` for a label (the anchor is not rewritten in place), whether the key is written with the alias or brought in by a `<<`. A word that is not one is
// refused in the words of the key.
func TestAnAnchorReadAsABooleanIsStillTheWordItIsElsewhere(t *testing.T) {
	// The label is read before the network that reads the anchor as a boolean (the services are read first) and after it (network `b` is read after network `a`).
	for _, tc := range []struct{ name, body, label string }{
		{"a value that is an alias", "x-v: &v \"yes\"\nservices:\n  a:\n    image: x\n    labels:\n      l: *v\nnetworks:\n  n:\n    attachable: *v\n", "l=yes"},
		{"a mapping a merge key brings in", "x-m: &m\n  attachable: \"yes\"\nservices:\n  a:\n    image: x\n    labels:\n      <<: *m\nnetworks:\n  n:\n    <<: *m\n", "attachable=yes"},
		{"a value that is an alias, read again by a network after it", "x-v: &v \"yes\"\nservices:\n  a:\n    image: x\nnetworks:\n  a:\n    attachable: *v\n  b:\n    labels:\n      l: *v\n", "network b: l=yes"},
		{"a mapping a merge key brings in, read again by a network after it", "x-m: &m\n  attachable: \"yes\"\nservices:\n  a:\n    image: x\nnetworks:\n  a:\n    <<: *m\n  b:\n    labels:\n      <<: *m\n", "network b: attachable=yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := Load(p)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := strings.Join(project.Services["a"].Labels, " ")
			if strings.HasPrefix(tc.label, "network b: ") {
				got = "network b: " + strings.Join(project.Networks["b"].Labels, " ")
			}
			if got != tc.label {
				t.Errorf("the label is %q, want %q", got, tc.label)
			}
		})
	}
	t.Run("a word that is not one is refused naming the key", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.yaml")
		body := "services:\n  a:\n    image: x\nnetworks:\n  n:\n    enable_ipv6: maybe\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "enable_ipv6 must be true or false") {
			t.Fatalf("want `enable_ipv6 must be true or false`, got %v", err)
		}
	})
}
