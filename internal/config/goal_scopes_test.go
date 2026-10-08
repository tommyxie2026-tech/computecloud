package config

import "testing"

func TestGoalScopesAcceptedForConfiguredIdentity(t *testing.T) {
	for _, scope := range []string{"goals:approve", "goals:budget", "goals:constraints", "goals:propose"} {
		t.Run(scope, func(t *testing.T) {
			c := Server{Users: []Identity{{Scopes: []string{"jobs:submit", "jobs:read", "jobs:control", scope}}}}
			c.DefaultV02()
			if err := c.ValidateV02(); err != nil {
				t.Fatalf("configured Goal scope rejected: %v", err)
			}
		})
	}
}
