package environment

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type Descriptor struct {
	Name           string
	Version        string
	IsolationClass string
	FilesystemMode string
	NetworkMode    string
	LegacyDefault  bool
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

var registry = struct {
	sync.RWMutex
	items map[string]Descriptor
}{items: map[string]Descriptor{}}

var (
	ErrUnregistered = errors.New("environment is not registered")
	ErrIncompatible = errors.New("environment is not compatible with runtime")
	ErrPolicyDenied = errors.New("environment is denied by policy")
)

func ValidName(name string) bool {
	return nameRE.MatchString(name) && !strings.Contains(name, ":")
}

func validIsolation(v string) bool {
	return v == "process" || v == "container" || v == "vm" || v == "remote"
}

func validFilesystem(v string) bool {
	return v == "workspace" || v == "isolated" || v == "remote"
}

func validNetwork(v string) bool {
	return v == "host" || v == "restricted" || v == "isolated" || v == "remote"
}

func Register(d Descriptor) error {
	d.Name = strings.TrimSpace(d.Name)
	d.Version = strings.TrimSpace(d.Version)
	if !ValidName(d.Name) {
		return fmt.Errorf("invalid environment name: %q", d.Name)
	}
	if d.Version == "" {
		return errors.New("environment version required")
	}
	if !validIsolation(d.IsolationClass) {
		return fmt.Errorf("invalid environment isolation class: %q", d.IsolationClass)
	}
	if !validFilesystem(d.FilesystemMode) {
		return fmt.Errorf("invalid environment filesystem mode: %q", d.FilesystemMode)
	}
	if !validNetwork(d.NetworkMode) {
		return fmt.Errorf("invalid environment network mode: %q", d.NetworkMode)
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.items[d.Name]; exists {
		return fmt.Errorf("environment already registered: %s", d.Name)
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
	seen := map[string]bool{}
	var out []string
	for _, name := range compatible {
		if _, ok := LookupProvider(name); !ok || seen[name] {
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

// AuthorizeRequired checks only namespaced Environment requirements.
// Legacy Tasks without environment:* remain compatible during the v0.4 window.
func AuthorizeRequired(requiredCapabilities, runtimeCompatible, policyAllowed []string) error {
	for _, capability := range requiredCapabilities {
		if !strings.HasPrefix(capability, "environment:") {
			continue
		}
		name := strings.TrimPrefix(capability, "environment:")
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

