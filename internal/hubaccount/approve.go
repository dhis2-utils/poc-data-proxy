package hubaccount

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

// Approve gives every hub that asked for one an account of its own, and returns their
// IDs. For each, it:
//   - creates the user <hub ID>, assigned to the hub's facility only (capture, data
//     view and search), so anything public on central is still limited to it;
//   - writes a random move secret to facility-hub-accounts/<hub ID>, readable by that
//     account only (the typed-in check of a central move);
//   - puts the credentials and the secret in the hub's registry entry, encrypted to
//     the hub's key.
//
// The hub takes the account up at its next catalog refresh.
func Approve(ctx context.Context, o Options) ([]string, error) {
	c := &client{o: o, http: o.HTTP}
	if c.http == nil {
		c.http = &http.Client{Timeout: time.Minute}
	}
	var reg struct {
		Entries []struct {
			Key   string         `json:"key"`
			Value map[string]any `json:"value"`
		} `json:"entries"`
	}
	if err := c.do(ctx, "GET", "/api/dataStore/facility-hub-registry?fields=.&paging=false", nil, &reg); err != nil {
		return nil, fmt.Errorf("reading the registry: %w", err)
	}
	var done []string
	for _, e := range reg.Entries {
		v := e.Value
		pub, _ := base64.StdEncoding.DecodeString(fmt.Sprint(v["boxKey"]))
		if v["accountRequested"] != true || v["status"] != "active" || v["account"] != nil || len(pub) != 32 {
			continue
		}
		facility := fmt.Sprint(v["facility"])
		creds := Credentials{Username: e.Key, Password: password(), MoveSecret: token(32)}
		uid := token(11)
		uid = "H" + uid[1:] // a DHIS2 uid starts with a letter
		ou := []map[string]string{{"id": facility}}
		user := map[string]any{"id": uid, "username": creds.Username, "password": creds.Password, "firstName": "Facility hub",
			"surname": e.Key, "organisationUnits": ou, "dataViewOrganisationUnits": ou, "teiSearchOrganisationUnits": ou,
			"userRoles": []map[string]string{{"id": o.Role}}}
		if err := c.do(ctx, "POST", "/api/users", user, nil); err != nil {
			return done, fmt.Errorf("creating the account of %s: %w", e.Key, err)
		}
		if err := c.do(ctx, "POST", "/api/userGroups/"+o.Group+"/users/"+uid, nil, nil); err != nil {
			return done, fmt.Errorf("adding %s to the hub accounts group: %w", e.Key, err)
		}
		secretPath := "/api/dataStore/facility-hub-accounts/" + url.PathEscape(e.Key)
		if err := c.put(ctx, secretPath, map[string]string{"moveSecret": creds.MoveSecret}); err != nil {
			return done, fmt.Errorf("writing the move secret of %s: %w", e.Key, err)
		}
		var meta struct {
			ID string `json:"id"`
		}
		if err := c.do(ctx, "GET", secretPath+"/metaData", nil, &meta); err != nil {
			return done, err
		}
		share := map[string]any{"object": map[string]any{"publicAccess": "--------",
			"userAccesses": []map[string]string{{"id": uid, "access": "r-------"}}}}
		if err := c.do(ctx, "POST", "/api/sharing?type=dataStore&id="+meta.ID, share, nil); err != nil {
			return done, fmt.Errorf("limiting the move secret of %s to its account: %w", e.Key, err)
		}
		plain, _ := json.Marshal(creds)
		box, err := Seal(pub, plain, e.Key)
		if err != nil {
			return done, err
		}
		v["account"] = map[string]string{"username": creds.Username, "box": base64.StdEncoding.EncodeToString(box)}
		if err := c.put(ctx, "/api/dataStore/facility-hub-registry/"+url.PathEscape(e.Key), v); err != nil {
			return done, fmt.Errorf("handing the account to %s: %w", e.Key, err)
		}
		done = append(done, e.Key)
	}
	return done, nil
}

type client struct {
	o    Options
	http *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
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
		req.Header.Set("Content-Type", "application/json")
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

// password meets DHIS2's rules: upper and lower case, a digit and a special character.
func password() string { return "Hub-" + token(28) + "9a!" }
