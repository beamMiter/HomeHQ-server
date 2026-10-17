// Package appliances lets the household switch and adjust what is in the
// house: lights, plugs, fans, air coolers, air conditioners, heaters.
//
// The device vendor (its cloud, or a local hub) stays the source of truth for
// what exists and what state it is in; this package relays commands after
// checking them, and keeps a short log of who sent what. A Driver hides which
// vendor that is, so a fake one serves the UI and tests before any appliance is
// wired up. It follows the shape of package cameras.
package appliances

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"
)

// Kind is what sort of appliance it is. It drives the icon and the safety
// policy (see Service), not which controls exist: those come from Capabilities.
type Kind string

const (
	KindLight  Kind = "light"
	KindPlug   Kind = "plug"
	KindFan    Kind = "fan"
	KindCooler Kind = "cooler" // a portable evaporative air cooler
	KindAircon Kind = "aircon"
	KindHeater Kind = "heater"
	KindOther  Kind = "other"
)

// Capability keys the app knows how to draw. A driver may add its own keys; the
// app shows the ones it recognises.
const (
	KeyPower       = "power"       // bool
	KeyBrightness  = "brightness"  // int, percent
	KeyTemperature = "temperature" // int, target in °C
	KeyMode        = "mode"        // enum
	KeyFanSpeed    = "fan_speed"   // int
	KeySwing       = "swing"       // bool, the louver sweeps
	KeyCooling     = "cooling"     // bool, an air cooler's water pump
)

// What an appliance is, in one word, in the words smart-home apps use. The
// server decides it (see statusOf), so the list, the page and the home screen
// always agree.
const (
	StatusOn      = "on"      // reachable and on
	StatusOff     = "off"     // reachable and off
	StatusOffline = "offline" // cannot be reached; State is the last known
)

// CapabilityType is the shape of a control's value.
type CapabilityType string

const (
	TypeBool CapabilityType = "bool"
	TypeInt  CapabilityType = "int"
	TypeEnum CapabilityType = "enum"
)

// Capability is one thing an appliance can be told to do. The app draws only
// the capabilities an appliance reports, and the server checks every command
// against them.
type Capability struct {
	Key  string         `json:"key"`
	Type CapabilityType `json:"type"`

	// Min and Max bound an int capability (both are required for TypeInt; a
	// value outside is brought back inside, not refused).
	Min  int    `json:"min"`
	Max  int    `json:"max"`
	Unit string `json:"unit,omitempty"`

	// Options lists the values of an enum capability.
	Options []string `json:"options,omitempty"`

	// AdminOnly is true when only an admin may use this control. Everyone may
	// switch an appliance on and off; adjusting it is for admins.
	AdminOnly bool `json:"admin_only"`
}

// normalize checks a value from the app against the capability and returns it
// in the type the driver expects: bool, int (clamped) or string.
func (c Capability) normalize(v any) (any, error) {
	switch c.Type {
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, ErrBadInput
		}
		return b, nil
	case TypeInt:
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case int:
			f = float64(n)
		default:
			return nil, ErrBadInput
		}
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
			return nil, ErrBadInput
		}
		switch {
		case f < float64(c.Min):
			return c.Min, nil
		case f > float64(c.Max):
			return c.Max, nil
		}
		return int(f), nil
	case TypeEnum:
		s, ok := v.(string)
		if !ok {
			return nil, ErrBadInput
		}
		for _, o := range c.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, ErrBadInput
	}
	return nil, ErrBadInput
}

// Appliance is one appliance as the app sees it.
type Appliance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	Room string `json:"room,omitempty"`

	// Brand and Model are what the household said when adding it (optional).
	Brand string `json:"brand,omitempty"`
	Model string `json:"model,omitempty"`

	// Connection is how the appliance is reached: "demo" (simulated by this
	// server), or a vendor/hub name once such a driver exists.
	Connection string `json:"connection,omitempty"`

	// Removable is true for appliances the household added itself.
	Removable bool `json:"removable,omitempty"`

	// Online is whether the vendor can currently reach the appliance.
	Online bool `json:"online"`

	// Status is StatusOn, StatusOff or StatusOffline.
	Status string `json:"status"`

	// CheckedAt is when State was read from the appliance (the list is
	// cached for a few seconds, so it can be a little behind).
	CheckedAt time.Time `json:"checked_at"`

	// Since is when it was switched to how it is now, by a command this
	// server relayed. Absent when that is not known: it was switched some
	// other way (a wall switch, the vendor's own app), or before the log.
	Since *time.Time `json:"since,omitempty"`

	// AutoOffAt is when the server will switch it off by itself, while it
	// is on and of a kind that must not stay on (see Service).
	AutoOffAt *time.Time `json:"auto_off_at,omitempty"`

	Capabilities []Capability `json:"capabilities"`

	// State holds the current value of each capability, by key.
	State map[string]any `json:"state"`
}

// statusOf says what an appliance is, from what the vendor reported.
func statusOf(a Appliance) string {
	if !a.Online {
		return StatusOffline
	}
	if on, _ := a.State[KeyPower].(bool); on {
		return StatusOn
	}
	return StatusOff
}

func (a Appliance) capability(key string) (Capability, bool) {
	for _, c := range a.Capabilities {
		if c.Key == key {
			return c, true
		}
	}
	return Capability{}, false
}

// Driver is an appliance source: a vendor cloud, a hub, or a fake.
type Driver interface {
	Name() string
	// List returns every appliance with its current state.
	List(ctx context.Context) ([]Appliance, error)
	// State reads one appliance's current state, by capability key.
	State(ctx context.Context, id string) (map[string]any, error)
	// Set changes one capability. value is already checked and normalized
	// (bool, int or string).
	Set(ctx context.Context, id, key string, value any) error
}

// NewAppliance is what the household tells us when adding one.
type NewAppliance struct {
	Name       string `json:"name"`
	Kind       Kind   `json:"kind"`
	Room       string `json:"room"`
	Brand      string `json:"brand"`
	Model      string `json:"model"`
	Connection string `json:"connection"`
}

// Registrar is implemented by a Driver that lets the household add and remove
// appliances itself. A driver that only reflects what a vendor reports does
// not implement it, and the app then offers no "add" button.
type Registrar interface {
	// Add registers an appliance and returns it as List would. A connection
	// the driver cannot make is ErrBadInput.
	Add(ctx context.Context, n NewAppliance, createdBy string) (Appliance, error)
	// Remove forgets an appliance; ErrNotFound when there is none.
	Remove(ctx context.Context, id string) error
}

// Actor is who is sending a command.
type Actor struct {
	UserID string
	Admin  bool
}

// Where a command came from.
const (
	SourceUser    = "user"
	SourceAutoOff = "auto_off" // the server's own safety switch-off
)

// Switch is the last power command relayed to an appliance.
type Switch struct {
	On bool
	At time.Time
}

// SwitchLog is implemented by an EventStore that can say when each appliance
// was last switched on or off; the Service then reports Since.
type SwitchLog interface {
	LastSwitched(ctx context.Context, ids []string) (map[string]Switch, error)
}

// Event is one relayed command, for the "who did that" list.
type Event struct {
	ID            string          `json:"id"`
	ApplianceID   string          `json:"appliance_id"`
	ApplianceName string          `json:"appliance_name"`
	Capability    string          `json:"capability"`
	Value         json.RawMessage `json:"value"`
	Source        string          `json:"source"`
	UserID        string          `json:"user_id,omitempty"`
	UserName      string          `json:"user_name,omitempty"`
	At            time.Time       `json:"at"`
}

// Result is what a command answers with: the state read back from the
// appliance after the change.
type Result struct {
	ID    string         `json:"id"`
	State map[string]any `json:"state"`
	// Confirmed is false when the command went through but the state could not
	// be read back, so State is only what was asked for.
	Confirmed bool `json:"confirmed"`
}

var (
	// ErrDisabled: no appliance source is configured.
	ErrDisabled = errors.New("appliances: not configured")
	// ErrProviderLimit: the vendor itself refused because its own call
	// allowance is used up. (There is no limit on our side.)
	ErrProviderLimit = errors.New("appliances: the vendor's call limit is reached")
	// ErrBadInput: the request itself is wrong (bad id, bad value).
	ErrBadInput = errors.New("appliances: bad input")
	// ErrNotFound: the vendor doesn't know this appliance.
	ErrNotFound = errors.New("appliances: appliance not found")
	// ErrOffline: the appliance can't be reached right now.
	ErrOffline = errors.New("appliances: appliance offline")
	// ErrConfig: the credentials are wrong or rejected; fix the server config.
	ErrConfig = errors.New("appliances: credentials rejected")
	// ErrUnsupported: this appliance doesn't have that capability.
	ErrUnsupported = errors.New("appliances: not supported by this appliance")
	// ErrForbidden: the control is for admins only.
	ErrForbidden = errors.New("appliances: admin only")
)
