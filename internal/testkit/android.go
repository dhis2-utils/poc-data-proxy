package testkit

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Android drives the Capture app on the emulator over adb (docs/TESTING.md). The app's
// screens are Jetpack Compose without resource IDs, so it finds nodes by text, content
// description and class.
type Android struct {
	Package string // com.dhis2
}

type Node struct {
	Text, Desc, Class string
	X1, Y1, X2, Y2    int
}

func (n Node) String() string { return fmt.Sprintf("%s %q %q", n.Class, n.Text, n.Desc) }

func adb(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "adb", args...).CombinedOutput()
	return string(out), err
}

// Available reports whether an emulator or a device is attached.
func (a *Android) Available() bool {
	out, err := adb(15*time.Second, "devices")
	return err == nil && strings.Contains(out, "\tdevice")
}

// HostFor returns the name under which the device reaches port on the machine that
// runs adb: 10.0.2.2 from an emulator; from a device on USB, localhost, with adb
// reverse forwarding the port over the cable.
func (a *Android) HostFor(port string) (string, error) {
	if out, _ := adb(15*time.Second, "shell", "getprop", "ro.kernel.qemu"); strings.TrimSpace(out) == "1" {
		return "10.0.2.2", nil
	}
	if out, err := adb(15*time.Second, "reverse", "tcp:"+port, "tcp:"+port); err != nil {
		return "", fmt.Errorf("adb reverse: %v %s", err, out)
	}
	return "localhost", nil
}

// Reset clears the app's data and starts it. Notifications are granted up front, so
// the permission prompt does not cover the sync screens.
func (a *Android) Reset() error {
	if _, err := adb(30*time.Second, "shell", "pm", "clear", a.Package); err != nil {
		// pm clear can hang while the app is busy (seen once mid-sync): stop it, retry.
		adb(30*time.Second, "shell", "am", "force-stop", a.Package)
		if out, err := adb(60*time.Second, "shell", "pm", "clear", a.Package); err != nil {
			return fmt.Errorf("pm clear: %v %s", err, out)
		}
	}
	adb(15*time.Second, "shell", "pm", "grant", a.Package, "android.permission.POST_NOTIFICATIONS")
	// pm clear removes the old task asynchronously, and its timeout kills a process
	// launched too soon ("remove task"); let it settle, and launch again if it did.
	for try := 0; try < 3; try++ {
		time.Sleep(5 * time.Second)
		if _, err := adb(30*time.Second, "shell", "monkey", "-p", a.Package, "-c", "android.intent.category.LAUNCHER", "1"); err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
		if out, _ := adb(15*time.Second, "shell", "pidof", a.Package); strings.TrimSpace(out) != "" {
			return nil
		}
	}
	return fmt.Errorf("%s does not stay running after launch", a.Package)
}

var boundsRe = regexp.MustCompile(`\[(\d+),(\d+)\]\[(\d+),(\d+)\]`)

// Screen returns the nodes on screen.
func (a *Android) Screen() ([]Node, error) {
	// A failed dump leaves the previous file behind, so remove it first.
	adb(15*time.Second, "shell", "rm", "-f", "/sdcard/hubtest-ui.xml")
	if out, err := adb(30*time.Second, "shell", "uiautomator", "dump", "/sdcard/hubtest-ui.xml"); err != nil || strings.Contains(out, "ERROR") {
		return nil, fmt.Errorf("uiautomator dump: %v %s", err, out)
	}
	x, err := adb(15*time.Second, "shell", "cat", "/sdcard/hubtest-ui.xml")
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(strings.NewReader(x))
	var out []Node
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "node" {
			continue
		}
		var n Node
		for _, at := range se.Attr {
			switch at.Name.Local {
			case "text":
				n.Text = at.Value
			case "content-desc":
				n.Desc = at.Value
			case "class":
				n.Class = at.Value
			case "bounds":
				if m := boundsRe.FindStringSubmatch(at.Value); m != nil {
					n.X1, _ = strconv.Atoi(m[1])
					n.Y1, _ = strconv.Atoi(m[2])
					n.X2, _ = strconv.Atoi(m[3])
					n.Y2, _ = strconv.Atoi(m[4])
				}
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// Find returns the first node whose text or description is s, or else contains it
// (case-insensitive): a button's label is often also part of a longer sentence.
func Find(nodes []Node, s string) (Node, bool) {
	s = strings.ToLower(s)
	for _, n := range nodes {
		if strings.ToLower(n.Text) == s || strings.ToLower(n.Desc) == s {
			return n, true
		}
	}
	for _, n := range nodes {
		if strings.Contains(strings.ToLower(n.Text), s) || strings.Contains(strings.ToLower(n.Desc), s) {
			return n, true
		}
	}
	return Node{}, false
}

// OfClass returns the nodes of a class (e.g. "EditText"), top to bottom.
func OfClass(nodes []Node, class string) []Node {
	var out []Node
	for _, n := range nodes {
		if strings.HasSuffix(n.Class, class) {
			out = append(out, n)
		}
	}
	return out
}

func (a *Android) Tap(n Node) error {
	_, err := adb(15*time.Second, "shell", "input", "tap", strconv.Itoa((n.X1+n.X2)/2), strconv.Itoa((n.Y1+n.Y2)/2))
	return err
}

// Type types text into the focused field.
func (a *Android) Type(s string) error {
	s = strings.NewReplacer(" ", "%s", "&", `\&`, "'", `\'`, "(", `\(`, ")", `\)`, "!", `\!`).Replace(s)
	_, err := adb(30*time.Second, "shell", "input", "text", s)
	return err
}

func (a *Android) Key(code string) error {
	_, err := adb(15*time.Second, "shell", "input", "keyevent", code)
	return err
}

// Wait polls the screen until one of texts appears, and returns which and the screen.
func (a *Android) Wait(timeout time.Duration, texts ...string) (string, []Node, error) {
	deadline := time.Now().Add(timeout)
	var last []Node
	for time.Now().Before(deadline) {
		nodes, err := a.Screen()
		if err == nil {
			last = nodes
			for _, t := range texts {
				if _, ok := Find(nodes, t); ok {
					return t, nodes, nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	var seen []string
	for _, n := range last {
		if n.Text != "" || n.Desc != "" {
			seen = append(seen, n.String())
		}
	}
	return "", last, fmt.Errorf("none of %q on screen after %s; screen: %s", texts, timeout, strings.Join(seen, "; "))
}

// Screenshot saves the screen as PNG.
func (a *Android) Screenshot(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", "adb exec-out screencap -p > '"+path+"'").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v %s", err, out)
	}
	return nil
}

// Login takes a freshly reset Capture app through server selection and login, and
// waits for the home screen, scrolled to program (by name).
func (a *Android) Login(server, user, password, program string, timeout time.Duration) ([]Node, error) {
	_, nodes, err := a.Wait(time.Minute, "Server URL")
	if err != nil {
		return nil, err
	}
	n, _ := Find(nodes, "Server URL")
	a.Tap(n)
	a.Type(server)
	n, _ = Find(nodes, "Next")
	a.Tap(n)
	if _, nodes, err = a.Wait(time.Minute, "Log in"); err != nil {
		return nil, err
	}
	fields := OfClass(nodes, "EditText")
	if len(fields) < 2 {
		return nil, fmt.Errorf("login screen has %d text fields", len(fields))
	}
	a.Tap(fields[0])
	a.Type(user)
	a.Tap(fields[1])
	a.Type(password)
	a.Key("111") // escape: hide the keyboard so the button is on screen
	if _, nodes, err = a.Wait(30*time.Second, "Log in"); err != nil {
		return nil, err
	}
	n, _ = Find(nodes, "Log in")
	a.Tap(n)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		seen, nodes, err := a.Wait(time.Until(deadline), "Not now", "No filters applied", "Wrong", "error", "failed")
		switch {
		case err != nil:
			return nodes, err
		case seen == "Not now": // the anonymous-statistics question
			n, _ := Find(nodes, "Not now")
			a.Tap(n)
		case seen == "No filters applied": // the home screen's filter bar
			return a.ScrollTo(program, 15)
		default:
			return nodes, fmt.Errorf("login: %q on screen", seen)
		}
	}
	return nil, fmt.Errorf("home screen with %q not reached in %s", program, timeout)
}

var countRe = regexp.MustCompile(`^(\d+) `)

// HomeCount reads the record count under a program on the home screen ("36 Person").
func HomeCount(nodes []Node, program string) (int, bool) {
	for i, n := range nodes {
		if n.Text != program {
			continue
		}
		for _, m := range nodes[i+1 : min(i+4, len(nodes))] {
			if c := countRe.FindStringSubmatch(m.Text); c != nil {
				v, _ := strconv.Atoi(c[1])
				return v, true
			}
		}
	}
	return 0, false
}

// ScrollTo swipes the screen up until a node with exactly this text is on it.
func (a *Android) ScrollTo(text string, swipes int) ([]Node, error) {
	for i := 0; ; i++ {
		nodes, err := a.Screen()
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.Text == text {
				return nodes, nil
			}
		}
		if i == swipes {
			return nodes, fmt.Errorf("%q not found after %d swipes", text, swipes)
		}
		w, h := a.size()
		adb(15*time.Second, "shell", "input", "swipe", strconv.Itoa(w/2), strconv.Itoa(h*3/4), strconv.Itoa(w/2), strconv.Itoa(h*3/8), "400")
		time.Sleep(time.Second)
	}
}

var sizeRe = regexp.MustCompile(`(\d+)x(\d+)`)

// size returns the screen size (wm size), or a common phone size if unknown.
func (a *Android) size() (int, int) {
	out, _ := adb(15*time.Second, "shell", "wm", "size")
	if m := sizeRe.FindStringSubmatch(out); m != nil {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		return w, h
	}
	return 1080, 2400
}

// TapAt taps a point as a share of the screen (0..1), for unlabelled controls.
func (a *Android) TapAt(fx, fy float64) error {
	w, h := a.size()
	_, err := adb(15*time.Second, "shell", "input", "tap", strconv.Itoa(int(fx*float64(w))), strconv.Itoa(int(fy*float64(h))))
	return err
}

// Transfer transfers the person whose list entry shows label, in the open program's
// list, to the org unit named to: dashboard, top-right menu, Transfer, pick, confirm.
func (a *Android) Transfer(label, to string) error {
	_, nodes, err := a.Wait(time.Minute, label)
	if err != nil {
		return err
	}
	n, _ := Find(nodes, label)
	a.Tap(n)
	if _, _, err := a.Wait(time.Minute, "Owned by"); err != nil {
		return err
	}
	a.TapAt(0.93, 0.065) // the dashboard's menu button has no label
	if _, nodes, err = a.Wait(30*time.Second, "Transfer"); err != nil {
		return err
	}
	n, _ = Find(nodes, "Transfer")
	a.Tap(n)
	if _, nodes, err = a.Wait(30*time.Second, to); err != nil {
		return err
	}
	n, _ = Find(nodes, to)
	a.Tap(n)
	time.Sleep(time.Second)
	if nodes, err = a.Screen(); err != nil {
		return err
	}
	n, _ = Find(nodes, "Transfer") // the confirm button; the title is "Transfer person"
	a.Tap(n)
	if _, _, err := a.Wait(30*time.Second, "Owned by:  "+to); err != nil {
		return fmt.Errorf("after the transfer: %w", err)
	}
	a.Key("4")
	return nil
}

// SyncRecord, on a program's list, taps the Sync of the first person whose card
// shows marker (e.g. "Owned by:  <org unit>") and confirms sending their changes.
func (a *Android) SyncRecord(marker string) error {
	seen, nodes, err := a.Wait(time.Minute, marker, "Sync needed")
	if err != nil {
		return err
	}
	if seen == "Sync needed" { // the app asks again after a failed send
		s, _ := Find(nodes, "Send")
		return a.Tap(s)
	}
	card, _ := Find(nodes, marker)
	for _, n := range nodes {
		if n.Text == "Sync" && n.Y1 > card.Y1 && n.Y1 < card.Y1+400 {
			a.Tap(n)
			if _, nodes, err = a.Wait(30*time.Second, "Send"); err != nil {
				return err
			}
			s, _ := Find(nodes, "Send")
			return a.Tap(s)
		}
	}
	return fmt.Errorf("no Sync button on the card with %q: nothing to send", marker)
}
