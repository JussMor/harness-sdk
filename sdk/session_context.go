package autobuild

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SessionContext is the situational awareness an agent needs every turn:
// who the user is, where they are, what time it is, what they're looking at.
//
// Without this, the agent makes time-blind and location-blind decisions:
//   - Searching for "news today" when "today" is unknown
//   - Recommending restaurants without knowing the city
//   - Greeting the user with the wrong name
//
// Claude's runtime injects equivalent context every turn. The SDK does
// not generate this — the application provides it via SessionContextProvider.
type SessionContext struct {
	// Time and locale
	Now      time.Time `json:"now"`
	Timezone string    `json:"timezone,omitempty"` // IANA name, e.g. "America/Guayaquil"
	Locale   string    `json:"locale,omitempty"`   // BCP 47, e.g. "es-EC"

	// User identity
	UserID       string   `json:"user_id,omitempty"`
	UserName     string   `json:"user_name,omitempty"`
	UserPronouns string   `json:"user_pronouns,omitempty"`

	// Approximate location (city-level — no precise coordinates by default)
	UserCity     string   `json:"user_city,omitempty"`
	UserRegion   string   `json:"user_region,omitempty"` // state/province
	UserCountry  string   `json:"user_country,omitempty"`

	// What the user is currently doing
	ActiveArtifact string   `json:"active_artifact,omitempty"` // file path, doc ID, etc.
	ActiveProject  string   `json:"active_project,omitempty"`
	Surface        string   `json:"surface,omitempty"` // "chat", "code", "browser", "mobile"

	// Free-form notes the application wants the agent to know
	Notes []string `json:"notes,omitempty"`
}

// Format renders the context as a compact human-readable block suitable
// for injection into LayerSession of the system prompt.
//
// Keep it short — every turn pays the token cost.
func (sc *SessionContext) Format() string {
	if sc == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Current session context:\n")

	// Time
	if !sc.Now.IsZero() {
		tz := sc.Timezone
		if tz == "" {
			tz = "UTC"
		}
		b.WriteString(fmt.Sprintf("- Time: %s (%s)\n",
			sc.Now.Format("Monday, January 2, 2006 15:04"), tz))
	}
	if sc.Locale != "" {
		b.WriteString(fmt.Sprintf("- Locale: %s\n", sc.Locale))
	}

	// User
	if sc.UserName != "" {
		line := "- User: " + sc.UserName
		if sc.UserPronouns != "" {
			line += " (" + sc.UserPronouns + ")"
		}
		b.WriteString(line + "\n")
	}

	// Location — only emit if at least one field is set
	if sc.UserCity != "" || sc.UserRegion != "" || sc.UserCountry != "" {
		var parts []string
		if sc.UserCity != "" {
			parts = append(parts, sc.UserCity)
		}
		if sc.UserRegion != "" {
			parts = append(parts, sc.UserRegion)
		}
		if sc.UserCountry != "" {
			parts = append(parts, sc.UserCountry)
		}
		b.WriteString("- Location: " + strings.Join(parts, ", ") + "\n")
	}

	// What's active
	if sc.Surface != "" {
		b.WriteString(fmt.Sprintf("- Surface: %s\n", sc.Surface))
	}
	if sc.ActiveProject != "" {
		b.WriteString(fmt.Sprintf("- Project: %s\n", sc.ActiveProject))
	}
	if sc.ActiveArtifact != "" {
		b.WriteString(fmt.Sprintf("- Active artifact: %s\n", sc.ActiveArtifact))
	}

	// Notes
	for _, n := range sc.Notes {
		b.WriteString("- " + n + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// SessionContextProvider produces a fresh SessionContext for each turn.
// Implementations should be cheap — this is called every Run.
type SessionContextProvider interface {
	Get(ctx context.Context, conv *Conversation) (*SessionContext, error)
}

// SessionContextProviderFunc adapts a function to SessionContextProvider.
type SessionContextProviderFunc func(ctx context.Context, conv *Conversation) (*SessionContext, error)

func (f SessionContextProviderFunc) Get(ctx context.Context, conv *Conversation) (*SessionContext, error) {
	return f(ctx, conv)
}

// StaticSessionContext returns a provider that always emits the same context
// (with Now refreshed to the current call). Useful for single-user apps
// where the user identity is known at startup.
func StaticSessionContext(base SessionContext) SessionContextProvider {
	return SessionContextProviderFunc(func(_ context.Context, _ *Conversation) (*SessionContext, error) {
		fresh := base
		fresh.Now = time.Now()
		return &fresh, nil
	})
}

// LocalTimeSessionContext returns a minimal provider that only fills Now and Timezone
// from the local system. Useful as a baseline when no user data is available.
func LocalTimeSessionContext() SessionContextProvider {
	return SessionContextProviderFunc(func(_ context.Context, _ *Conversation) (*SessionContext, error) {
		now := time.Now()
		zone, _ := now.Zone()
		return &SessionContext{
			Now:      now,
			Timezone: zone,
		}, nil
	})
}
