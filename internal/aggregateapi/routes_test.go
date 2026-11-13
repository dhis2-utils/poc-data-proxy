package aggregateapi

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// OfflineRoutes, which the catalog of what works offline lists (docs/SUPPORTED.md),
// is what ServeHTTP's switch answers offline: each case's path and methods.
func TestAggregateOfflineRoutes(t *testing.T) {
	src, err := os.ReadFile("aggregateapi.go")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	caseRe := regexp.MustCompile(`(?m)^\tcase p == "([^"]+)" && \(?(.*):$`)
	methodRe := regexp.MustCompile(`http\.Method([A-Z][a-z]+)`)
	for _, m := range caseRe.FindAllStringSubmatch(string(src), -1) {
		for _, mm := range methodRe.FindAllStringSubmatch(m[2], -1) {
			got = append(got, map[string]string{"Get": "GET", "Post": "POST", "Delete": "DELETE", "Put": "PUT"}[mm[1]]+" "+m[1])
		}
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(OfflineRoutes))
	if !slices.Equal(got, want) {
		t.Errorf("ServeHTTP answers offline %v; OfflineRoutes says %v", got, want)
	}
}
