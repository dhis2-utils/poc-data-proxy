package provision

import "testing"

func TestPairwiseCoversAllPairs(t *testing.T) {
	sizes := []int{5, 5, 4, 2, 3, 2, 3, 3}
	rows := Pairwise(sizes)
	for i := range sizes {
		for j := i + 1; j < len(sizes); j++ {
			for vi := 0; vi < sizes[i]; vi++ {
				for vj := 0; vj < sizes[j]; vj++ {
					found := false
					for _, r := range rows {
						if r[i] == vi && r[j] == vj {
							found = true
						}
					}
					if !found {
						t.Fatalf("pair %d=%d %d=%d not covered", i, vi, j, vj)
					}
				}
			}
		}
	}
	if len(rows) > 40 {
		t.Fatalf("%d rows", len(rows))
	}
	t.Logf("%d rows for %v", len(rows), sizes)
}
