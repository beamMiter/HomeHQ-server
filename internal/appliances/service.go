package appliances

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// listTTL is short on purpose: unlike a camera, an appliance's state changes
// under us (someone uses the wall switch), so a long cache would show lies.
// A command drops the cache at once.
const listTTL = 5 * time.Second

// Events: how many the app may ask for.
const (
	DefaultEventLimit = 50
	MaxEventLimit     = 200
)

// defaultAutoOff is how long a kind of appliance may stay on before the server
// switches it off itself. Only things that make heat: if the connection drops
// after "on", they must not stay on all day.
var defaultAutoOff = map[Kind]time.Duration{
	KindHeater: 2 * time.Hour,
}

// idPattern: vendor device ids are short alphanumeric strings.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)

// keyPattern: capability keys are short snake_case words.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// EventStore keeps the log of relayed commands.
type EventStore interface {
	Add(ctx context.Context, e Event) error
	// List returns one appliance's events, newest first, at most limit.
	List(ctx context.Context, applianceID string, limit int) ([]Event, error)
}

// Status is what the app learns before it asks for any appliance.
type Status struct {
	Enabled bool   `json:"enabled"`
	Driver  string `json:"driver,omitempty"`

	// CanAdd is true when the household can add and remove appliances itself
	// (the driver is a Registrar). Which of them may is decided per request.
	CanAdd bool `json:"can_add"`
}

type stopper interface{ Stop() bool }

type autoOffTimer struct {
	t   stopper
	gen uint64
	at  time.Time // when it fires
}

// Service adds checks, a short cache, a safety switch-off and an event log in
// front of a Driver.
//
// The safety switch-off lives in this process: a restart forgets pending
// timers, so an appliance switched on just before one stays on. Say so to the
// household, or move the timer into the vendor's own scheduler, before relying
// on it for anything that can start a fire.
type Service struct {
	drv     Driver // nil = off
	store   EventStore
	now     func() time.Time
	after   func(d time.Duration, f func()) stopper
	autoOff map[Kind]time.Duration

	mu   sync.Mutex
	list struct {
		at    time.Time
		items []Appliance
	}
	timers map[string]autoOffTimer
	gen    uint64
}

// NewService wraps drv (nil switches the feature off). store receives the
// event log; nil keeps no log.
func NewService(drv Driver, store EventStore) *Service {
	auto := make(map[Kind]time.Duration, len(defaultAutoOff))
	for k, d := range defaultAutoOff {
		auto[k] = d
	}
	return &Service{
		drv:     drv,
		store:   store,
		now:     time.Now,
		after:   func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
		autoOff: auto,
		timers:  map[string]autoOffTimer{},
	}
}

func (s *Service) Enabled() bool { return s != nil && s.drv != nil }

func (s *Service) Status() Status {
	if !s.Enabled() {
		return Status{}
	}
	_, canAdd := s.drv.(Registrar)
	return Status{Enabled: true, Driver: s.drv.Name(), CanAdd: canAdd}
}

// List returns the appliances with their state, from a very short cache, each
// with its status, when that state was read, and (when known) since when it
// has been so and when the server will switch it off.
func (s *Service) List(ctx context.Context) ([]Appliance, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	s.mu.Lock()
	if s.list.items != nil && s.now().Sub(s.list.at) < listTTL {
		items := append([]Appliance(nil), s.list.items...)
		s.mu.Unlock()
		return s.annotate(ctx, items), nil
	}
	s.mu.Unlock()

	items, err := s.drv.List(ctx)
	if err != nil {
		return nil, err
	}
	at := s.now()
	items = decorate(items)
	for i := range items {
		items[i].CheckedAt = at
	}
	s.mu.Lock()
	s.list.items = append([]Appliance(nil), items...)
	s.list.at = at
	s.mu.Unlock()
	return s.annotate(ctx, items), nil
}

// annotate adds what changes without the vendor knowing: since when each
// appliance has been as it is (from our own log), and when the safety
// switch-off will fire. items is a copy the caller owns.
func (s *Service) annotate(ctx context.Context, items []Appliance) []Appliance {
	var last map[string]Switch
	if switches, ok := s.store.(SwitchLog); ok && len(items) > 0 {
		ids := make([]string, len(items))
		for i, a := range items {
			ids[i] = a.ID
		}
		var err error
		if last, err = switches.LastSwitched(ctx, ids); err != nil {
			// The status itself does not depend on the log: say less, not fail.
			log.Printf("appliances: could not read when appliances were switched: %v", err)
			last = nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		a := &items[i]
		a.Since, a.AutoOffAt = nil, nil
		on, _ := a.State[KeyPower].(bool)
		// Only when the log agrees with the appliance: if it was switched
		// some other way since, we do not know when.
		if sw, ok := last[a.ID]; ok && sw.On == on {
			at := sw.At
			a.Since = &at
		}
		if t, ok := s.timers[a.ID]; ok && on {
			at := t.at
			a.AutoOffAt = &at
		}
	}
	return items
}

// decorate applies our policy to what the vendor reported: which controls are
// for admins only, the status, and an empty (not null) capability list and
// state.
func decorate(items []Appliance) []Appliance {
	out := make([]Appliance, len(items))
	for i, a := range items {
		caps := make([]Capability, len(a.Capabilities))
		for j, c := range a.Capabilities {
			c.AdminOnly = adminOnly(c.Key)
			caps[j] = c
		}
		a.Capabilities = caps
		if a.State == nil {
			a.State = map[string]any{}
		}
		a.Status = statusOf(a)
		out[i] = a
	}
	return out
}

// adminOnly: any signed-in member may switch an appliance on or off; every
// other control (temperature, mode, brightness...) is for admins.
func adminOnly(key string) bool { return key != KeyPower }

// Refresh drops the cached list, so the next call asks the vendor.
func (s *Service) Refresh() {
	s.mu.Lock()
	s.list.items = nil
	s.mu.Unlock()
}

// Get returns one appliance.
func (s *Service) Get(ctx context.Context, id string) (Appliance, error) {
	if !s.Enabled() {
		return Appliance{}, ErrDisabled
	}
	if !idPattern.MatchString(id) {
		return Appliance{}, ErrBadInput
	}
	items, err := s.List(ctx)
	if err != nil {
		return Appliance{}, err
	}
	for _, a := range items {
		if a.ID == id {
			return a, nil
		}
	}
	return Appliance{}, ErrNotFound
}

// Command sets one capability of one appliance and answers with the state read
// back from it.
//
// An int value outside the capability's range is brought inside it. An
// appliance already known to be offline is refused without a vendor call.
func (s *Service) Command(ctx context.Context, who Actor, id, key string, value any) (Result, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if !a.Online {
		return Result{}, ErrOffline
	}
	if !keyPattern.MatchString(key) {
		return Result{}, ErrBadInput
	}
	c, ok := a.capability(key)
	if !ok {
		return Result{}, ErrUnsupported
	}
	if c.AdminOnly && !who.Admin {
		return Result{}, ErrForbidden
	}
	v, err := c.normalize(value)
	if err != nil {
		return Result{}, err
	}

	if err := s.drv.Set(ctx, id, key, v); err != nil {
		return Result{}, err
	}
	s.Refresh()

	res := Result{ID: id, Confirmed: true}
	st, err := s.drv.State(ctx, id)
	if err != nil {
		// The command went through; only the read-back failed.
		log.Printf("appliances: read-back of %s failed: %v", id, err)
		st = map[string]any{key: v}
		res.Confirmed = false
	}
	res.State = st

	s.record(ctx, Event{
		ApplianceID:   id,
		ApplianceName: a.Name,
		Capability:    key,
		Value:         mustJSON(v),
		Source:        SourceUser,
		UserID:        who.UserID,
	})

	if key == KeyPower {
		on, _ := v.(bool)
		s.watch(a, on)
	}
	return res, nil
}

// Limits on what the household types when adding an appliance.
const (
	maxNameRunes  = 60
	maxRoomRunes  = 40
	maxBrandRunes = 40
	maxModelRunes = 60
)

// clean trims what was typed and checks it.
func (n NewAppliance) clean() (NewAppliance, error) {
	n.Name = strings.TrimSpace(n.Name)
	n.Room = strings.TrimSpace(n.Room)
	n.Brand = strings.TrimSpace(n.Brand)
	n.Model = strings.TrimSpace(n.Model)
	n.Connection = strings.TrimSpace(n.Connection)
	if n.Connection == "" {
		n.Connection = ConnectionDemo
	}
	switch {
	case n.Name == "", utf8.RuneCountInString(n.Name) > maxNameRunes,
		utf8.RuneCountInString(n.Room) > maxRoomRunes,
		utf8.RuneCountInString(n.Brand) > maxBrandRunes,
		utf8.RuneCountInString(n.Model) > maxModelRunes,
		!ValidKind(n.Kind):
		return NewAppliance{}, ErrBadInput
	}
	return n, nil
}

// Add registers a new appliance. Only an admin may, and only a driver that is
// a Registrar can.
func (s *Service) Add(ctx context.Context, who Actor, n NewAppliance) (Appliance, error) {
	if !s.Enabled() {
		return Appliance{}, ErrDisabled
	}
	if !who.Admin {
		return Appliance{}, ErrForbidden
	}
	reg, ok := s.drv.(Registrar)
	if !ok {
		return Appliance{}, ErrUnsupported
	}
	n, err := n.clean()
	if err != nil {
		return Appliance{}, err
	}
	a, err := reg.Add(ctx, n, who.UserID)
	if err != nil {
		return Appliance{}, err
	}
	s.Refresh()
	a = decorate([]Appliance{a})[0]
	a.CheckedAt = s.now()
	return a, nil
}

// Remove forgets an appliance the household added. Only an admin may.
func (s *Service) Remove(ctx context.Context, who Actor, id string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if !who.Admin {
		return ErrForbidden
	}
	reg, ok := s.drv.(Registrar)
	if !ok {
		return ErrUnsupported
	}
	if !idPattern.MatchString(id) {
		return ErrBadInput
	}
	if err := reg.Remove(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	if t, ok := s.timers[id]; ok {
		t.t.Stop()
		delete(s.timers, id)
	}
	s.mu.Unlock()
	s.Refresh()
	return nil
}

// Events returns who sent what to one appliance, newest first.
func (s *Service) Events(ctx context.Context, id string, limit int) ([]Event, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if !idPattern.MatchString(id) {
		return nil, ErrBadInput
	}
	switch {
	case limit <= 0:
		limit = DefaultEventLimit
	case limit > MaxEventLimit:
		limit = MaxEventLimit
	}
	if s.store == nil {
		return []Event{}, nil
	}
	items, err := s.store.List(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []Event{}
	}
	return items, nil
}

// record writes an event. The appliance has already changed, so a failure here
// is logged, not returned.
func (s *Service) record(ctx context.Context, e Event) {
	if s.store == nil {
		return
	}
	if err := s.store.Add(ctx, e); err != nil {
		log.Printf("appliances: could not record event for %s: %v", e.ApplianceID, err)
	}
}

// watch starts or cancels the safety switch-off after a power command.
func (s *Service) watch(a Appliance, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.timers[a.ID]; ok {
		old.t.Stop()
		delete(s.timers, a.ID)
	}
	d := s.autoOff[a.Kind]
	if !on || d <= 0 {
		return
	}
	s.gen++
	gen := s.gen
	id, name := a.ID, a.Name
	s.timers[id] = autoOffTimer{
		gen: gen,
		at:  s.now().Add(d),
		t:   s.after(d, func() { s.fireAutoOff(id, name, gen) }),
	}
}

// fireAutoOff switches an appliance off when its time is up. gen guards
// against a timer that was replaced a moment before it fired.
func (s *Service) fireAutoOff(id, name string, gen uint64) {
	s.mu.Lock()
	cur, ok := s.timers[id]
	if !ok || cur.gen != gen {
		s.mu.Unlock()
		return
	}
	delete(s.timers, id)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.drv.Set(ctx, id, KeyPower, false); err != nil {
		log.Printf("appliances: safety switch-off of %s failed: %v", id, err)
		return
	}
	s.Refresh()
	s.record(ctx, Event{
		ApplianceID:   id,
		ApplianceName: name,
		Capability:    KeyPower,
		Value:         mustJSON(false),
		Source:        SourceAutoOff,
	})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}
