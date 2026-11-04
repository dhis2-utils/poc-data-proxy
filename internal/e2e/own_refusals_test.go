package e2e

import (
	"strings"
	"testing"
)

// The hub's page shows signed-in staff their own refused changes with what they
// entered, and who closes each. Others' refusals stay in the issue table, masked, and
// a visitor who is not signed in sees none (plan of 4 November, item 4).
func TestOwnRefusalsOnHubPage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.offline()
	e.central.Seed("CentralTe81", "CentralEn81", "Other", "NAT-8181", nil)
	e.central.Seed("CentralTe82", "CentralEn82", "Other", "NAT-8282", nil)
	a.json("POST", postSync, newPatient("OwnTe000081", "OwnEn000081", "Amina", "NAT-8181", nil), 200)
	b.json("POST", postSync, newPatient("OwnTe000082", "OwnEn000082", "Bakari", "NAT-8282", nil), 200)
	e.online()
	e.sync()

	page := func(c *client) string {
		t.Helper()
		st, p := c.do("GET", "/hub/ui", nil)
		if st != 200 {
			t.Fatalf("%s: page %d", c.user, st)
		}
		return string(p)
	}
	own := func(p string) string {
		i := strings.Index(p, `id="own-refusals"`)
		if i < 0 {
			return ""
		}
		return p[i : i+strings.Index(p[i:], "Issues (rejected by central)")]
	}
	pa := page(a)
	sa := own(pa)
	for _, want := range []string{"Your sync errors", "OwnTe000081", "NAT-8181", "Amina",
		"Non-unique attribute value `NAT-8181`", "E1064", "Fixed by the author", " | ",
		"OwnEn000081", "with <span class=\"mono\">OwnTe000081</span>"} {
		if !strings.Contains(sa, want) {
			t.Errorf("nurse_a's sync errors lack %q:\n%s", want, sa)
		}
	}
	for _, other := range []string{"NAT-8282", "Bakari", "OwnTe000082"} {
		if strings.Contains(sa, other) {
			t.Errorf("nurse_a's sync errors show nurse_b's %q", other)
		}
	}
	// The issue table is as before: every issue, with its author, and values masked.
	if !strings.Contains(pa, "OwnTe000082") || !strings.Contains(pa, "nurse_b") {
		t.Error("the issue table no longer lists nurse_b's refusal")
	}
	if rest := strings.Replace(pa, sa, "", 1); strings.Contains(rest, "NAT-8181") || strings.Contains(rest, "NAT-8282") ||
		strings.Contains(rest, "Bakari") {
		t.Error("a value shows outside the user's own sync errors")
	}
	if sb := own(page(b)); !strings.Contains(sb, "NAT-8282") || strings.Contains(sb, "NAT-8181") {
		t.Errorf("nurse_b's sync errors:\n%s", sb)
	}
	st, anon := e.web("nurse_a").do("GET", "/hub/ui", nil)
	if st == 200 || strings.Contains(string(anon), "NAT-8181") || strings.Contains(string(anon), "sync errors") {
		t.Errorf("a visitor not signed in: %d %.200s", st, anon)
	}

	// Fixed by the author: the error leaves their list.
	e.offline()
	a.json("POST", postSync, newPatient("OwnTe000081", "OwnEn000081", "Amina", "NAT-8383", nil), 200)
	e.online()
	e.sync()
	if s := own(page(a)); s != "" {
		t.Errorf("fixed, still listed:\n%s", s)
	}
}
