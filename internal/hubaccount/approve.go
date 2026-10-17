package hubaccount

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
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

// The national team approves hubs in the Facility Hub app on central
// (docs/FACILITY-HUB-APP.md); Approve is the reference the app must match, and what
// the hub's tests approve with.

// Credentials are what the box carries to the hub.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	MoveKey  []byte `json:"moveKey"` // base64 in JSON
}

// Options for Approve: central, an account allowed to create users, and the user
// group (write access to the facility-hub namespaces) and the user role with no
// authorities that hub accounts get.
type Options struct {
	Central, Username, Password string
	Group, Role                 string
	HTTP                        *http.Client
}

// Request is one hub to approve, with the code the facility read from the hub's page.
type Request struct {
	HubID, Code string
}

// Approve gives each requested hub an account of its own, and returns the IDs of those
// done; the others are in the error, and do not stop the rest. For each, it:
//   - checks the code against the key in the hub's registry entry (AccountCode):
//     anyone in the hub owners group can write the entry, but not the hub's screen;
//   - creates the user <hub ID>, assigned to the hub's facility only (capture, data
//     view and search), so anything public on central is still limited to it, or, for
//     a hub approved before, gives that user a new password;
//   - makes a new move key, kept on central in facility-hub-keys/<hub ID>, created
//     afresh so that its owner is the approver, and shared with no one: only
//     administrators read it (move codes, MoveCode);
//   - puts the credentials and the move key in the hub's registry entry, encrypted to
//     the hub's key.
//
// The hub takes the account up at its next catalog refresh, and replaces the
// password and token at once.
func Approve(ctx context.Context, o Options, reqs []Request) ([]string, error) {
	c := &client{o: o, http: o.HTTP}
	if c.http == nil {
		c.http = &http.Client{Timeout: time.Minute}
	}
	var done []string
	var errs []error
	for _, r := range reqs {
		if err := c.approve(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.HubID, err))
			continue
		}
		done = append(done, r.HubID)
	}
	return done, errors.Join(errs...)
}

func (c *client) approve(ctx context.Context, r Request) error {
	id := r.HubID
	regPath := "/api/dataStore/facility-hub-registry/" + url.PathEscape(id)
	var v map[string]any
	if err := c.do(ctx, "GET", regPath, nil, &v); err != nil {
		return fmt.Errorf("reading its registry entry: %w", err)
	}
	pub, _ := base64.StdEncoding.DecodeString(str(v["boxKey"]))
	facility := str(v["facility"])
	switch {
	case v["accountRequested"] != true || v["account"] != nil:
		return errors.New("not waiting for an account")
	case v["status"] != "active":
		return errors.New("not active")
	case facility == "":
		return errors.New("no facility")
	case len(pub) != 32:
		return errors.New("no key to hand the account over with")
	case subtle.ConstantTimeCompare([]byte(NormalizeCode(AccountCode(pub, id, facility))), []byte(NormalizeCode(r.Code))) != 1:
		return errors.New("the code is not the one for this hub's key and facility: ask the facility to read it again from the hub's page")
	}
	moveKey := make([]byte, 32)
	rand.Read(moveKey)
	creds := Credentials{Username: id, Password: NewPassword(), MoveKey: moveKey}
	uid, err := c.account(ctx, creds, facility)
	if err != nil {
		return err
	}
	if err := c.do(ctx, "POST", "/api/userGroups/"+c.o.Group+"/users/"+uid, nil, nil); err != nil {
		return fmt.Errorf("adding it to the hub accounts group: %w", err)
	}
	keyPath := "/api/dataStore/facility-hub-keys/" + url.PathEscape(id)
	if err := c.do(ctx, "DELETE", keyPath, nil, nil); err != nil && !strings.Contains(err.Error(), ": 404 ") {
		return fmt.Errorf("removing its old move key: %w", err)
	}
	// A new entry is public (read and write) until its sharing is set: the key goes in
	// only after.
	if err := c.do(ctx, "POST", keyPath, map[string]string{}, nil); err != nil {
		return fmt.Errorf("making the entry for its move key: %w", err)
	}
	var meta struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, "GET", keyPath+"/metaData", nil, &meta); err != nil {
		return err
	}
	share := map[string]any{"object": map[string]any{"publicAccess": "--------", "userAccesses": []any{}, "userGroupAccesses": []any{}}}
	if err := c.do(ctx, "POST", "/api/sharing?type=dataStore&id="+meta.ID, share, nil); err != nil {
		return fmt.Errorf("limiting its move key to administrators: %w", err)
	}
	if err := c.do(ctx, "PUT", keyPath, map[string]string{"moveKey": base64.StdEncoding.EncodeToString(moveKey)}, nil); err != nil {
		return fmt.Errorf("keeping its move key: %w", err)
	}
	plain, _ := json.Marshal(creds)
	box, err := Seal(pub, plain, id)
	if err != nil {
		return err
	}
	// Read again just before writing, and change only the account: the hub writes its
	// entry too.
	var now map[string]any
	if err := c.do(ctx, "GET", regPath, nil, &now); err != nil {
		return err
	}
	if str(now["boxKey"]) != str(v["boxKey"]) || str(now["facility"]) != facility || now["account"] != nil {
		return errors.New("its entry changed meanwhile (another approval?); ask the facility for the code again")
	}
	now["account"] = map[string]string{"username": id, "box": base64.StdEncoding.EncodeToString(box)}
	if err := c.do(ctx, "PUT", regPath, now, nil); err != nil {
		return fmt.Errorf("handing the account over: %w", err)
	}
	return nil
}

type ref struct {
	ID          string   `json:"id"`
	Authorities []string `json:"authorities"`
}

// account creates the hub's user, or gives the existing one (approved before) a new
// password. An existing account the hub would refuse is not handed over again: the
// approver is told what to correct (docs/FACILITY-HUB-APP.md, step 3).
func (c *client) account(ctx context.Context, creds Credentials, facility string) (string, error) {
	var role ref
	if err := c.do(ctx, "GET", "/api/userRoles/"+url.PathEscape(c.o.Role)+"?fields=id,authorities", nil, &role); err != nil {
		return "", fmt.Errorf("reading the hub account role: %w", err)
	}
	if len(role.Authorities) > 0 {
		return "", fmt.Errorf("the hub account role has authorities (%s): it must have none", strings.Join(role.Authorities, ", "))
	}
	var us struct {
		Users []struct {
			ID                         string `json:"id"`
			Disabled                   bool   `json:"disabled"`
			OrganisationUnits          []ref  `json:"organisationUnits"`
			DataViewOrganisationUnits  []ref  `json:"dataViewOrganisationUnits"`
			TeiSearchOrganisationUnits []ref  `json:"teiSearchOrganisationUnits"`
			UserRoles                  []ref  `json:"userRoles"`
		} `json:"users"`
	}
	if err := c.do(ctx, "GET", "/api/users?filter=username:eq:"+url.QueryEscape(creds.Username)+
		"&fields=id,disabled,organisationUnits[id],dataViewOrganisationUnits[id],teiSearchOrganisationUnits[id],userRoles[id,authorities]&paging=false", nil, &us); err != nil {
		return "", err
	}
	if len(us.Users) == 1 {
		u := us.Users[0]
		facilityOnly := func(ous []ref, mayBeEmpty bool) bool {
			return (mayBeEmpty && len(ous) == 0) || (len(ous) == 1 && ous[0].ID == facility)
		}
		var auths []string
		for _, r := range u.UserRoles {
			auths = append(auths, r.Authorities...)
		}
		switch {
		case u.Disabled:
			return "", errors.New("its existing account is disabled: enable it first if the hub should have it back")
		case !facilityOnly(u.OrganisationUnits, false) || !facilityOnly(u.DataViewOrganisationUnits, true) || !facilityOnly(u.TeiSearchOrganisationUnits, true):
			return "", errors.New("its existing account is not assigned to its facility only (capture, data view and search): correct it, then approve again")
		case len(auths) > 0:
			return "", fmt.Errorf("its existing account has authorities (%s): give it only the hub account role, then approve again", strings.Join(auths, ", "))
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

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
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
