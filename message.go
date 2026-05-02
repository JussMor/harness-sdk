package autobuild

import "time"

// DeliveryMode determines when a message is delivered to the receiving thread.
type DeliveryMode string

const (
	// DeliveryInterjected delivers the message after the current tool call
	// in the receiving thread.
	DeliveryInterjected DeliveryMode = "interjected"

	// DeliveryQueued delivers the message after the receiving thread's
	// current execution completes.
	DeliveryQueued DeliveryMode = "queued"
)

// Message is a unit of communication between threads.
type Message struct {
	ID           string       `json:"id"`
	FromThreadID string       `json:"from_thread_id"`
	ToThreadID   string       `json:"to_thread_id"`
	Content      string       `json:"content"`
	Delivery     DeliveryMode `json:"delivery_mode"`
	CreatedAt    time.Time    `json:"created_at"`
}
