package tracker

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Person searches filter in SQL and event searches in Go: both must give the same
// answer for every operator, kind and value (the values of TestFilterOperatorParity,
// whose answers were checked against central).
func TestFilterSQLMatchesGo(t *testing.T) {
	s := testStore(t)
	values := []string{"Abc", "abc", "ABC def", "xabcx", "Åsa", "åsa", "a;b", "10", "9", "a%b", "a_b", "Ab C",
		"9", "10.5", "100", "2.5", "-1", "0", "1000", "2024-01-05", "2024-01-15", "2023-12-31", "2024-02-29",
		"red,blue", "Blue", "a,b,c", "red", "x,abc"}
	for i, v := range values {
		if _, err := s.DB().Exec(`INSERT INTO te_attrs(te_uid, attr, value, value_lc) VALUES(?,?,?,?)`,
			fmt.Sprintf("Te%09d", i), "AttrAbcdefg", v, strings.ToLower(v)); err != nil {
			t.Fatal(err)
		}
	}
	// One person without the attribute (another one instead), for null and the negations.
	s.DB().Exec(`INSERT INTO te_attrs(te_uid, attr, value, value_lc) VALUES('TeNoValue01', 'AttrOther01', 'x', 'x')`)
	people := []string{"TeNoValue01"}
	for i := range values {
		people = append(people, fmt.Sprintf("Te%09d", i))
	}
	operands := []string{"abc", "ABC", "åsa", "ÅSA", "a", "c", "10", "10.0", "9.5", "1e1", "-1", "a;b", "abc;10", "10;9",
		"%", "_", "2024-01-05", "2024", "ab", "red", "BLUE", "red;blue", "b,c", "re", "ue"}
	ops := []string{"eq", "ieq", "ne", "neq", "nieq", "like", "ilike", "nlike", "nilike", "sw", "ew", "in", "gt", "ge", "lt", "le"}
	checked := 0
	for _, k := range []valueKind{kindText, kindNumber, kindInteger, kindOrdered, kindMulti} {
		for _, op := range append(ops, "null", "!null") {
			list := operands
			if op == "null" || op == "!null" {
				list = []string{""}
			}
			for _, x := range list {
				f := valueFilter{uid: "AttrAbcdefg", op: op, value: x}
				if f.check(k, "TYPE", "attribute") != nil {
					continue // refused or needs a connection: neither path runs
				}
				cond, args, negate := f.sqlCond(k)
				q := "SELECT p.value FROM json_each(?) p WHERE EXISTS (SELECT 1 FROM te_attrs a WHERE a.te_uid = p.value AND a.attr = ? AND " + cond + ")"
				if negate {
					q = "SELECT p.value FROM json_each(?) p WHERE NOT EXISTS (SELECT 1 FROM te_attrs a WHERE a.te_uid = p.value AND a.attr = ? AND a.value_lc <> '')"
				}
				list, _ := jsonList(people)
				rows, err := s.DB().Query(q, append([]any{list, f.uid}, args...)...)
				if err != nil {
					t.Fatalf("%v %s:%s: %v", k, op, x, err)
				}
				var bySQL []string
				for rows.Next() {
					var uid string
					rows.Scan(&uid)
					bySQL = append(bySQL, uid)
				}
				rows.Close()
				var byGo []string
				for i, p := range people {
					vals := map[string]string{}
					if i > 0 {
						vals[f.uid] = values[i-1]
					}
					if f.match(vals, k) {
						byGo = append(byGo, p)
					}
				}
				if k.numeric() {
					// Central keeps only numbers in number attributes and data values
					// (SQLite's CAST reads a prefix of other text, Go rejects it).
					bySQL, byGo = onlyNumbers(bySQL, values), onlyNumbers(byGo, values)
				}
				slices.Sort(bySQL)
				slices.Sort(byGo)
				if !slices.Equal(bySQL, byGo) {
					t.Errorf("kind %v, %s:%s: SQL %v, Go %v", k, op, x, bySQL, byGo)
				}
				checked++
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d cases checked", checked)
	}
}

// What central refuses, the hub refuses with central's words; a range on text needs
// a connection.
func TestFilterParsingAndChecks(t *testing.T) {
	for _, c := range []struct {
		filter string
		want   string // "" = accepted
	}{
		{"AttrAbcdefg:zz:1", "filter=AttrAbcdefg:zz:1 is invalid. 'zz' is not a valid operator."},
		{"AttrAbcdefg:eq", "filter=AttrAbcdefg:eq is invalid. Binary operator 'eq' must have a value."},
		{"AttrAbcdefg:eq:Red,Blue", "filter=AttrAbcdefg:eq:Red,Blue is invalid. UID must be an alphanumeric string of 11 characters starting with a letter, but was: Blue"},
		{"AttrAbcdefg", ""}, {"AttrAbcdefg:null", ""}, {"AttrAbcdefg:!null", ""}, {"AttrAbcdefg:ge:1:le:5", ""},
		{"AttrAbcdefg:LIKE:x", ""}, {"AttrAbcdefg:!eq:x", ""},
		{"AttrAbcdefg:!in:x", "filter=AttrAbcdefg:!in:x is invalid. '!in' is not a valid operator."},
	} {
		fs, err := parseFilters(map[string][]string{"filter": {c.filter}})
		got := ""
		if err != nil {
			got = err.Message
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.filter, got, c.want)
		}
		if c.filter == "AttrAbcdefg" && (len(fs) != 1 || fs[0].op != "!null") {
			t.Errorf("a bare filter is %+v, want !null", fs)
		}
	}
	if err := (valueFilter{uid: "A", op: "gt", value: "abc"}).check(kindNumber, "NUMBER", "attribute"); err == nil || err.Status != 400 {
		t.Errorf("a number filter with abc: %v", err)
	}
	if err := (valueFilter{uid: "A", op: "in", value: "1;x"}).check(kindNumber, "NUMBER", "attribute"); err == nil {
		t.Error("in with a non-number on a number was accepted")
	}
	if err := (valueFilter{uid: "A", op: "like", value: "abc"}).check(kindNumber, "NUMBER", "attribute"); err != nil {
		t.Errorf("like on a number reads text: %v", err)
	}
	if err := (valueFilter{uid: "A", op: "lt", value: "m"}).check(kindText, "TEXT", "attribute"); err == nil || err.Status != 503 {
		t.Errorf("a range on text: %v, want needs connection", err)
	}
	if err := (valueFilter{uid: "A", op: "lt", value: "2024-01-01"}).check(kindOrdered, "DATE", "attribute"); err != nil {
		t.Errorf("a range on a date: %v", err)
	}
}

func jsonList(xs []string) (string, error) {
	b := strings.Builder{}
	b.WriteString("[")
	for i, x := range xs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q", x)
	}
	b.WriteString("]")
	return b.String(), nil
}

// onlyNumbers keeps the people whose value is a number (and the one without a value).
func onlyNumbers(uids, values []string) []string {
	var out []string
	for _, u := range uids {
		var i int
		if _, err := fmt.Sscanf(u, "Te%09d", &i); err != nil {
			out = append(out, u)
			continue
		}
		if _, err := strconv.ParseFloat(values[i], 64); err == nil {
			out = append(out, u)
		}
	}
	return out
}
