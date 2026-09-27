package worker

import (
	"reflect"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	toolreg "github.com/tommyxie2026-tech/computecloud/internal/tool"
)

type toolAdvertiseProvider struct{ recoveryRemoteProvider }

func (toolAdvertiseProvider) Profile() string { return "tool_advertise_fixture" }
func (toolAdvertiseProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{
		Runtime: []string{"remote_api"},
		Tools: []string{"job_io_v1", "not_installed_fixture"},
		Environment: []string{"process"},
	}
}

func TestWorkerAdvertisesOnlyInstalledCompatibleTools(t *testing.T) {
	got := advertisedRuntimeCapabilities(toolAdvertiseProvider{})
	for _, want := range []string{"runtime:remote_api", "tool:job_io_v1", "environment:process"} {
		found := false
		for _, value := range got {
			found = found || value == want
		}
		if !found {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	for _, value := range got {
		if value == "tool:not_installed_fixture" {
			t.Fatalf("unregistered Tool leaked into Worker advertisement: %v", got)
		}
	}
}

func TestRegisteredFixtureToolCanBeAdvertisedWithoutSchedulerChange(t *testing.T) {
	name := "worker_fixture_tool"
	if _, ok := toolreg.Lookup(name); !ok {
		if err := toolreg.Register(toolreg.Descriptor{Name: name, Version: "1", SideEffect: toolreg.ReadOnly}); err != nil {
			t.Fatal(err)
		}
	}
	caps := adapter.CapabilitySet{Tools: []string{name}}
	caps.Tools = toolreg.InstalledCompatible(caps.Tools)
	if !reflect.DeepEqual(caps.Advertised(), []string{"tool:" + name}) {
		t.Fatalf("fixture Tool advertisement=%v", caps.Advertised())
	}
}
