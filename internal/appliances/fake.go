package appliances

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a Driver with a few made-up appliances, for UI work and tests.
type Fake struct {
	mu    sync.Mutex
	state map[string]map[string]any
	cmds  []string
}

func NewFake() *Fake {
	f := &Fake{state: map[string]map[string]any{}}
	for _, a := range fakeAppliances {
		st := map[string]any{}
		for k, v := range a.State {
			st[k] = v
		}
		f.state[a.ID] = st
	}
	return f
}

func (f *Fake) Name() string { return "fake" }

var (
	capPower = Capability{Key: KeyPower, Type: TypeBool}
)

// The demo set covers the cases the UI has to handle: a dimmable light, a plain
// plug, an air conditioner with every kind of control, a heater (which the
// server switches off by itself) and one that is offline.
var fakeAppliances = []Appliance{
	{
		ID: "FAKE-LIVING-LIGHT", Name: "Living room light", Kind: KindLight, Room: "Living room", Online: true,
		Capabilities: []Capability{
			capPower,
			{Key: KeyBrightness, Type: TypeInt, Min: 1, Max: 100, Unit: "%"},
		},
		State: map[string]any{KeyPower: false, KeyBrightness: 60},
	},
	{
		ID: "FAKE-DESK-PLUG", Name: "Desk plug", Kind: KindPlug, Room: "Study", Online: true,
		Capabilities: []Capability{capPower},
		State:        map[string]any{KeyPower: false},
	},
	{
		ID: "FAKE-BEDROOM-AC", Name: "Bedroom aircon", Kind: KindAircon, Room: "Bedroom", Online: true,
		Capabilities: []Capability{
			capPower,
			{Key: KeyTemperature, Type: TypeInt, Min: 16, Max: 30, Unit: "°C"},
			{Key: KeyMode, Type: TypeEnum, Options: []string{"auto", "cool", "dry", "fan", "heat"}},
			{Key: KeyFanSpeed, Type: TypeInt, Min: 1, Max: 5},
		},
		State: map[string]any{KeyPower: false, KeyTemperature: 25, KeyMode: "cool", KeyFanSpeed: 2},
	},
	{
		ID: "FAKE-SPACE-HEATER", Name: "Space heater", Kind: KindHeater, Room: "Living room", Online: true,
		Capabilities: []Capability{capPower},
		State:        map[string]any{KeyPower: false},
	},
	{
		ID: "FAKE-GARAGE-FAN", Name: "Garage fan", Kind: KindFan, Room: "Garage", Online: false,
		Capabilities: []Capability{capPower},
		State:        map[string]any{KeyPower: false},
	},
}

func (f *Fake) find(id string) (Appliance, error) {
	for _, a := range fakeAppliances {
		if a.ID == id {
			return a, nil
		}
	}
	return Appliance{}, ErrNotFound
}

func (f *Fake) List(context.Context) ([]Appliance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Appliance, len(fakeAppliances))
	for i, a := range fakeAppliances {
		a.Capabilities = append([]Capability(nil), a.Capabilities...)
		a.State = copyState(f.state[a.ID])
		out[i] = a
	}
	return out, nil
}

func (f *Fake) State(_ context.Context, id string) (map[string]any, error) {
	a, err := f.find(id)
	if err != nil {
		return nil, err
	}
	if !a.Online {
		return nil, ErrOffline
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyState(f.state[id]), nil
}

// Set records the command ("<id> <key>=<value>") so tests can see what was sent.
func (f *Fake) Set(_ context.Context, id, key string, value any) error {
	a, err := f.find(id)
	if err != nil {
		return err
	}
	if !a.Online {
		return ErrOffline
	}
	if _, ok := a.capability(key); !ok {
		return ErrUnsupported
	}
	f.mu.Lock()
	f.state[id][key] = value
	f.cmds = append(f.cmds, fmt.Sprintf("%s %s=%v", id, key, value))
	f.mu.Unlock()
	return nil
}

// Commands returns the recorded commands.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func copyState(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
