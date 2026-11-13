package catalog

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the generated part of docs/SUPPORTED.md")

const (
	doc   = "../../docs/SUPPORTED.md"
	begin = "<!-- generated: go test ./internal/catalog -update -->\n"
	end   = "<!-- end of generated part -->\n"
)

// The generated part of docs/SUPPORTED.md is what the code decides.
func TestSupportedDocUpToDate(t *testing.T) {
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatalf("%s has no generated part between %q and %q", doc, begin, end)
	}
	want := s[:i+len(begin)] + "\n" + Appendix() + "\n" + s[j:]
	if want == s {
		return
	}
	if *update {
		if err := os.WriteFile(doc, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Errorf("docs/SUPPORTED.md is out of date with the code: run go test ./internal/catalog -update")
}
