package environment

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

type State string

const (
	StatePrepared State = "PREPARED"
	StateActive   State = "ACTIVE"
	StateReleased State = "RELEASED"
	StateUnknown  State = "UNKNOWN"
)

type CleanupState string

const (
	CleanupPending   CleanupState = "PENDING"
	CleanupConfirmed CleanupState = "CONFIRMED"
	CleanupUnknown   CleanupState = "UNKNOWN"
)

type Ref struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

type PrepareRequest struct {
	AttemptID string
	TaskID    string
	Generation int64
	CWD       string
	Env       []string
}

type Prepared struct {
	Ref Ref
	CWD string
	Env []string
}

type Inspection struct {
	State   State
	Cleanup CleanupState
}

type ReleaseResult struct {
	State   State
	Cleanup CleanupState
	Err     error
}

type Provider interface {
	Descriptor() Descriptor
	Prepare(context.Context, PrepareRequest, func(Ref) error) (Prepared, error)
	Activate(context.Context, Prepared) error
	Inspect(context.Context, Ref) (Inspection, error)
	Release(context.Context, Ref) (ReleaseResult, error)
}

var providers = struct {
	sync.RWMutex
	items map[string]Provider
}{items: map[string]Provider{}}

func validRef(ref Ref) error {
	if strings.TrimSpace(ref.Provider) == "" || strings.TrimSpace(ref.ID) == "" {
		return errors.New("environment reference is incomplete")
	}
	if !ValidName(ref.Provider) {
		return fmt.Errorf("invalid environment provider reference: %q", ref.Provider)
	}
	return nil
}

func RegisterProvider(p Provider) error {
	if p == nil {
		return errors.New("environment provider required")
	}
	d := p.Descriptor()
	if !ValidName(d.Name) {
		return fmt.Errorf("invalid environment provider name: %q", d.Name)
	}
	if old, ok := Lookup(d.Name); ok {
		if old != d {
			return fmt.Errorf("environment descriptor conflict for provider: %s", d.Name)
		}
	} else if err := Register(d); err != nil {
		return err
	}
	providers.Lock()
	defer providers.Unlock()
	if _, exists := providers.items[d.Name]; exists {
		return fmt.Errorf("environment provider already registered: %s", d.Name)
	}
	providers.items[d.Name] = p
	return nil
}

func LookupProvider(name string) (Provider, bool) {
	providers.RLock()
	defer providers.RUnlock()
	p, ok := providers.items[name]
	return p, ok
}

func ProviderNames() []string {
	providers.RLock()
	defer providers.RUnlock()
	out := make([]string, 0, len(providers.items))
	for name := range providers.items {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

func RequiredName(required []string) string {
	name := ""
	for _, capability := range required {
		if !strings.HasPrefix(capability, "environment:") {
			continue
		}
		v := strings.TrimPrefix(capability, "environment:")
		if name == "" {
			name = v
			continue
		}
		if name != v {
			return ""
		}
	}
	if name == "" {
		return "process"
	}
	return name
}

type processProvider struct{}

func (processProvider) Descriptor() Descriptor {
	return Descriptor{
		Name: "process", Version: "1",
		IsolationClass: "process", FilesystemMode: "workspace", NetworkMode: "host",
		LegacyDefault: true,
	}
}

func (processProvider) Prepare(_ context.Context, req PrepareRequest, record func(Ref) error) (Prepared, error) {
	if strings.TrimSpace(req.AttemptID) == "" || strings.TrimSpace(req.TaskID) == "" || req.Generation < 1 {
		return Prepared{}, errors.New("environment prepare requires attempt/task/generation")
	}
	if req.CWD == "" || !filepath.IsAbs(req.CWD) {
		return Prepared{}, errors.New("process environment requires absolute workspace")
	}
	ref := Ref{Provider: "process", ID: req.AttemptID}
	if record != nil {
		if err := record(ref); err != nil {
			return Prepared{}, err
		}
	}
	return Prepared{Ref: ref, CWD: req.CWD, Env: append([]string(nil), req.Env...)}, nil
}

func (processProvider) Activate(context.Context, Prepared) error { return nil }

func (processProvider) Inspect(_ context.Context, ref Ref) (Inspection, error) {
	if err := validRef(ref); err != nil || ref.Provider != "process" {
		if err != nil {
			return Inspection{State: StateUnknown, Cleanup: CleanupUnknown}, err
		}
		return Inspection{State: StateUnknown, Cleanup: CleanupUnknown}, errors.New("process provider received foreign reference")
	}
	// The process Environment owns no resource beyond the Attempt workspace.
	// Runtime cleanup remains separately fenced by Runtime Provider evidence.
	return Inspection{State: StateReleased, Cleanup: CleanupConfirmed}, nil
}

func (processProvider) Release(_ context.Context, ref Ref) (ReleaseResult, error) {
	inspection, err := (processProvider{}).Inspect(context.Background(), ref)
	return ReleaseResult{State: inspection.State, Cleanup: inspection.Cleanup, Err: err}, err
}

func init() {
	if err := RegisterProvider(processProvider{}); err != nil {
		panic(err)
	}
}
