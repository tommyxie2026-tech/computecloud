package tool

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type SideEffect string

const (
	ReadOnly   SideEffect = "read_only"
	Idempotent SideEffect = "idempotent"
	Mutating   SideEffect = "mutating"
)

type Descriptor struct {
	Name          string
	Version       string
	SideEffect    SideEffect
	LegacyDefault bool
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

var registry = struct {
	sync.RWMutex
	items map[string]Descriptor
}{items: map[string]Descriptor{}}

var (
	ErrUnregistered = errors.New("tool is not registered")
	ErrIncompatible = errors.New("tool is not compatible with runtime")
	ErrPolicyDenied = errors.New("tool is denied by policy")
)

func ValidName(name string) bool {
	return nameRE.MatchString(name) && !strings.Contains(name, ":")
}

func validSideEffect(v SideEffect) bool {
	return v == ReadOnly || v == Idempotent || v == Mutating
}

func Register(d Descriptor) error {
	d.Name = strings.TrimSpace(d.Name)
	d.Version = strings.TrimSpace(d.Version)
	if !ValidName(d.Name) {
		return fmt.Errorf("invalid tool name: %q", d.Name)
	}
	if d.Version == "" {
		return errors.New("tool version required")
	}
	if !validSideEffect(d.SideEffect) {
		return fmt.Errorf("invalid tool side effect: %q", d.SideEffect)
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.items[d.Name]; exists {
		return fmt.Errorf("tool already registered: %s", d.Name)
	}
	registry.items[d.Name] = d
	return nil
}

func Lookup(name string) (Descriptor, bool) {
	registry.RLock()
	defer registry.RUnlock()
	d, ok := registry.items[name]
	return d, ok
}

func Names() []string {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]string, 0, len(registry.items))
	for name := range registry.items {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func InstalledCompatible(compatible []string) []string {
	registry.RLock()
	defer registry.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, name := range compatible {
		if _, ok := registry.items[name]; !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func compatible(compatible []string, name string) bool {
	for _, v := range compatible {
		if v == name {
			return true
		}
	}
	return false
}

func allowedByPolicy(d Descriptor, allowed []string) bool {
	if len(allowed) == 0 {
		return d.LegacyDefault
	}
	for _, v := range allowed {
		if v == d.Name {
			return true
		}
	}
	return false
}

// AuthorizeRequired checks only namespaced Tool requirements. Legacy
// capabilities remain unchanged during the v0.4 compatibility window.
func AuthorizeRequired(requiredCapabilities, runtimeCompatible, policyAllowed []string) error {
	for _, capability := range requiredCapabilities {
		if !strings.HasPrefix(capability, "tool:") {
			continue
		}
		name := strings.TrimPrefix(capability, "tool:")
		d, ok := Lookup(name)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnregistered, name)
		}
		if !compatible(runtimeCompatible, name) {
			return fmt.Errorf("%w: %s", ErrIncompatible, name)
		}
		if !allowedByPolicy(d, policyAllowed) {
			return fmt.Errorf("%w: %s", ErrPolicyDenied, name)
		}
	}
	return nil
}

func init() {
	for _, d := range []Descriptor{
		{Name: "artifact_inputs_v1", Version: "1", SideEffect: ReadOnly, LegacyDefault: true},
		{Name: "job_io_v1", Version: "1", SideEffect: Idempotent, LegacyDefault: true},
	} {
		if err := Register(d); err != nil {
			panic(err)
		}
	}
}
