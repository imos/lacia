package routine

import "testing"

func TestCoverageValidationAndLegacyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result Result
		valid  bool
	}{
		{"old zero", Result{Nothing: true}, true},
		{"complete without evidence", Result{CheckStatus: "complete", Nothing: true}, false},
		{"old findings", Result{Matters: []Matter{{Key: "one", Revision: "1"}}}, true},
		{"old partial", Result{Unverified: []string{"private-repo"}}, true},
		{"complete contradicts unread scope", Result{CheckStatus: "complete", Unverified: []string{"one"}}, false},
		{"unavailable contradicts new matters", Result{CheckStatus: "unavailable", Unverified: []string{"one"}, Matters: []Matter{{Key: "one", Revision: "1"}}}, false},
		{"unknown status", Result{CheckStatus: "unknown"}, false},
		{"duplicate scope", Result{CheckStatus: "partial", Checks: []Check{{Scope: "one", Status: "verified"}, {Scope: "one", Status: "unverified"}}}, false},
		{"bad version", Result{Version: 99}, false},
		{"partial checks", Result{CheckStatus: "partial", Checks: []Check{{Scope: "one", Status: "verified"}, {Scope: "two", Status: "unverified"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.result.NormalizeChecks()
			if (err == nil) != tc.valid {
				t.Fatalf("%+v err=%v", tc.result, err)
			}
			if tc.valid {
				if err := tc.result.NormalizeChecks(); err != nil {
					t.Fatalf("normalization is not idempotent: %v", err)
				}
			}
		})
	}
}
