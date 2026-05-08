package autobuild

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ── Outbound webhooks ─────────────────────────────────────────────────────────
//
// WebhookDispatcher publishes selected stream events to external HTTP
// endpoints. It is the SDK's hook for downstream systems that need to react
// to agent activity asynchronously: audit logs, Slack notifications, custom
// dashboards, mobile push.
//
// Wire format (POST body):
//
//	{
//	  "id":         "<event uuid>",        // duplicates X-Harness-Event-Id
//	  "version":    "1",                   // ProtocolVersion at time of emit
//	  "type":       "interrupt_required",  // StreamEventType
//	  "timestamp":  "2026-04-12T10:23:45Z",
//	  "data":       { ... payload ... }
//	}
//
// Headers:
//
//	Content-Type: application/json
//	X-Harness-Protocol: 1
//	X-Harness-Event-Id: <uuid>          // idempotency key
//	X-Harness-Signature: sha256=<hex>   // HMAC-SHA256(secret, body)
//
// Subscribers verify signatures with constant-time compare. If a delivery
// fails (network error or non-2xx response), the dispatcher retries with
// exponential backoff up to MaxRetries.

// WebhookSubscription describes a single outbound endpoint.
type WebhookSubscription struct {
	// ID is a stable identifier (typically a uuid). The dispatcher uses it
	// for logging and the WebhookStore uses it for indexing.
	ID string `json:"id"`

	// URL is the target HTTPS endpoint. Plain HTTP is allowed but discouraged.
	URL string `json:"url"`

	// Secret is the HMAC key. Generate with crypto/rand and never log it.
	Secret string `json:"secret"`

	// EventFilter restricts which StreamEventTypes are delivered.
	// Empty/nil = deliver all events the dispatcher knows about.
	EventFilter []StreamEventType `json:"event_filter,omitempty"`

	// Headers is an optional set of additional headers attached to every
	// request (e.g. for vendor-specific routing).
	Headers map[string]string `json:"headers,omitempty"`

	// Active is false to pause delivery without unregistering.
	Active bool `json:"active"`
}

// WebhookEvent is the JSON payload posted to subscriber URLs.
type WebhookEvent struct {
	ID        string          `json:"id"`
	Version   string          `json:"version"`
	Type      StreamEventType `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// WebhookStore persists subscriptions. The default in-process store is
// fine for development; production deployments should back it with a
// durable database so subscriptions survive restarts.
type WebhookStore interface {
	List(ctx context.Context) ([]WebhookSubscription, error)
	Get(ctx context.Context, id string) (WebhookSubscription, bool, error)
	Put(ctx context.Context, sub WebhookSubscription) error
	Delete(ctx context.Context, id string) error
}

// InMemoryWebhookStore is a process-local store, suitable for tests and
// single-replica deployments.
type InMemoryWebhookStore struct {
	mu sync.RWMutex
	m  map[string]WebhookSubscription
}

func NewInMemoryWebhookStore() *InMemoryWebhookStore {
	return &InMemoryWebhookStore{m: make(map[string]WebhookSubscription)}
}

func (s *InMemoryWebhookStore) List(_ context.Context) ([]WebhookSubscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WebhookSubscription, 0, len(s.m))
	for _, v := range s.m {
		out = append(out, v)
	}
	return out, nil
}

func (s *InMemoryWebhookStore) Get(_ context.Context, id string) (WebhookSubscription, bool, error) {
	s.mu.RLock()
	v, ok := s.m[id]
	s.mu.RUnlock()
	return v, ok, nil
}

func (s *InMemoryWebhookStore) Put(_ context.Context, sub WebhookSubscription) error {
	if sub.ID == "" {
		return errors.New("webhook subscription requires an ID")
	}
	s.mu.Lock()
	s.m[sub.ID] = sub
	s.mu.Unlock()
	return nil
}

func (s *InMemoryWebhookStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
	return nil
}

// ── Dispatcher ────────────────────────────────────────────────────────────────

// WebhookDispatcher publishes events to all matching subscriptions with
// HMAC-signed payloads, idempotency keys, and exponential-backoff retries.
//
// One dispatcher serves the whole runtime; events from many concurrent
// streams converge into a single delivery queue.
type WebhookDispatcher struct {
	store      WebhookStore
	httpClient *http.Client
	maxRetries int
	baseDelay  time.Duration
	queue      chan webhookJob
	closeOnce  sync.Once
	closed     chan struct{}
	wg         sync.WaitGroup
}

type webhookJob struct {
	sub     WebhookSubscription
	event   WebhookEvent
	body    []byte
	attempt int
}

// NewWebhookDispatcher creates a dispatcher backed by the given store.
// Pass nil http.Client for the default with a 10s timeout.
func NewWebhookDispatcher(store WebhookStore, client *http.Client) *WebhookDispatcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	d := &WebhookDispatcher{
		store:      store,
		httpClient: client,
		maxRetries: 5,
		baseDelay:  500 * time.Millisecond,
		queue:      make(chan webhookJob, 256),
		closed:     make(chan struct{}),
	}
	d.wg.Add(1)
	go d.worker()
	return d
}

// WithMaxRetries overrides the default retry cap (5).
func (d *WebhookDispatcher) WithMaxRetries(n int) *WebhookDispatcher {
	d.maxRetries = n
	return d
}

// Close shuts the dispatcher down, draining in-flight deliveries.
func (d *WebhookDispatcher) Close() {
	d.closeOnce.Do(func() {
		close(d.closed)
		close(d.queue)
		d.wg.Wait()
	})
}

// Publish enqueues an event for delivery to all matching subscriptions.
// Returns the number of subscriptions queued (0 means no matches).
func (d *WebhookDispatcher) Publish(ctx context.Context, ev WebhookEvent) (int, error) {
	if d == nil || d.store == nil {
		return 0, nil
	}
	if ev.ID == "" {
		ev.ID = "evt_" + randomHex(12)
	}
	if ev.Version == "" {
		ev.Version = ProtocolVersion
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	subs, err := d.store.List(ctx)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("marshal webhook event: %w", err)
	}
	queued := 0
	for _, sub := range subs {
		if !sub.Active {
			continue
		}
		if !subscriptionMatches(sub, ev.Type) {
			continue
		}
		select {
		case <-d.closed:
			return queued, errors.New("dispatcher closed")
		case d.queue <- webhookJob{sub: sub, event: ev, body: body}:
			queued++
		}
	}
	return queued, nil
}

// PublishStreamEvent is a convenience wrapper that converts a StreamEvent
// into a WebhookEvent and publishes it.
func (d *WebhookDispatcher) PublishStreamEvent(ctx context.Context, ev StreamEvent) (int, error) {
	if d == nil {
		return 0, nil
	}
	data, err := streamEventDataPayload(ev)
	if err != nil {
		return 0, err
	}
	return d.Publish(ctx, WebhookEvent{Type: ev.Type, Data: data})
}

func subscriptionMatches(sub WebhookSubscription, t StreamEventType) bool {
	if len(sub.EventFilter) == 0 {
		return true
	}
	for _, want := range sub.EventFilter {
		if want == t {
			return true
		}
	}
	return false
}

// ── Worker ────────────────────────────────────────────────────────────────────

func (d *WebhookDispatcher) worker() {
	defer d.wg.Done()
	for job := range d.queue {
		d.deliver(job)
	}
}

func (d *WebhookDispatcher) deliver(job webhookJob) {
	for {
		err := d.attempt(job)
		if err == nil {
			return
		}
		job.attempt++
		if job.attempt >= d.maxRetries {
			return
		}
		// Exponential backoff with capped sleep.
		delay := d.baseDelay << job.attempt
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		select {
		case <-d.closed:
			return
		case <-time.After(delay):
		}
	}
}

func (d *WebhookDispatcher) attempt(job webhookJob) error {
	mac := hmac.New(sha256.New, []byte(job.sub.Secret))
	mac.Write(job.body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, job.sub.URL, bytes.NewReader(job.body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Harness-Protocol", ProtocolVersion)
	req.Header.Set("X-Harness-Event-Id", job.event.ID)
	req.Header.Set("X-Harness-Signature", sig)
	for k, v := range job.sub.Headers {
		req.Header.Set(k, v)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("webhook %s: status %d", job.sub.ID, resp.StatusCode)
}

// VerifyWebhookSignature is a helper for receivers: returns nil if the
// X-Harness-Signature header matches the body under secret. Constant-time.
func VerifyWebhookSignature(secret, body []byte, headerValue string) error {
	const prefix = "sha256="
	if len(headerValue) <= len(prefix) || headerValue[:len(prefix)] != prefix {
		return errors.New("missing or unsupported signature scheme")
	}
	expected, err := hex.DecodeString(headerValue[len(prefix):])
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), expected) {
		return errors.New("signature mismatch")
	}
	return nil
}

// streamEventDataPayload extracts the relevant payload of a StreamEvent
// for webhook delivery. Bytes are pre-marshaled JSON.
func streamEventDataPayload(ev StreamEvent) (json.RawMessage, error) {
	// Strip non-JSON fields (Error is `json:"-"`) and emit the rest verbatim.
	type wireEvent struct {
		Delta               string             `json:"delta,omitempty"`
		Thinking            string             `json:"thinking,omitempty"`
		ToolCall            *ToolCallEntry     `json:"tool_call,omitempty"`
		ToolResult          *ToolResult        `json:"tool_result,omitempty"`
		Plan                *Plan              `json:"plan,omitempty"`
		SubagentResult      *SubagentResult    `json:"subagent_result,omitempty"`
		ConfirmationRequest *ApprovalRequest   `json:"confirmation_request,omitempty"`
		Interrupt           *InterruptRequest  `json:"interrupt,omitempty"`
		Final               *AgentLoopResult   `json:"final,omitempty"`
	}
	wire := wireEvent{
		Delta:               ev.Delta,
		Thinking:            ev.Thinking,
		ToolCall:            ev.ToolCall,
		ToolResult:          ev.ToolResult,
		Plan:                ev.Plan,
		SubagentResult:      ev.SubagentResult,
		ConfirmationRequest: ev.ConfirmationRequest,
		Interrupt:           ev.Interrupt,
		Final:               ev.Final,
	}
	return json.Marshal(wire)
}
