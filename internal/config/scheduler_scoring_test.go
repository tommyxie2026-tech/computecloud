package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPreparedWorkspaceScoringDefaultsOffAndCanBeExplicitlyEnabled(t *testing.T) {
	var defaultConfig Server
	defaultConfig.DefaultV02()
	if defaultConfig.PreparedWorkspaceScoreEnabled {
		t.Fatal("scheduler scoring must default to disabled")
	}
	var enabled Server
	if err := yaml.NewDecoder(strings.NewReader("prepared_workspace_score_enabled: true\n")).Decode(&enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled.PreparedWorkspaceScoreEnabled {
		t.Fatal("explicit scheduler scoring opt-in was ignored")
	}
}

func TestInvalidPreparedWorkspaceScoringConfigFailsClosed(t *testing.T) {
	var config Server
	err := yaml.NewDecoder(strings.NewReader("prepared_workspace_score_enabled: maybe\n")).Decode(&config)
	if err == nil {
		t.Fatal("invalid boolean setting must reject the config rather than silently enabling or disabling scheduling")
	}
}
