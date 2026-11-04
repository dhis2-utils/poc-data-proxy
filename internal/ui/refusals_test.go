package ui

import "testing"

// Who closes a refusal, by its codes (plan of 4 November, "Refusals by who can fix them").
func TestClosedBy(t *testing.T) {
	for _, c := range []struct {
		codes []string
		want  string
	}{
		{[]string{"E1064"}, ClosedByAuthor},
		{[]string{"E1020"}, ClosedByAuthor},
		{[]string{"E1082"}, ClosedByNobody},
		{[]string{"E1302", "E1114"}, ClosedByNobody},
		{[]string{"E1000"}, ClosedOnCentral},
		{[]string{"NO_LOGIN"}, ClosedOnCentral},
		{nil, ClosedOnCentral}, // a plain 500
	} {
		if got := closedBy(c.codes); got != c.want {
			t.Errorf("%v: %q, want %q", c.codes, got, c.want)
		}
	}
}
