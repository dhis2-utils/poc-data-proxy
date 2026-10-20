package mdns

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func query(name string, qtype uint16, qu bool) []byte {
	b := []byte{0x12, 0x34, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range strings.Split(name, ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	b = append(b, 0)
	class := uint16(1)
	if qu {
		class |= 0x8000
	}
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(b, qtype), class)
}

type rr struct {
	name  string
	typ   uint16
	class uint16
	ttl   uint32
	ip    net.IP
}

// answers parses the answer section of a response (test helper, no compression).
func answers(t *testing.T, b []byte) (id uint16, out []rr) {
	t.Helper()
	if len(b) < 12 {
		t.Fatalf("short response %d", len(b))
	}
	id = binary.BigEndian.Uint16(b)
	qd, an := int(binary.BigEndian.Uint16(b[4:])), int(binary.BigEndian.Uint16(b[6:]))
	off := 12
	readName := func() string {
		var labels []string
		for b[off] != 0 {
			l := int(b[off])
			labels = append(labels, string(b[off+1:off+1+l]))
			off += 1 + l
		}
		off++
		return strings.Join(labels, ".")
	}
	for i := 0; i < qd; i++ {
		readName()
		off += 4
	}
	for i := 0; i < an; i++ {
		r := rr{name: readName()}
		r.typ, r.class = binary.BigEndian.Uint16(b[off:]), binary.BigEndian.Uint16(b[off+2:])
		r.ttl = binary.BigEndian.Uint32(b[off+4:])
		n := int(binary.BigEndian.Uint16(b[off+8:]))
		r.ip = net.IP(b[off+10 : off+10+n])
		off += 10 + n
		out = append(out, r)
	}
	return id, out
}

func nets(cidrs ...string) func() []*net.IPNet {
	return func() []*net.IPNet {
		var out []*net.IPNet
		for _, c := range cidrs {
			ip, n, _ := net.ParseCIDR(c)
			n.IP = ip
			out = append(out, n)
		}
		return out
	}
}

func responder() *Responder {
	return &Responder{Name: "dhis2.local", Nets: nets("192.168.1.20/24", "8.8.8.8/24", "fe80::1/64", "10.0.0.5/8")}
}

func TestAnswersItsName(t *testing.T) {
	r := responder()
	for _, q := range [][]byte{query("dhis2.local", 1, false), query("DHIS2.Local", 1, true), query("dhis2.local", 255, false)} {
		_, rrs := answers(t, r.Answer(q, nil, false))
		if len(rrs) != 2 {
			t.Fatalf("%d answers: %+v", len(rrs), rrs)
		}
		for i, want := range []string{"192.168.1.20", "10.0.0.5"} {
			a := rrs[i]
			if a.name != "dhis2.local" || a.typ != 1 || a.class != 0x8001 || a.ttl != 120 || !a.ip.Equal(net.ParseIP(want)) {
				t.Errorf("answer %d: %+v", i, a)
			}
		}
	}
	// A legacy unicast query (not from port 5353) gets its ID and question back, with a
	// short TTL (RFC 6762, 6.7).
	id, rrs := answers(t, r.Answer(query("dhis2.local", 1, false), nil, true))
	if id != 0x1234 || len(rrs) != 2 || rrs[0].ttl > 10 {
		t.Fatalf("legacy: id %x, %+v", id, rrs)
	}
}

func TestResponderIgnoresOthers(t *testing.T) {
	r := responder()
	for _, q := range [][]byte{query("other.local", 1, false), query("dhis2.local", 28, false), query("dhis2", 1, false)} {
		if got := r.Answer(q, nil, false); got != nil {
			t.Errorf("answered %x", got)
		}
	}
	resp := query("dhis2.local", 1, false)
	resp[2] = 0x84 // a response, not a query
	if r.Answer(resp, nil, false) != nil {
		t.Error("answered a response")
	}
}

func TestResponderMalformed(t *testing.T) {
	r := responder()
	q := query("dhis2.local", 1, false)
	loop := []byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 12, 0, 1, 0, 1} // a pointer to itself
	many := append([]byte(nil), q...)
	many[5] = 0xe8 // claims 1000 questions
	many[4] = 0x03
	long := append([]byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}, []byte(strings.Repeat("\x3f"+strings.Repeat("a", 63), 5))...)
	for _, b := range [][]byte{nil, q[:5], q[:len(q)-3], loop, many, long} {
		if got := r.Answer(b, nil, false); got != nil {
			t.Errorf("answered malformed %x", b)
		}
	}
}

func TestConflict(t *testing.T) {
	own := []net.IP{net.ParseIP("192.168.1.20")}
	resp := func(ip string) []byte {
		other := &Responder{Name: "dhis2.local", Nets: nets(ip + "/24")}
		return other.Answer(query("dhis2.local", 1, false), nil, true)
	}
	ask := func(answers ...[]byte) func([]byte) [][]byte { return func([]byte) [][]byte { return answers } }
	ctx := context.Background()
	if !Conflict(ctx, ask(resp("192.168.1.99")), "dhis2.local", own) {
		t.Error("another device answering is no conflict")
	}
	if Conflict(ctx, ask(resp("192.168.1.20")), "dhis2.local", own) {
		t.Error("our own answer is a conflict")
	}
	if Conflict(ctx, ask(), "dhis2.local", own) || Conflict(ctx, ask([]byte{1, 2, 3}), "dhis2.local", own) {
		t.Error("silence or junk is a conflict")
	}
}

func TestServeOverUDP(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := responder()
	go r.Serve(ctx, conn)
	c, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write(query("dhis2.local", 1, false))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, rrs := answers(t, buf[:n]); len(rrs) != 2 {
		t.Fatalf("%+v", rrs)
	}
	r.silenced.Store(true)
	c.Write(query("dhis2.local", 1, false))
	c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("a silenced responder answered")
	}
}

// The system's own responder (Windows' mDNS, Bonjour, avahi) holds port 5353 already:
// the hub's shares it.
func TestRunSharesThePort(t *testing.T) {
	other, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		t.Skipf("no multicast here: %v", err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := responder().Run(ctx); err != nil {
		t.Fatalf("could not share port 5353: %v", err)
	}
}

// Review: a PC has virtual networks too (WSL, VirtualBox, VPN): a device gets the
// address on its own network (RFC 6762, 15), not one it cannot reach.
func TestAnswersOnTheQueriersNetwork(t *testing.T) {
	r := &Responder{Name: "dhis2.local", Nets: nets("192.168.1.20/24", "172.28.16.1/20", "192.168.56.1/24")}
	_, rrs := answers(t, r.Answer(query("dhis2.local", 1, false), net.ParseIP("192.168.1.33"), false))
	if len(rrs) != 1 || !rrs[0].ip.Equal(net.ParseIP("192.168.1.20")) {
		t.Fatalf("%+v", rrs)
	}
	// From somewhere no network of ours holds (routed): every address, as before.
	if _, rrs := answers(t, r.Answer(query("dhis2.local", 1, false), net.ParseIP("10.9.9.9"), false)); len(rrs) != 3 {
		t.Fatalf("%+v", rrs)
	}
}

// A LAN numbered from the shared address space (100.64.0.0/10, some mobile routers) is
// a local network: the hub answers with its address there.
func TestAnswersOnSharedAddressSpace(t *testing.T) {
	r := &Responder{Name: "dhis2.local", Nets: nets("100.64.3.20/24")}
	_, rrs := answers(t, r.Answer(query("dhis2.local", 1, false), net.ParseIP("100.64.3.33"), false))
	if len(rrs) != 1 || !rrs[0].ip.Equal(net.ParseIP("100.64.3.20")) {
		t.Fatalf("%+v", rrs)
	}
}
