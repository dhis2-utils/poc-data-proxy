package access

import "testing"

// Users differing only in who they are share a key; anything central decides by, or
// relationship-type access that was never read, keeps them apart.
func TestGroupKey(t *testing.T) {
	base := func(uid string) *List {
		return &List{UserUID: uid, Username: uid, OrgUnits: []string{"B", "A"}, UserGroups: []string{"G"},
			Programs: map[string]Perm{"P": {Read: true}}, RelationshipTypes: map[string]Perm{}}
	}
	a, b := base("u1"), base("u2")
	b.OrgUnits = []string{"A", "B"} // order does not matter
	if a.GroupKey() != b.GroupKey() {
		t.Fatal("same access, different keys")
	}
	c := base("u3")
	c.Programs["P"] = Perm{Read: true, Write: true}
	if a.GroupKey() == c.GroupKey() {
		t.Fatal("different program access, same key")
	}
	d, e := base("u4"), base("u5")
	d.RelationshipTypes, e.RelationshipTypes = nil, nil
	if d.GroupKey() == e.GroupKey() || d.GroupKey() == a.GroupKey() {
		t.Fatal("unknown relationship-type access was grouped")
	}
	f := base("u6")
	f.Ungroupable = true
	if f.GroupKey() == a.GroupKey() {
		t.Fatal("an ungroupable user was grouped")
	}
}
