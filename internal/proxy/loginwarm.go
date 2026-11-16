package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
)

// The login page is kept as soon as the hub reaches central, not only once a device
// has loaded it online: staff at a new hub, after a reset, or the one setting it up
// may open it first offline, and a hub without it cannot be logged in to, or unlocked,
// until the internet is back (FINDINGS.md, "The demo of 13 November 2026"). The hub loads it
// as a browser does, without a login: the page, then what its HTML, scripts and styles
// name by relative path under the login app's path, then the settings, logo and flag
// the page asks the API for. About 1.1 MB on 2.42 and 2.43; once a day, so a new login
// app reaches the hub within a day of central's upgrade.
var (
	loginWarmDelay = 30 * time.Second // after the hub starts: let set-up and the first sync go first
	loginWarmEvery = 24 * time.Hour
	loginWarmRetry = 10 * time.Minute
)

const (
	loginWarmMaxFiles = 120
	loginRoot         = "/dhis-web-login/"
)

// relRef: a relative reference in HTML (src, href), CSS (url()) or a script's import,
// to a file of a kind the login page loads. Fonts in WOFF2 only: browsers that run the
// apps all take it, and the CSS names a WOFF fallback for each.
var relRef = regexp.MustCompile(`["'(](\.{1,2}/[A-Za-z0-9._/-]+\.(?:js|css|woff2|png|svg|ico|json|webmanifest))["')]`)

// RunLoginWarm keeps the login page in the public file while the hub runs.
func (p *Proxy) RunLoginWarm(ctx context.Context) {
	t := time.NewTimer(loginWarmDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		next := time.Minute
		if p.H.Online() && p.loginWarmDue() {
			start := time.Now()
			n, kb, err := p.WarmLoginPage(ctx)
			slog.Info("login page kept for offline use", "files", n, "kB", kb, "s", int(time.Since(start).Seconds()), "err", err)
			if err != nil || n == 0 {
				next = loginWarmRetry
			}
		}
		t.Reset(next)
	}
}

// loginWarmDue: the page's copy is missing or older than a day.
func (p *Proxy) loginWarmDue() bool {
	c, err := p.H.Store.GetPublic(loginRoot)
	return err == nil && (c == nil || time.Since(store.ParseTime(c.FetchedAt)) > loginWarmEvery)
}

// WarmLoginPage loads the login page and what it loads into the public file; it
// returns how many answers it kept and their size in kB.
func (p *Proxy) WarmLoginPage(ctx context.Context) (n, kb int, err error) {
	queue := []string{loginRoot, "/api/loginConfig"}
	seen := map[string]bool{}
	bytes := 0
	defer func() { kb = bytes / 1024 }()
	for len(queue) > 0 && len(seen) < loginWarmMaxFiles {
		uri := queue[0]
		queue = queue[1:]
		u, perr := url.Parse(uri)
		if perr != nil || seen[uri] || !publicAnswer(u) {
			continue
		}
		seen[uri] = true
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		r, _ := http.NewRequestWithContext(rctx, http.MethodGet, uri, nil)
		res, ferr := p.Forward(rctx, r, nil, central.Auth{})
		cancel()
		if ferr != nil {
			if central.IsNetwork(ferr) {
				return n, 0, ferr
			}
			continue
		}
		switch res.Status {
		case http.StatusOK, http.StatusMovedPermanently, http.StatusFound:
		default:
			continue
		}
		if len(res.Body) > maxCacheBody {
			continue
		}
		if err := p.H.Store.PutPublic(uri, &store.CachedResponse{Status: res.Status, Header: res.Header, Body: res.Body}); err != nil {
			return n, 0, err
		}
		n++
		bytes += len(res.Body)
		if res.Status != http.StatusOK {
			// 2.43: /dhis-web-login/ → /login/; the logo → its file.
			if loc := p.H.Central.Local(res.Header.Get("Location")); strings.HasPrefix(loc, "/") {
				queue = append(queue, loc)
			}
			continue
		}
		if strings.TrimPrefix(u.Path, "/api") != u.Path {
			queue = append(queue, apiRefs(u.Path, res.Body)...)
			continue
		}
		for _, m := range relRef.FindAllSubmatch(res.Body, -1) {
			if ref, err := u.Parse(string(m[1])); err == nil {
				queue = append(queue, ref.Path)
			}
		}
	}
	return n, 0, nil
}

// apiRefs: what the login page asks the API for once it has central's login settings
// (seen on 2.42.8 and 2.43.2.1): the settings again under the API version, the UI
// languages, the logo and the country's flag.
func apiRefs(path string, body []byte) []string {
	if path != "/api/loginConfig" {
		return nil
	}
	var cfg struct {
		APIVersion  string `json:"apiVersion"`
		CountryFlag string `json:"countryFlag"`
	}
	json.Unmarshal(body, &cfg)
	refs := []string{"/api/staticContent/logo_front.png"}
	if parts := strings.Split(cfg.APIVersion, "."); len(parts) > 1 && regexp.MustCompile(`^[0-9]{2}$`).MatchString(parts[1]) {
		v := "/api/" + parts[1]
		refs = append(refs, v+"/loginConfig", v+"/loginConfig?paging=false", v+"/locales/ui")
	}
	if regexp.MustCompile(`^[a-z_]{1,40}$`).MatchString(cfg.CountryFlag) {
		refs = append(refs, "/dhis-web-commons/flags/"+cfg.CountryFlag+".png")
	}
	return refs
}
