package hubaccount

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Credentials are what the box carries to the hub.
type Credentials struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	MoveSecret string `json:"moveSecret"`
}

// Options for Approve: central, an account allowed to create users, and the user
// group (write access to the facility-hub namespaces, read on its own secret) and the
// minimal user role the hub accounts get.
type Options struct {
	Central, Username, Password string
	Group, Role                 string
	HTTP                        *http.Client
}

// PendingHub is a hub waiting for an account; Problem says why it may not have one.
type PendingHub struct {
	HubID, Facility, FacilityName, SetupBy, SetupAt, Problem string
}

type entry struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

// Pending lists the hubs waiting for an account. Anyone in the hub owners group can
// write the registry, so an entry proves little by itself: each is checked as
// Approve will check it, and the person approving confirms the rest (that the hub
// exists, and that this person set it up) outside the hub.
func Pending(ctx context.Context, o Options) ([]PendingHub, error) {
	c := newClient(o)
	entries, err := c.registry(ctx)
	if err != nil {
		return nil, err
	}
	var out []PendingHub
	for _, e := range entries {
		v := e.Value
		if v["accountRequested"] != true || v["account"] != nil {
			continue
		}
		p := PendingHub{HubID: e.Key, Facility: str(v["facility"]), FacilityName: str(v["facilityName"]),
			SetupBy: str(v["setupBy"]), SetupAt: str(v["setupAt"])}
		if err := c.check(ctx, e); err != nil {
			p.Problem = err.Error()
		}
		out = append(out, p)
	}
	return out, nil
}

// Approve gives each named hub an account of its own, and returns the IDs of those
// done; the others are in the error, and do not stop the rest. For each, it:
//   - checks the registry entry (check);
//   - creates the user <hub ID>, assigned to the hub's facility only (capture, data
//     view and search), so anything public on central is still limited to it, or, for
//     a hub approved before, gives that user a new password;
//   - writes a new move secret to facility-hub-accounts/<hub ID>, shared with that
//     account only (the typed-in check of a central move);
//   - puts the credentials and the secret in the hub's registry entry, encrypted to
//     the hub's key.
//
// The hub takes the account up at its next catalog refresh, and replaces the
// password, token and move secret at once.
func Approve(ctx context.Context, o Options, hubIDs []string) ([]string, error) {
	c := newClient(o)
	var done []string
	var errs []error
	for _, id := range hubIDs {
		if err := c.approve(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		done = append(done, id)
	}
	return done, errors.Join(errs...)
}

func (c *client) approve(ctx context.Context, id string) error {
	regPath := "/api/dataStore/facility-hub-registry/" + url.PathEscape(id)
	e := entry{Key: id}
	if err := c.do(ctx, "GET", regPath, nil, &e.Value); err != nil {
		return fmt.Errorf("reading its registry entry: %w", err)
	}
	if e.Value["accountRequested"] != true || e.Value["account"] != nil {
		return errors.New("not waiting for an account")
	}
	if err := c.check(ctx, e); err != nil {
		return err
	}
	pub, _ := base64.StdEncoding.DecodeString(str(e.Value["boxKey"]))
	facility := str(e.Value["facility"])
	creds := Credentials{Username: id, Password: NewPassword(), MoveSecret: NewSecret()}
	uid, err := c.account(ctx, creds, facility)
	if err != nil {
		return err
	}
	if err := c.do(ctx, "POST", "/api/userGroups/"+c.o.Group+"/users/"+uid, nil, nil); err != nil {
		return fmt.Errorf("adding it to the hub accounts group: %w", err)
	}
	secretPath := "/api/dataStore/facility-hub-accounts/" + url.PathEscape(id)
	if err := c.put(ctx, secretPath, map[string]string{"moveSecret": creds.MoveSecret}); err != nil {
		return fmt.Errorf("writing its move secret: %w", err)
	}
	var meta struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, "GET", secretPath+"/metaData", nil, &meta); err != nil {
		return err
	}
	// Read and write for the account, which replaces the secret; no one else.
	share := map[string]any{"object": map[string]any{"publicAccess": "--------",
		"userAccesses": []map[string]string{{"id": uid, "access": "rw------"}}}}
	if err := c.do(ctx, "POST", "/api/sharing?type=dataStore&id="+meta.ID, share, nil); err != nil {
		return fmt.Errorf("limiting its move secret to its account: %w", err)
	}
	plain, _ := json.Marshal(creds)
	box, err := Seal(pub, plain, id)
	if err != nil {
		return err
	}
	// Read again just before writing, and change only the account: the hub writes its
	// entry too.
	var v map[string]any
	if err := c.do(ctx, "GET", regPath, nil, &v); err != nil {
		return err
	}
	if str(v["boxKey"]) != str(e.Value["boxKey"]) {
		return errors.New("its key changed meanwhile; list the hubs again")
	}
	v["account"] = map[string]string{"username": id, "box": base64.StdEncoding.EncodeToString(box)}
	if err := c.do(ctx, "PUT", regPath, v, nil); err != nil {
		return fmt.Errorf("handing the account over: %w", err)
	}
	return nil
}

// check refuses an entry that is not plausibly a hub set up by a staff member of its
// facility: an active entry with a facility and a key, created on central by the
// staff member it names as having set it up, who is assigned to that facility.
func (c *client) check(ctx context.Context, e entry) error {
	v := e.Value
	pub, _ := base64.StdEncoding.DecodeString(str(v["boxKey"]))
	facility, setupBy := str(v["facility"]), str(v["setupBy"])
	switch {
	case v["status"] != "active":
		return errors.New("not active")
	case facility == "":
		return errors.New("no facility")
	case len(pub) != 32:
		return errors.New("no key to hand the account over with")
	case setupBy == "":
		return errors.New("no one recorded as having set it up")
	}
	var meta struct {
		CreatedBy struct {
			Username string `json:"username"`
		} `json:"createdBy"`
	}
	if err := c.do(ctx, "GET", "/api/dataStore/facility-hub-registry/"+url.PathEscape(e.Key)+"/metaData", nil, &meta); err != nil {
		return err
	}
	if meta.CreatedBy.Username != setupBy {
		return fmt.Errorf("the entry names %s as having set it up, but %s created it", setupBy, meta.CreatedBy.Username)
	}
	var us struct {
		Users []struct {
			OrganisationUnits []struct {
				ID string `json:"id"`
			} `json:"organisationUnits"`
		} `json:"users"`
	}
	if err := c.do(ctx, "GET", "/api/users?filter=username:eq:"+url.QueryEscape(setupBy)+"&fields=organisationUnits[id]&paging=false", nil, &us); err != nil {
		return err
	}
	for _, u := range us.Users {
		for _, o := range u.OrganisationUnits {
			if o.ID == facility {
				return nil
			}
		}
	}
	return fmt.Errorf("%s, who set it up, is not assigned to its facility %s", setupBy, facility)
}

// account creates the hub's user, or gives the existing one (approved before) a new
// password; either way assigned to the facility only.
func (c *client) account(ctx context.Context, creds Credentials, facility string) (string, error) {
	var us struct {
		Users []struct {
			ID                string `json:"id"`
			OrganisationUnits []struct {
				ID string `json:"id"`
			} `json:"organisationUnits"`
		} `json:"users"`
	}
	if err := c.do(ctx, "GET", "/api/users?filter=username:eq:"+url.QueryEscape(creds.Username)+"&fields=id,organisationUnits[id]&paging=false", nil, &us); err != nil {
		return "", err
	}
	if len(us.Users) == 1 {
		u := us.Users[0]
		if len(u.OrganisationUnits) != 1 || u.OrganisationUnits[0].ID != facility {
			return "", errors.New("its existing account is not assigned to its facility only")
		}
		patch := []map[string]string{{"op": "replace", "path": "/password", "value": creds.Password}}
		if err := c.doType(ctx, "PATCH", "/api/users/"+u.ID, patch, nil, "application/json-patch+json"); err != nil {
			return "", fmt.Errorf("giving its existing account a new password: %w", err)
		}
		return u.ID, nil
	}
	uid := token(11)
	uid = "H" + uid[1:] // a DHIS2 uid starts with a letter
	ou := []map[string]string{{"id": facility}}
	user := map[string]any{"id": uid, "username": creds.Username, "password": creds.Password, "firstName": "Facility hub",
		"surname": creds.Username, "organisationUnits": ou, "dataViewOrganisationUnits": ou, "teiSearchOrganisationUnits": ou,
		"userRoles": []map[string]string{{"id": c.o.Role}}}
	if err := c.do(ctx, "POST", "/api/users", user, nil); err != nil {
		return "", fmt.Errorf("creating its account: %w", err)
	}
	return uid, nil
}

func (c *client) registry(ctx context.Context) ([]entry, error) {
	var reg struct {
		Entries []entry `json:"entries"`
	}
	if err := c.do(ctx, "GET", "/api/dataStore/facility-hub-registry?fields=.&paging=false", nil, &reg); err != nil {
		return nil, fmt.Errorf("reading the registry: %w", err)
	}
	return reg.Entries, nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func newClient(o Options) *client {
	c := &client{o: o, http: o.HTTP}
	if c.http == nil {
		c.http = &http.Client{Timeout: time.Minute}
	}
	return c
}

type client struct {
	o    Options
	http *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doType(ctx, method, path, body, out, "application/json")
}

func (c *client) doType(ctx context.Context, method, path string, body, out any, contentType string) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.o.Central, "/")+path, rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.o.Username, c.o.Password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %.300s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// put writes a dataStore entry, creating it if it is missing.
func (c *client) put(ctx context.Context, path string, v any) error {
	err := c.do(ctx, "PUT", path, v, nil)
	if err != nil && strings.Contains(err.Error(), ": 404 ") {
		err = c.do(ctx, "POST", path, v, nil)
	}
	return err
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func token(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}

// NewPassword meets DHIS2's rules: upper and lower case, a digit and a special
// character.
func NewPassword() string { return "Hub-" + token(28) + "9a!" }

// NewSecret is a move secret.
func NewSecret() string { return token(32) }
