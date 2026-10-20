package config

import "testing"

// Review focus 2: both ports move to the fallback together, so the address has one port.
func TestPortsMoveTogether(t *testing.T) {
	taken := func(ports ...int) func(int) bool {
		return func(p int) bool {
			for _, x := range ports {
				if x == p {
					return false
				}
			}
			return true
		}
	}
	for _, c := range []struct {
		taken       []int
		https, http int
	}{
		{nil, 443, 80},
		{[]int{443}, 8443, 8080},
		{[]int{80}, 8443, 8080},
		{[]int{8443}, 443, 80},
	} {
		if h, p := ChoosePorts(taken(c.taken...)); h != c.https || p != c.http {
			t.Errorf("taken %v: %d/%d, want %d/%d", c.taken, h, p, c.https, c.http)
		}
	}
	h, p := ChoosePorts(taken(443, 80, 8443, 8080))
	if h == 0 || p == 0 || h == p || h == 443 || h == 8443 || p == 80 || p == 8080 {
		t.Fatalf("all four taken: %d/%d", h, p)
	}
}

// The last resort holds the first port while asking for the second: the system may hand
// out a port it just took back (Windows does), and the two ports would be one.
func TestLastResortPortsDiffer(t *testing.T) {
	none := func(int) bool { return false }
	for range 2000 {
		if h, p := ChoosePorts(none); h == 0 || h == p {
			t.Fatalf("%d/%d", h, p)
		}
	}
}
