package environment

import (
	"errors"
	"reflect"
	"testing"
)

func TestBuiltinProcessAndInstalledCompatible(t *testing.T) {
	if got, want := Names(), []string{"process"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("environments=%v want=%v", got, want)
	}
	got := InstalledCompatible([]string{"missing", "process", "process"})
	if !reflect.DeepEqual(got, []string{"process"}) {
		t.Fatalf("installed compatible=%v", got)
	}
}

func TestRegistryValidationAndFixtureExtension(t *testing.T) {
	name := "fixture_container_env"
	if err := Register(Descriptor{Name: name, Version: "1", IsolationClass: "container", FilesystemMode: "isolated", NetworkMode: "restricted"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(name); !ok {
		t.Fatal("registered fixture environment missing")
	}
	if err := Register(Descriptor{Name: name, Version: "1", IsolationClass: "container", FilesystemMode: "isolated", NetworkMode: "restricted"}); err == nil {
		t.Fatal("duplicate environment accepted")
	}
	for _, d := range []Descriptor{
		{Name: "environment:bad", Version: "1", IsolationClass: "process", FilesystemMode: "workspace", NetworkMode: "host"},
		{Name: "Bad", Version: "1", IsolationClass: "process", FilesystemMode: "workspace", NetworkMode: "host"},
		{Name: "missing_version", IsolationClass: "process", FilesystemMode: "workspace", NetworkMode: "host"},
		{Name: "bad_isolation", Version: "1", IsolationClass: "unknown", FilesystemMode: "workspace", NetworkMode: "host"},
		{Name: "bad_fs", Version: "1", IsolationClass: "process", FilesystemMode: "unknown", NetworkMode: "host"},
		{Name: "bad_network", Version: "1", IsolationClass: "process", FilesystemMode: "workspace", NetworkMode: "unknown"},
	} {
		if err := Register(d); err == nil {
			t.Fatalf("invalid descriptor accepted: %+v", d)
		}
	}
}

func TestAuthorizeRequired(t *testing.T) {
	required := []string{"runtime:event_stream", "environment:process"}
	if err := AuthorizeRequired(required, []string{"process"}, nil); err != nil {
		t.Fatalf("legacy process environment denied: %v", err)
	}
	if err := AuthorizeRequired(required, []string{"remote"}, nil); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("incompatible err=%v", err)
	}
	if err := AuthorizeRequired([]string{"environment:missing_env"}, []string{"missing_env"}, nil); !errors.Is(err, ErrUnregistered) {
		t.Fatalf("unregistered err=%v", err)
	}
	name := "fixture_vm_env"
	if _, ok := Lookup(name); !ok {
		if err := Register(Descriptor{Name: name, Version: "1", IsolationClass: "vm", FilesystemMode: "isolated", NetworkMode: "restricted"}); err != nil {
			t.Fatal(err)
		}
	}
	req := []string{"environment:" + name}
	if err := AuthorizeRequired(req, []string{name}, nil); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("non-legacy environment allowed without policy: %v", err)
	}
	if err := AuthorizeRequired(req, []string{name}, []string{name}); err != nil {
		t.Fatalf("explicitly allowed environment denied: %v", err)
	}
}
