package autobuild

import (
	"sync"
	"time"
)

// EventType identifies the kind of event published on the bus.
//
// Events are the SDK's observability primitive for things consumers care
// about: agent loop progress, phase transitions, tool calls. Internal
// state (per-span timing) belongs in Tracing instead.
type EventType string

const (
	// Agent loop lifecycle
	EventAgentLoopStarted   EventType = "agent.loop.started"
	EventAgentTurnCompleted EventType = "agent.turn.completed"
	EventAgentTraceStep     EventType = "agent.trace.step"
	EventAgentLoopCompleted EventType = "agent.loop.completed"
	EventAgentLoopFailed    EventType = "agent.loop.failed"

	// Phase transitions (driven by ExecutionContext)
	EventPhaseAdvanced EventType = "phase.advanced"

	// Plan lifecycle (within ExecutionContext)
	EventPlanProposed      EventType = "plan.proposed"
	EventPlanApproved      EventType = "plan.approved"
	EventExecutableUpdated EventType = "executable.updated"

	// Safety events
	EventSafetyBlocked EventType = "safety.blocked"

	// Verification events
	EventVerificationPassed EventType = "verification.passed"
	EventVerificationFailed EventType = "verification.failed"

	// Memory events
	EventMemoryWritten EventType = "memory.written"

	// Subagent events
	EventSubagentStarted   EventType = "subagent.started"
	EventSubagentCompleted EventType = "subagent.completed"
)

// Event is an immutable notification published on the EventBus.
type Event struct {
	Type      EventType      `json:"type"`
	Source    string         `json:"source"`              // originating component/thread ID
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// Subscriber is a callback invoked when an event matching its subscription
// is published.
type Subscriber func(e Event)

// TransformedSubscriber is a callback invoked after an event is transformed
// into a consumer-specific payload.
type TransformedSubscriber func(value any)

// Subscription is a handle returned by EventBus.Subscribe that can be
// used to cancel the subscription.
type Subscription struct {
	id       uint64
	eventType EventType
	bus      *InMemoryEventBus
}

// Cancel removes this subscription from the bus.
func (s *Subscription) Cancel() {
	if s.bus != nil {
		s.bus.Unsubscribe(s)
	}
}

// EventBus is a publish/subscribe system for inter-component notifications.
type EventBus interface {
	// Publish sends an event to all subscribers of that event type.
	Publish(e Event)

	// Subscribe registers a callback for the given event type and returns
	// a subscription handle that can cancel itself.
	Subscribe(eventType EventType, fn Subscriber) *Subscription

	// Unsubscribe removes a subscription.
	Unsubscribe(sub *Subscription)
}

// SubscribeTransformed subscribes to an event type and applies a transform
// before invoking the callback. Returning false from transform skips the event.
func SubscribeTransformed(bus EventBus, eventType EventType, transform func(Event) (any, bool), fn TransformedSubscriber) *Subscription {
	if bus == nil {
		return nil
	}

	return bus.Subscribe(eventType, func(e Event) {
		value, ok := transform(e)
		if !ok {
			return
		}
		fn(value)
	})
}

// ──────────────────────────────────────────────
// InMemoryEventBus — reference implementation
// ──────────────────────────────────────────────

type subscriber struct {
	id uint64
	fn Subscriber
}

// InMemoryEventBus is a simple, synchronous, thread-safe EventBus
// implementation suitable for single-process use.
type InMemoryEventBus struct {
	mu      sync.RWMutex
	subs    map[EventType][]subscriber
	nextID  uint64
}

// NewEventBus creates a new in-memory event bus.
func NewEventBus() *InMemoryEventBus {
	return &InMemoryEventBus{
		subs: make(map[EventType][]subscriber),
	}
}

// Publish sends the event to all matching subscribers synchronously.
func (b *InMemoryEventBus) Publish(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	b.mu.RLock()
	subs := b.subs[e.Type]
	b.mu.RUnlock()

	for _, s := range subs {
		s.fn(e)
	}
}

// Subscribe registers a callback for the given event type.
func (b *InMemoryEventBus) Subscribe(eventType EventType, fn Subscriber) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	id := b.nextID
	b.subs[eventType] = append(b.subs[eventType], subscriber{id: id, fn: fn})

	return &Subscription{id: id, eventType: eventType, bus: b}
}

// Unsubscribe removes a subscription.
func (b *InMemoryEventBus) Unsubscribe(sub *Subscription) {
	if sub == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subs[sub.eventType]
	for i, s := range subs {
		if s.id == sub.id {
			b.subs[sub.eventType] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}
