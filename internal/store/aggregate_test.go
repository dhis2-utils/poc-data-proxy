package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func aggStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDK(mustKey(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAggFormsRoundTrip(t *testing.T) {
	s := aggStore(t)
	f := &AggForm{Key: "agg:A|202610|OU|AOC", DataSet: "A", Period: "202610", OrgUnit: "OU", AOC: "AOC",
		Body: []byte(`{"dataValues":[]}`), Reg: "true|nurse|2026-10-15", FetchedAt: Now()}
	if err := s.PutAggForm(f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAggForm(f.Key)
	if err != nil || got == nil || string(got.Body) != string(f.Body) || got.Reg != f.Reg || got.Stale {
		t.Fatalf("get: %+v %v", got, err)
	}
	s.MarkAggFormStale(f.Key)
	all, _ := s.AggForms()
	if len(all) != 1 || !all[0].Stale || all[0].Body != nil {
		t.Fatalf("list: %+v", all)
	}
	if any, _ := s.AnyAggForm("A"); any == nil || string(any.Body) != string(f.Body) {
		t.Fatalf("any form of A: %+v", any)
	}
	s.DeleteAggForm(f.Key)
	if got, _ := s.GetAggForm(f.Key); got != nil {
		t.Fatal("deleted")
	}
}

func TestAggregateCollapse(t *testing.T) {
	s := aggStore(t)
	app := func(user int64, payload, cell string, completion bool) int64 {
		var seq int64
		err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			var err error
			seq, err = s.AppendAggregate(tx, user, []byte(payload), cell, "agg:F", completion)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	a := app(1, `{"v":1}`, "agg:F|de|co|value", false)
	if b := app(1, `{"v":2}`, "agg:F|de|co|value", false); b != a {
		t.Fatalf("same user, same cell and field: replaced (%d, %d)", a, b)
	}
	if c := app(1, `{"c":"x"}`, "agg:F|de|co|comment", false); c == a {
		t.Fatal("a comment does not replace a value edit")
	}
	if x := app(1, `{"v":5}`, "agg:F|de|co|value", false); x == a {
		t.Fatal("a value edit after a comment of the cell is a new entry: the order of the cell's edits holds")
	}
	if d := app(2, `{"v":3}`, "agg:F|de|co|value", false); d == a {
		t.Fatal("another user's edit is its own entry")
	}
	done := app(1, `{"complete":true}`, "agg:F|complete", true)
	if e := app(1, `{"v":4}`, "agg:F|de|co|value", false); e == a || e < done {
		t.Fatal("an edit after a queued completion is not folded into an entry before it")
	}
	es, err := s.PendingAggregate("agg:F")
	if err != nil || len(es) != 6 || string(es[0].Payload) != `{"v":2}` || es[0].Kind != OutboxAggregate {
		t.Fatalf("pending for the form: %d %v", len(es), err)
	}
	if es[4].DependsOnSeq == 0 {
		t.Fatal("a completion depends on the form's earlier entries")
	}
}
