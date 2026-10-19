package netsetup

import (
	"context"
	"github.com/dhis2/facility-hub/internal/netsetup/nmfake"
	"reflect"
	"testing"
)

func canned() *nmfake.NM {
	return &nmfake.NM{Out: map[string]string{
		"-t -f DEVICE,TYPE,STATE,CONNECTION device": "eth0:ethernet:connected:Wired connection 1\n" +
			"wlan0:wifi:disconnected:\n" +
			"usb0:ethernet:connected:Wired connection 2\n" +
			"lo:loopback:connected (externally):lo\n" +
			"p2p-dev-wlan0:wifi-p2p:disconnected:\n",
		"-t -f GENERAL.DRIVER,IP4.ADDRESS device show eth0":  "GENERAL.DRIVER:bcmgenet\nIP4.ADDRESS[1]:192.168.1.20/24\n",
		"-t -f GENERAL.DRIVER,IP4.ADDRESS device show wlan0": "GENERAL.DRIVER:brcmfmac\n",
		"-t -f GENERAL.DRIVER,IP4.ADDRESS device show usb0":  "GENERAL.DRIVER:cdc_ether\nIP4.ADDRESS[1]:192.168.8.100/24\nIP4.ADDRESS[2]:10.0.0.5/8\n",
		"-t -f SSID,SIGNAL,SECURITY device wifi list --rescan yes": "Clinic\\:Staff:72:WPA2\n" +
			":40:WPA2\n" +
			"Guest:55:\n" +
			"Clinic\\:Staff:60:WPA2 WPA3\n" +
			"Back\\\\slash:30:WPA1\n",
	}}
}

func TestDevices(t *testing.T) {
	devs, err := Devices(context.Background(), canned())
	if err != nil {
		t.Fatal(err)
	}
	want := []Device{
		{Name: "eth0", Type: "ethernet", State: "connected", Connection: "Wired connection 1", Driver: "bcmgenet", Addresses: []string{"192.168.1.20"}},
		{Name: "wlan0", Type: "wifi", State: "disconnected", Driver: "brcmfmac"},
		{Name: "usb0", Type: "ethernet", State: "connected", Connection: "Wired connection 2", Driver: "cdc_ether", Addresses: []string{"192.168.8.100", "10.0.0.5"}},
	}
	if !reflect.DeepEqual(devs, want) {
		t.Fatalf("got  %+v\nwant %+v", devs, want)
	}
}

func TestScan(t *testing.T) {
	nets, err := Scan(context.Background(), canned())
	if err != nil {
		t.Fatal(err)
	}
	want := []Network{{SSID: "Clinic:Staff", Signal: 72, Secure: true}, {SSID: "Guest", Signal: 55}, {SSID: `Back\slash`, Signal: 30, Secure: true}}
	if !reflect.DeepEqual(nets, want) {
		t.Fatalf("got  %+v\nwant %+v", nets, want)
	}
}

func TestUplink(t *testing.T) {
	cable := Device{Name: "eth0", Type: "ethernet", State: "connected", Driver: "bcmgenet", Addresses: []string{"192.168.1.20"}}
	modem := Device{Name: "usb0", Type: "ethernet", State: "connected", Driver: "cdc_ether", Addresses: []string{"192.168.8.100"}}
	wifi := Device{Name: "wlan0", Type: "wifi", State: "connected", Connection: "hub-wifi", Addresses: []string{"10.1.1.5"}}
	ap := Device{Name: "wlan0", Type: "wifi", State: "connected", Connection: "hub-facility", Addresses: []string{"10.42.0.1"}}
	noAddr := Device{Name: "eth0", Type: "ethernet", State: "connected", Driver: "bcmgenet"}
	for _, c := range []struct {
		devs       []Device
		kind, name string
	}{
		{[]Device{cable, modem}, "cable", "eth0"},
		{[]Device{noAddr, modem}, "modem", "usb0"},
		{[]Device{wifi}, "wifi", "hub-wifi"},
		{[]Device{ap}, "", ""},
		{[]Device{noAddr}, "", ""},
	} {
		if k, n := Uplink(c.devs); k != c.kind || n != c.name {
			t.Errorf("%+v: %q %q, want %q %q", c.devs, k, n, c.kind, c.name)
		}
	}
}
