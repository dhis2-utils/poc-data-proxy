package provision

// Pairwise returns rows (one value index per dimension) such that every pair of values
// of every two dimensions appears in at least one row: an all-pairs covering array,
// built greedily and deterministically. Thirty rows cover what a full product would
// need hundreds for, and most access bugs need only two settings to interact.
func Pairwise(sizes []int) [][]int {
	type pair struct{ i, vi, j, vj int }
	uncovered := map[pair]bool{}
	for i := range sizes {
		for j := i + 1; j < len(sizes); j++ {
			for vi := 0; vi < sizes[i]; vi++ {
				for vj := 0; vj < sizes[j]; vj++ {
					uncovered[pair{i, vi, j, vj}] = true
				}
			}
		}
	}
	covers := func(row []int) int {
		n := 0
		for i := range row {
			for j := i + 1; j < len(row); j++ {
				if uncovered[pair{i, row[i], j, row[j]}] {
					n++
				}
			}
		}
		return n
	}
	var rows [][]int
	for len(uncovered) > 0 {
		// Seed the row with the first uncovered pair (in a fixed order), then fill the
		// other dimensions one by one with the value that covers most new pairs.
		var seed pair
		first := true
		for i := range sizes {
			for j := i + 1; j < len(sizes); j++ {
				for vi := 0; vi < sizes[i]; vi++ {
					for vj := 0; vj < sizes[j]; vj++ {
						if first && uncovered[pair{i, vi, j, vj}] {
							seed, first = pair{i, vi, j, vj}, false
						}
					}
				}
			}
		}
		row := make([]int, len(sizes))
		fixed := make([]bool, len(sizes))
		row[seed.i], row[seed.j] = seed.vi, seed.vj
		fixed[seed.i], fixed[seed.j] = true, true
		for d := range sizes {
			if fixed[d] {
				continue
			}
			best, bestN := 0, -1
			for v := 0; v < sizes[d]; v++ {
				row[d] = v
				n := 0
				for e := range sizes {
					if e != d && fixed[e] {
						a, b := d, e
						va, vb := v, row[e]
						if a > b {
							a, b, va, vb = b, a, vb, va
						}
						if uncovered[pair{a, va, b, vb}] {
							n++
						}
					}
				}
				if n > bestN {
					best, bestN = v, n
				}
			}
			row[d] = best
			fixed[d] = true
		}
		if covers(row) == 0 {
			break // cannot happen: the seed pair is uncovered
		}
		for i := range row {
			for j := i + 1; j < len(row); j++ {
				delete(uncovered, pair{i, row[i], j, row[j]})
			}
		}
		rows = append(rows, row)
	}
	return rows
}
