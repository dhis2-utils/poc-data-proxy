package proxy

import (
	"net/url"
	"testing"
)

func TestReplayable(t *testing.T) {
	others := []string{"OtherUser01"}
	for uri, want := range map[string]bool{
		"/api/programs?fields=id,displayName&paging=false":  true,
		"/api/42/optionSets/AbCdEfGhIjK?fields=options[id]": true,
		"/api/userSettings":                         true,
		"/api/me/dashboard":                         true,
		"/api/dataStore/capture/ruleEngine":         true,
		"/api/me?fields=id,settings":                false, // answered from the copy kept at login
		"/api/42/me":                                false,
		"/api/loginConfig":                          false, // shared already
		"/dhis-web-capture/index.html":              false, // static, shared already
		"hub:/api/me":                               false,
		"/api/42/tracker/trackedEntities?program=x": false,
		"/api/trackedEntityAttributes/AbCdEfGhIjK/generate?ORG=1":      false,
		"/api/trackedEntityAttributes/AbCdEfGhIjK?fields=id":           true,
		"/api/43/dataEntry/dataValues?ds=x":                            false,
		"/api/dataValueSets.json?dataSet=x":                            false,
		"/api/analytics?dimension=dx:x":                                false,
		"/api/42/validation/dataSet/x":                                 false,
		"/api/users/OtherUser01?fields=id":                             false,
		"/api/organisationUnits?filter=user.id:eq:OtherUser01":         false,
		"/api/userDataStore/capture/x?user=someone":                    false,
		"/api/fileResources/AbCdEfGhIjK/data":                          false,
		"/api/42/audits/trackedEntityAttributeValue?trackedEntity=abc": false,
	} {
		if got := replayable(uri, others); got != want {
			t.Errorf("replayable(%s) = %v, want %v", uri, got, want)
		}
	}
}

// Only the login page's files, images and shared settings are public copies, and not
// under a query string of the client's choosing (review R-16, R-18).
func TestPublicAnswer(t *testing.T) {
	for uri, want := range map[string]bool{
		"/dhis-web-login/":                          true,
		"/dhis-web-login/assets/app-5xT9c-9H.js":    true,
		"/dhis-web-login/assets/app.js?x=1":         false,
		"/login/":                                   true,
		"/login/assets/main-Cdde16Q7.js":            true,
		"/dhis-web-commons/flags/sierra_leone.png":  true,
		"/dhis-web-commons/security/logo_front.png": true,
		"/dhis-web-commons-security/logout.action":  false,
		"/api/staticContent/logo_front.png":         true,
		"/api/loginConfig":                          true,
		"/api/42/loginConfig?paging=false":          true,
		"/api/42/locales/ui":                        true,
		"/api/staticContent/logo_front":             true,
		"/api/loginConfig?x=2":                      false,
		"/dhis-web-capture/index.html":              false,
		"/apps/capture":                             false,
		"/api/apps/capture/index.html":              false,
		"/api/me":                                   false,
	} {
		u, _ := url.Parse(uri)
		if got := publicAnswer(u); got != want {
			t.Errorf("publicAnswer(%s) = %v, want %v", uri, got, want)
		}
	}
}
