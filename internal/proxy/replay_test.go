package proxy

import "testing"

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
