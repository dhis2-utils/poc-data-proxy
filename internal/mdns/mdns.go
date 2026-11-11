// Package mdns answers one name, dhis2.local, with the machine's private IPv4
// addresses, where nothing else announces it: on Windows (the Pi has avahi). It answers
// A queries for that name and nothing else, and stays silent while another device on
// the network answers the same name with a different address (docs/superpowers/specs/
// 2026-10-20-windows-service-design.md).
package mdns

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

const (
	typeA     = 1
	typeAny   = 255
	classIN   = 1
	ttl       = 120
	legacyTTL = 10
	port      = 5353
)

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: port}

// shared is 100.64.0.0/10, the shared address space (carrier-grade NAT) some routers
// number a LAN from: as private as 10.0.0.0/8 for the hub.
var shared = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// Responder answers queries for Name.
type Responder struct {
	Name string              // e.g. "dhis2.local"
	Nets func() []*net.IPNet // the machine's addresses with their networks (InterfaceNets)

	silenced atomic.Bool
}

// Answer returns the response to a query from the device at from (nil if unknown), or
// nil. It answers with the private IPv4 addresses on that device's network (RFC 6762,
// section 15: not a WSL or VPN adapter's), or all of them when no network of ours holds
// it. A legacy query (one not from port 5353, section 6.7) gets its ID and question
// back and a short TTL.
func (r *Responder) Answer(q []byte, from net.IP, legacy bool) []byte {
	if len(q) < 12 || q[2]&0x80 != 0 || q[2]&0x78 != 0 { // a response, or not a standard query
		return nil
	}
	qd := int(binary.BigEndian.Uint16(q[4:]))
	if qd == 0 || qd > 16 {
		return nil
	}
	off := 12
	asked := false
	var question []byte
	for i := 0; i < qd; i++ {
		name, next, ok := readName(q, off)
		if !ok || next+4 > len(q) {
			return nil
		}
		qtype := binary.BigEndian.Uint16(q[next:])
		qclass := binary.BigEndian.Uint16(q[next+2:]) &^ 0x8000
		off = next + 4
		if strings.EqualFold(name, r.Name) && (qtype == typeA || qtype == typeAny) && (qclass == classIN || qclass == typeAny) {
			asked = true
			// Written out, not copied: a compression pointer in the query would point
			// elsewhere in the answer, or at itself (fuzzing, review section 10).
			question = append(appendName(nil, name), q[next:off]...)
		}
	}
	if !asked {
		return nil
	}
	var ips, near []net.IP
	for _, n := range r.Nets() {
		if v4 := n.IP.To4(); v4 != nil && (v4.IsPrivate() || shared.Contains(v4)) {
			ips = append(ips, v4)
			if from != nil && n.Contains(from) {
				near = append(near, v4)
			}
		}
	}
	if len(near) > 0 {
		ips = near
	}
	if len(ips) == 0 {
		return nil
	}
	out := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(out[2:], 0x8400) // response, authoritative
	binary.BigEndian.PutUint16(out[6:], uint16(len(ips)))
	life, class := uint32(ttl), uint16(0x8000|classIN) // cache-flush: we own the name
	if legacy {
		copy(out[0:2], q[0:2])
		binary.BigEndian.PutUint16(out[4:], 1)
		out = append(out, question...)
		life, class = legacyTTL, classIN
	}
	for _, ip := range ips {
		out = appendName(out, r.Name)
		out = binary.BigEndian.AppendUint16(out, typeA)
		out = binary.BigEndian.AppendUint16(out, class)
		out = binary.BigEndian.AppendUint32(out, life)
		out = binary.BigEndian.AppendUint16(out, 4)
		out = append(out, ip...)
	}
	return out
}

// readName decodes a name at off, following compression pointers (at most 10 jumps),
// and returns the offset after it in the original position.
func readName(b []byte, off int) (string, int, bool) {
	var labels []string
	next, jumps, length := -1, 0, 0
	for {
		if off >= len(b) {
			return "", 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			return strings.Join(labels, "."), next, true
		case l&0xc0 == 0xc0:
			if off+1 >= len(b) || jumps >= 10 {
				return "", 0, false
			}
			if next < 0 {
				next = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3fff)
			jumps++
		case l&0xc0 != 0:
			return "", 0, false
		default:
			if off+1+l > len(b) {
				return "", 0, false
			}
			length += l + 1
			if length > 255 {
				return "", 0, false
			}
			labels = append(labels, string(b[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

func appendName(b []byte, name string) []byte {
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

// answeredIPs is every A record for name in a response.
func answeredIPs(b []byte, name string) []net.IP {
	if len(b) < 12 || b[2]&0x80 == 0 {
		return nil
	}
	qd, an := int(binary.BigEndian.Uint16(b[4:])), int(binary.BigEndian.Uint16(b[6:]))
	off := 12
	for i := 0; i < qd; i++ {
		_, next, ok := readName(b, off)
		if !ok {
			return nil
		}
		off = next + 4
	}
	var ips []net.IP
	for i := 0; i < an && i < 64; i++ {
		n, next, ok := readName(b, off)
		if !ok || next+10 > len(b) {
			return ips
		}
		typ := binary.BigEndian.Uint16(b[next:])
		rdlen := int(binary.BigEndian.Uint16(b[next+8:]))
		if next+10+rdlen > len(b) {
			return ips
		}
		if typ == typeA && rdlen == 4 && strings.EqualFold(n, name) {
			ips = append(ips, net.IP(b[next+10:next+14]))
		}
		off = next + 10 + rdlen
	}
	return ips
}

// Conflict asks the network who answers name and reports whether any answer holds an
// address that is not one of own.
func Conflict(ctx context.Context, ask func(q []byte) [][]byte, name string, own []net.IP) bool {
	q := appendName([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}, name)
	q = binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(q, typeA), classIN)
	for _, resp := range ask(q) {
		for _, ip := range answeredIPs(resp, name) {
			mine := false
			for _, o := range own {
				if o.Equal(ip) {
					mine = true
				}
			}
			if !mine {
				return true
			}
		}
	}
	return false
}

// Serve answers queries arriving on conn until ctx ends.
func (r *Responder) Serve(ctx context.Context, conn net.PacketConn) {
	go func() { <-ctx.Done(); conn.Close() }()
	buf := make([]byte, 9000)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if r.silenced.Load() {
			continue
		}
		udp, _ := from.(*net.UDPAddr)
		legacy := udp == nil || udp.Port != port
		var fromIP net.IP
		if udp != nil {
			fromIP = udp.IP
		}
		resp := r.Answer(buf[:n], fromIP, legacy)
		if resp == nil {
			continue
		}
		to := net.Addr(group)
		if legacy || n >= 12 && hasQU(buf[:n]) {
			to = from
		}
		conn.WriteTo(resp, to)
	}
}

func hasQU(q []byte) bool {
	_, next, ok := readName(q, 12)
	return ok && next+4 <= len(q) && q[next+2]&0x80 != 0
}

// Run listens on the mDNS group (sharing the port with the system's own responder) and
// answers until ctx ends, checking for another device with the name at start and
// every 5 minutes.
func (r *Responder) Run(ctx context.Context) error {
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		return err
	}
	go r.Serve(ctx, conn)
	check := func() {
		var own []net.IP
		for _, n := range r.Nets() {
			own = append(own, n.IP)
		}
		other := Conflict(ctx, askNetwork, r.Name, own)
		if other != r.silenced.Load() {
			r.silenced.Store(other)
			if other {
				slog.Warn("mdns: another device answers the hub's name; staying silent", "name", r.Name)
			} else {
				slog.Info("mdns: answering the hub's name", "name", r.Name)
			}
		}
	}
	check()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			check()
		}
	}
}

// askNetwork sends q to the mDNS group from a port of its own (so answers come back
// to it, unicast) and collects what arrives within a second.
func askNetwork(q []byte) [][]byte {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil
	}
	defer c.Close()
	if _, err := c.WriteTo(q, group); err != nil {
		return nil
	}
	var out [][]byte
	deadline := time.Now().Add(time.Second)
	for {
		c.SetReadDeadline(deadline)
		buf := make([]byte, 9000)
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			return out
		}
		out = append(out, buf[:n])
	}
}

// InterfaceNets is the machine's addresses with their networks.
func InterfaceNets() []*net.IPNet {
	var out []*net.IPNet
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				out = append(out, n)
			}
		}
	}
	return out
}
