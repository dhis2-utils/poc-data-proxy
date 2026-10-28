package testkit

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Data entry, accounts, connectivity and app versions for the Capture app, as the
// adversarial Android scenarios use them (docs/ADVERSARIAL-TESTING.md, I and N).
// Written against Capture 3.5.0 on a 800x1340 tablet; other versions are driven by
// the same labels where they kept them.

// SetWifi turns the device's Wi-Fi on or off: off, the app knows it has no network
// and works offline, as a tablet out of range does. adb runs over USB and keeps
// working, and so does adb reverse.
func (a *Android) SetWifi(on bool) error {
	arg := "disable"
	if on {
		arg = "enable"
	}
	if out, err := adb(30*time.Second, "shell", "svc", "wifi", arg); err != nil {
		return fmt.Errorf("svc wifi %s: %v %s", arg, err, out)
	}
	for range 30 {
		out, _ := adb(15*time.Second, "shell", "dumpsys", "connectivity")
		up := !strings.Contains(out, "Active default network: none")
		if up == on {
			if on {
				time.Sleep(3 * time.Second) // the app hears of the network a moment later
			}
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("wifi did not turn %s", arg)
}

// Version returns the installed app's versionName.
func (a *Android) Version() string {
	out, _ := adb(30*time.Second, "shell", "dumpsys", "package", a.Package)
	if m := regexp.MustCompile(`versionName=(\S+)`).FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// Install installs an APK. With keep, it is an update in place (adb install -r) that
// keeps the app's data; otherwise the app is removed first, with its data.
func (a *Android) Install(apk string, keep bool) error {
	if !keep {
		adb(2*time.Minute, "uninstall", a.Package)
	}
	args := []string{"install"}
	if keep {
		args = append(args, "-r")
	}
	out, err := adb(10*time.Minute, append(args, apk)...)
	if err != nil || !strings.Contains(out, "Success") {
		return fmt.Errorf("adb install %s: %v %s", apk, err, out)
	}
	adb(15*time.Second, "shell", "pm", "grant", a.Package, "android.permission.POST_NOTIFICATIONS")
	return nil
}

// Launch brings the app to the front, as tapping its icon does.
func (a *Android) Launch() error {
	_, err := adb(30*time.Second, "shell", "monkey", "-p", a.Package, "-c", "android.intent.category.LAUNCHER", "1")
	time.Sleep(4 * time.Second)
	return err
}

// Home goes back to the home screen (the program list), relaunching the app if back
// leaves it.
func (a *Android) Home() ([]Node, error) {
	for range 8 {
		nodes, err := a.Screen()
		if err == nil {
			if _, ok := Find(nodes, "No filters applied"); ok {
				return nodes, nil
			}
			if n, ok := Find(nodes, "Discard changes"); ok {
				// leaving a form the app would not save ("Not saved")
				a.Tap(n)
				time.Sleep(2 * time.Second)
				continue
			}
			if _, ok := Find(nodes, "Allow access to tablet data"); ok {
				// Samsung's USB dialog over the launcher
				n, _ := Find(nodes, "Deny")
				a.Tap(n)
			}
		}
		if !a.inApp() {
			a.Launch()
			continue
		}
		a.Key("4")
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("home screen not reached")
}

func (a *Android) inApp() bool {
	out, _ := adb(15*time.Second, "shell", "dumpsys", "window")
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "mCurrentFocus") {
			return strings.Contains(l, a.Package)
		}
	}
	return false
}

// OpenProgram opens a program from the home screen.
func (a *Android) OpenProgram(program string) error {
	if _, err := a.Home(); err != nil {
		return err
	}
	nodes, err := a.ScrollTo(program, 15)
	if err != nil {
		return err
	}
	n, _ := Find(nodes, program)
	a.Tap(n)
	_, _, err = a.Wait(time.Minute, "Search / Add new", "Synthetic first name")
	return err
}

// below returns the first node of a class below the node whose text is label: the
// input of a labelled form field.
func below(nodes []Node, label, class string) (Node, bool) {
	l, ok := Find(nodes, label)
	if !ok {
		return Node{}, false
	}
	for _, n := range nodes {
		if strings.HasSuffix(n.Class, class) && n.Y1 >= l.Y1 && n.Y1 < l.Y2+80 {
			return n, true
		}
	}
	return Node{}, false
}

// lowest returns the node with exactly this text lowest on the screen: a button that
// shares its label with the screen's title ("Search").
func lowest(nodes []Node, text string) (Node, bool) {
	var out Node
	ok := false
	for _, n := range nodes {
		if n.Text == text && (!ok || n.Y1 > out.Y1) {
			out, ok = n, true
		}
	}
	return out, ok
}

// Registration is what the app showed of one person registered on the tablet.
type Registration struct {
	First, Last string
	Generated   string // the generated attribute's value as the form filled it in
	Saved       bool
	Message     string // what the app showed instead of the person's dashboard
}

// Register registers a person in program, as staff do: open the program, search by
// first name, create, fill in the incident date and the last name, save. generated
// is the label of a generated attribute to read from the form ("" for none). It
// returns to the home screen.
func (a *Android) Register(program, first, last, generated string) (Registration, error) {
	r := Registration{First: first, Last: last}
	if err := a.OpenProgram(program); err != nil {
		return r, err
	}
	// The list's "Search / Add new", or (seen once in 25) the search form already open.
	seen, nodes, err := a.Wait(time.Minute, "Search / Add new", "Synthetic first name")
	if err != nil {
		return r, err
	}
	if seen == "Search / Add new" {
		n, _ := Find(nodes, "Search / Add new")
		a.Tap(n)
		if _, nodes, err = a.Wait(time.Minute, "Synthetic first name"); err != nil {
			return r, err
		}
	}
	n, _ := Find(nodes, "Synthetic first name")
	a.Tap(n)
	time.Sleep(time.Second)
	a.Type(first)
	a.Key("111")
	time.Sleep(time.Second)
	if nodes, err = a.Screen(); err != nil {
		return r, err
	}
	if n, ok := lowest(nodes, "Search"); ok {
		a.Tap(n)
	}
	// The results, then the create button (no label) at the bottom right.
	if _, nodes, err = a.Wait(time.Minute, "No more results", "No results", "Search outside", "create"); err != nil {
		return r, err
	}
	w, h := a.size()
	tapped := false
	for _, n := range nodes {
		if strings.HasSuffix(n.Class, "Button") && n.Text == "" && n.X1 > w/2 && n.Y1 > h*3/4 {
			a.Tap(n)
			tapped = true
			break
		}
	}
	if !tapped {
		if n, ok := Find(nodes, "Create new"); ok {
			a.Tap(n)
		} else {
			return r, fmt.Errorf("no create button after the search")
		}
	}
	if _, nodes, err = a.Wait(time.Minute, "Incident date"); err != nil {
		return r, err
	}
	if n, ok := below(nodes, "Incident date", "EditText"); ok {
		a.Tap(n)
		time.Sleep(time.Second)
		a.Type(time.Now().Format("02012006"))
		a.Key("111")
		time.Sleep(time.Second)
	}
	if nodes, err = a.Screen(); err != nil {
		return r, err
	}
	if n, ok := lowest(nodes, "Next"); ok {
		a.Tap(n)
	}
	if _, nodes, err = a.Wait(30*time.Second, "Synthetic last name"); err != nil {
		return r, err
	}
	if n, ok := below(nodes, "Synthetic last name", "EditText"); ok {
		a.Tap(n)
		time.Sleep(time.Second)
		a.Type(last)
		a.Key("111")
		time.Sleep(time.Second)
	}
	if nodes, err = a.Screen(); err != nil {
		return r, err
	}
	if generated != "" {
		if n, ok := below(nodes, generated, "EditText"); ok {
			r.Generated = n.Text
		}
	}
	n, ok := Find(nodes, "Save")
	if !ok {
		// Before 3.3 the save button has no label: the round button at the bottom right.
		w, h := a.size()
		for _, x := range nodes {
			if (strings.HasSuffix(x.Class, "ImageButton") || strings.HasSuffix(x.Class, "FloatingActionButton")) && x.X1 > w*3/4 && x.Y1 > h*3/4 {
				n, ok = x, true
			}
		}
	}
	if !ok {
		return r, fmt.Errorf("no Save button on the form")
	}
	a.Tap(n)
	// The dashboard, or whatever the app shows instead (a dialog, the form again).
	time.Sleep(3 * time.Second)
	if _, nodes, err = a.Wait(20*time.Second, "Owned by"); err == nil {
		r.Saved = true
	} else {
		// What the app shows over the form, then the form itself under it.
		r.Message = Texts(nodes)
		a.Key("4")
		time.Sleep(2 * time.Second)
		if nodes, err := a.Screen(); err == nil {
			r.Message += " || " + Texts(nodes)
		}
	}
	_, err = a.Home()
	return r, err
}

// Texts joins the text on screen, for the record of what the app showed (I8).
func Texts(nodes []Node) string {
	var out []string
	for _, n := range nodes {
		if s := strings.TrimSpace(n.Text); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " | ")
}

// SyncAll taps the home screen's Sync and sends every change, or downloads when there
// is nothing to send. It returns once the app has started; the caller waits for the
// data where it should land.
func (a *Android) SyncAll() error {
	nodes, err := a.Home()
	if err != nil {
		return err
	}
	// The toolbar's Sync button, by its description: Find would also match the
	// "SYNC" filter.
	var n Node
	ok := false
	for _, x := range nodes {
		if x.Desc == "Sync" {
			n, ok = x, true
			break
		}
	}
	if !ok {
		// Before 3.3 the toolbar's icons have no labels: Sync is the left one of the
		// two on the right (Sync, Filters).
		w, h := a.size()
		for _, x := range nodes {
			if x.Text == "" && x.Desc == "" && strings.HasSuffix(x.Class, "ImageView") && x.Y2 < h/10 && x.X1 > w/2 && (!ok || x.X1 < n.X1) {
				n, ok = x, true
			}
		}
	}
	if !ok {
		return fmt.Errorf("no Sync on the home screen")
	}
	a.Tap(n)
	// "Sync needed … Send" when there are changes to send; "Synced … check for
	// updates? Refresh" when there are none.
	if seen, nodes, err := a.Wait(30*time.Second, "Send", "Refresh"); err == nil {
		n, _ = Find(nodes, seen)
		a.Tap(n)
	}
	return nil
}

// Logout logs out from the home screen's menu.
func (a *Android) Logout() error {
	if _, err := a.Home(); err != nil {
		return err
	}
	_, nodes, err := a.Wait(15*time.Second, "menu")
	if err != nil {
		return err
	}
	n, _ := Find(nodes, "menu")
	a.Tap(n)
	if _, nodes, err = a.Wait(15*time.Second, "Log Out"); err != nil {
		return err
	}
	n, _ = Find(nodes, "Log Out")
	a.Tap(n)
	// The login screen of the account, or the list of accounts when there are several.
	_, _, err = a.Wait(time.Minute, "Manage accounts", "Server URL", "Add new account")
	return err
}

// AddAccount adds an account on another server from the login screen (Manage
// accounts, Add new account), and logs in as Login does.
func (a *Android) AddAccount(server, user, password, program string, timeout time.Duration) ([]Node, error) {
	seen, nodes, err := a.Wait(time.Minute, "Manage accounts", "Add new account", "Server URL")
	if err != nil {
		return nil, err
	}
	if seen == "Manage accounts" {
		n, _ := Find(nodes, "Manage accounts")
		a.Tap(n)
		if _, nodes, err = a.Wait(30*time.Second, "Add new account"); err != nil {
			return nil, err
		}
		seen = "Add new account"
	}
	if seen == "Add new account" {
		n, _ := Find(nodes, "Add new account")
		a.Tap(n)
	}
	return a.Login(server, user, password, program, timeout)
}

// SwitchAccount logs in again to an account the app holds, from the login screen
// (Manage accounts, the account's server).
func (a *Android) SwitchAccount(server, password, program string, timeout time.Duration) ([]Node, error) {
	seen, nodes, err := a.Wait(time.Minute, "Add new account", "Manage accounts")
	if err != nil {
		return nil, err
	}
	if seen == "Manage accounts" {
		n, _ := Find(nodes, "Manage accounts")
		a.Tap(n)
		if _, nodes, err = a.Wait(30*time.Second, server); err != nil {
			return nil, err
		}
	}
	n, ok := Find(nodes, server)
	if !ok {
		return nil, fmt.Errorf("no account on %s: %s", server, Texts(nodes))
	}
	a.Tap(n)
	if _, nodes, err = a.Wait(30*time.Second, "Password"); err != nil {
		return nil, err
	}
	f, ok := below(nodes, "Password", "EditText")
	if !ok {
		return nil, fmt.Errorf("no password field")
	}
	a.Tap(f)
	a.Type(password)
	a.Key("111")
	if _, nodes, err = a.Wait(30*time.Second, "Log in"); err != nil {
		return nil, err
	}
	n, _ = lowest(nodes, "Log in")
	a.Tap(n)
	deadline := time.Now().Add(timeout)
	seen, nodes, err = a.Wait(time.Until(deadline), "No filters applied", "Wrong", "error", "failed")
	if err != nil {
		return nodes, err
	}
	if seen != "No filters applied" {
		return nodes, fmt.Errorf("login: %q on screen", seen)
	}
	return a.ScrollTo(program, 15)
}
