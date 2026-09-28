package environment

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProcessProviderLifecycle(t *testing.T) {
	p, ok := LookupProvider("process")
	if !ok {
		t.Fatal("process Provider missing")
	}
	dir := t.TempDir()
	var recorded Ref
	prepared, err := p.Prepare(context.Background(), PrepareRequest{
		AttemptID: "attempt",
		TaskID: "task",
		Generation: 1,
		CWD: dir,
		Env: []string{"A=B"},
	}, func(ref Ref) error {
		recorded = ref
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Ref != recorded || prepared.Ref.Provider != "process" || prepared.Ref.ID != "attempt" {
		t.Fatalf("prepared ref=%+v recorded=%+v", prepared.Ref, recorded)
	}
	if prepared.CWD != filepath.Clean(dir) || !reflect.DeepEqual(prepared.Env, []string{"A=B"}) {
		t.Fatalf("prepared=%+v", prepared)
	}
	if err = p.Activate(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	inspection, err := p.Inspect(context.Background(), prepared.Ref)
	if err != nil || inspection.Cleanup != CleanupConfirmed {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	released, err := p.Release(context.Background(), prepared.Ref)
	if err != nil || released.State != StateReleased || released.Cleanup != CleanupConfirmed {
		t.Fatalf("release=%+v err=%v", released, err)
	}
}

func TestEnvironmentRefRoundTrip(t *testing.T) {
	want := Ref{Provider: "process", ID: "attempt-1"}
	b, err := EncodeRef(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRef(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ref=%+v want=%+v", got, want)
	}
	for _, bad := range [][]byte{nil, []byte("{}"), []byte(`{"provider":"Bad","id":"x"}`)} {
		if _, err = DecodeRef(bad); err == nil {
			t.Fatalf("invalid ref accepted: %s", bad)
		}
	}
}

func TestDescriptorWithoutProviderIsNotInstalled(t *testing.T) {
	name := "descriptor_only_fixture"
	if _, ok := Lookup(name); !ok {
		if err := Register(Descriptor{
			Name: name, Version: "1",
			IsolationClass: "container", FilesystemMode: "isolated", NetworkMode: "restricted",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := InstalledCompatible([]string{name}); len(got) != 0 {
		t.Fatalf("descriptor-only environment advertised: %v", got)
	}
}
