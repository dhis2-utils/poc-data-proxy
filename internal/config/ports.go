package config

import (
	"strconv"
)
import "net"

// ChoosePorts picks the hub's HTTPS and HTTP ports on a PC that may run other web
// software: 443 and 80, else 8443 and 8080, else a pair the system gives. Both move
// together, so the address has one port. free reports whether a port can be bound.
func ChoosePorts(free func(port int) bool) (https, http int) {
	for _, p := range [][2]int{{443, 80}, {8443, 8080}} {
		if free(p[0]) && free(p[1]) {
			return p[0], p[1]
		}
	}
	return systemPorts()
}

// PortFree reports whether a TCP port can be bound on every address.
func PortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// systemPorts holds the first port while asking for the second, so the system cannot
// hand the same one out twice.
func systemPorts() (int, int) {
	var ports [2]int
	for i := range ports {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, 0
		}
		defer l.Close()
		ports[i] = l.Addr().(*net.TCPAddr).Port
	}
	return ports[0], ports[1]
}
