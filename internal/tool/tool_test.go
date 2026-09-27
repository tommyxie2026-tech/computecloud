package tool

import (
	"errors"
	"reflect"
	"testing"
)

func TestBuiltinsAndInstalledCompatible(t *testing.T) {
	if got, want := Names(), []string{"artifact_inputs_v1", "job_io_v1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tools=%v want=%v", got, want)
	}
	got := InstalledCompatible([]string{"missing", "job_io_v1", "artifact_inputs_v1", "job_io_v1"})
	want := []string{"artifact_inputs_v1", "job_io_v1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installed compatible=%v want=%v", got, want)
	}
}

func TestRegistryValidationAndFixtureExtension(t *testing.T) {
	name := "fixture_tool_contract_v1"
	if err := Register(Descriptor{Name: name, Version: "1.0", SideEffect: ReadOnly}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(name); !ok {
		t.Fatal("registered fixture tool missing")
	}
	if err := Register(Descriptor{Name: name, Version: "1.0", SideEffect: ReadOnly}); err == nil {
		t.Fatal("duplicate tool accepted")
	}
	for _, d := range []Descriptor{
		{Name: "tool:bad", Version: "1", SideEffect: ReadOnly},
		{Name: "Bad", Version: "1", SideEffect: ReadOnly},
		{Name: "missing_version", SideEffect: ReadOnly},
		{Name: "bad_side_effect", Version: "1", SideEffect: SideEffect("unknown")},
	} {
		if err := Register(d); err == nil {
			t.Fatalf("invalid descriptor accepted: %+v", d)
		}
	}
}

func TestAuthorizeRequired(t *testing.T) {
	required := []string{"runtime:event_stream", "tool:job_io_v1"}
	if err := AuthorizeRequired(required, []string{"job_io_v1"}, nil); err != nil {
		t.Fatalf("legacy default tool denied: %v", err)
	}
	if err := AuthorizeRequired(required, []string{"artifact_inputs_v1"}, nil); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("incompatible err=%v", err)
	}
	if err := AuthorizeRequired([]string{"tool:missing_tool"}, []string{"missing_tool"}, nil); !errors.Is(err, ErrUnregistered) {
		t.Fatalf("unregistered err=%v", err)
	}
	name := "fixture_mutating_tool"
	if err := Register(Descriptor{Name: name, Version: "1", SideEffect: Mutating}); err != nil {
		t.Fatal(err)
	}
	req := []string{"tool:" + name}
	if err := AuthorizeRequired(req, []string{name}, nil); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("non-legacy tool allowed without policy: %v", err)
	}
	if err := AuthorizeRequired(req, []string{name}, []string{name}); err != nil {
		t.Fatalf("explicitly allowed tool denied: %v", err)
	}
}
