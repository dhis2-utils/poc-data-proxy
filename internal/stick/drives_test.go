package stick

import (
	"reflect"
	"testing"
)

var (
	e = Drive{Root: `E:\`, Label: "KINGSTON", Serial: 0x1a2b3c4d, Size: 16 << 30}
	f = Drive{Root: `F:\`, Label: "STAFF", Serial: 0x0badf00d, Size: 32 << 30}
)

func has(roots ...string) func(string) bool {
	return func(r string) bool {
		for _, x := range roots {
			if x == r {
				return true
			}
		}
		return false
	}
}

// Review focus 5: a stick is never guessed. Nothing chosen: nothing used, ask. The
// chosen one gone: missing, not replaced by another.
func TestStickChosenNotGuessed(t *testing.T) {
	for _, c := range []struct {
		name    string
		drives  []Drive
		choice  string
		root    string
		ask     []Drive
		missing bool
	}{
		{"two, none chosen", []Drive{e, f}, "", "", []Drive{e, f}, false},
		{"one, none chosen", []Drive{e}, "", "", []Drive{e}, false},
		{"the chosen one", []Drive{e, f}, f.ID(), `F:\`, nil, false},
		{"the chosen one gone", []Drive{e}, f.ID(), "", nil, true},
		{"none plugged in", nil, "", "", nil, false},
	} {
		root, ask, missing := Locate(c.drives, "hub-abc", c.choice, has())
		if root != c.root || !reflect.DeepEqual(ask, c.ask) || missing != c.missing {
			t.Errorf("%s: %q %v %v", c.name, root, ask, missing)
		}
	}
}

// The drive letter changes between plugs: the hub's folder on it finds it again.
func TestStickFoundByFolder(t *testing.T) {
	moved := e
	moved.Root = `G:\`
	root, ask, missing := Locate([]Drive{f, moved}, "hub-abc", "", has(`G:\`))
	if root != `G:\` || ask != nil || missing {
		t.Fatalf("%q %v %v", root, ask, missing)
	}
	if e.ID() != "KINGSTON-1A2B3C4D" {
		t.Fatalf("id %q", e.ID())
	}
}
