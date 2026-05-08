package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// ── Generative-UI component catalog ──────────────────────────────────────────
//
// The backend exposes a single allowlist of component names that the frontend
// (example/chat-app/src/lib/component-catalog.tsx) knows how to render. The
// LLM picks a name from this list and supplies free-form props as JSON. We
// *do not* render JSX/HTML on the wire — the frontend looks up the component
// by name, validates props at render time, and mounts it.
//
// To add a new component:
//  1. Implement and register it in component-catalog.tsx (frontend).
//  2. Add an entry below describing when to use it and its props shape.
//  3. The system prompt includes this list automatically.

type componentSpec struct {
	Name        string
	Description string
	PropsHint   string // freeform JSON schema hint for the LLM
}

// componentCatalog is the source of truth for what the LLM may render.
// Keep this in sync with the frontend componentCatalog object.
var componentCatalog = []componentSpec{
	{
		Name:        "PatientChart",
		Description: "Display a single patient's identity and vitals.",
		PropsHint:   `{"patientId":"string","name":"string","age":number,"vitals":{"bp":"string","hr":number,"temp":number}}`,
	},
	{
		Name:        "MedicationList",
		Description: "List a patient's active medications.",
		PropsHint:   `{"medications":[{"name":"string","dose":"string","frequency":"string"}]}`,
	},
	{
		Name:        "AppointmentForm",
		Description: "Render a form to schedule an appointment for a patient.",
		PropsHint:   `{"patientId":"string","defaultDate":"YYYY-MM-DDTHH:MM"}`,
	},
}

// describeComponentCatalog renders the catalog for the system prompt so the
// LLM knows which names + prop shapes are valid.
func describeComponentCatalog() string {
	if len(componentCatalog) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available generative-UI components (use via `render_component`):\n")
	for _, c := range componentCatalog {
		fmt.Fprintf(&b, "- %s — %s\n  props: %s\n", c.Name, c.Description, c.PropsHint)
	}
	b.WriteString("\nPlacement values:\n")
	b.WriteString("- `canvas` (default) — mount in the side artifact panel; use for dashboards or rich UIs that need their own real estate.\n")
	b.WriteString("- `inline` — mount inside the chat bubble; use for short widgets the user reads alongside your reply (a single chart card, a confirmation form, a stat tile).\n")
	return b.String()
}

func componentNameAllowed(name string) bool {
	for _, c := range componentCatalog {
		if c.Name == name {
			return true
		}
	}
	return false
}

func componentNameList() []string {
	names := make([]string, 0, len(componentCatalog))
	for _, c := range componentCatalog {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

// newRenderComponentTool returns a tool that emits a ComponentArtifact so the
// frontend renders a registered React component (NOT a file). Use this when
// the user wants a domain UI (chart, form, dashboard) instead of code.
func (r *agentRuntime) newRenderComponentTool() *ab.Tool {
	return &ab.Tool{
		Name: "render_component",
		Description: "Render one of the app's pre-registered generative-UI React components in the canvas. " +
			"Use this — NOT file_write — whenever the user asks to display, show, or visualize a domain UI " +
			"(patient chart, medication list, appointment form, etc.). The frontend mounts the component by " +
			"name; you do not write JSX. Pick `name` from the catalog and supply matching `props`.",
		Category: ab.ToolCategoryCustom,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"name": {
					Type:        "string",
					Description: "Component name from the catalog.",
					Enum:        componentNameList(),
				},
				"props": {
					Type:        "object",
					Description: "Free-form JSON props matching the component's expected shape (see system prompt for each component).",
				},
				"placement": {
					Type:        "string",
					Description: "Where to mount the component. `canvas` (default) = side panel; `inline` = inside the chat bubble for the current turn.",
					Enum:        []string{"canvas", "inline"},
				},
			},
			Required: []string{"name"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			name := strings.TrimSpace(asString(args["name"]))
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if !componentNameAllowed(name) {
				return "", fmt.Errorf("unknown component %q (allowed: %s)", name, strings.Join(componentNameList(), ", "))
			}
			props, _ := args["props"]

			art, err := ab.NewComponentArtifact(name, props)
			if err != nil {
				return "", fmt.Errorf("build component artifact: %w", err)
			}
			placement := strings.TrimSpace(asString(args["placement"]))
			switch placement {
			case "inline":
				art.Placement = ab.ArtifactPlacementInline
			default:
				art.Placement = ab.ArtifactPlacementCanvas
			}
			if !ab.EmitArtifact(ctx, art) {
				// Outside of RunStream there is no emitter — surface a clear
				// error rather than silently dropping the call.
				return "", fmt.Errorf("artifact emitter not installed (component must be rendered within a streaming turn)")
			}
			return fmt.Sprintf("rendered component %s (id=%s)", name, art.ID), nil
		},
	}
}

// newAwaitComponentInputTool returns a tool that renders a component AND
// pauses the agent loop until the user submits the form. The component must
// implement an `onSubmit(data)` callback (the frontend renderer wires this
// to /api/interrupts/:token/resolve). The user's submission is returned as
// a JSON string the model continues to reason against.
func (r *agentRuntime) newAwaitComponentInputTool(chatID int64) *ab.Tool {
	return &ab.Tool{
		Name: "await_component_input",
		Description: "Render an interactive component (form, picker, etc.) and PAUSE the agent loop " +
			"until the user submits it. Use this when you need structured data from the user " +
			"that's better collected through a UI than a free-form question — e.g. dates, " +
			"choices, multi-field forms. The user's submission is returned as JSON so you can " +
			"reason about it on the next turn. Prefer `render_component` when no input is needed.",
		Category: ab.ToolCategoryCustom,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"name": {
					Type:        "string",
					Description: "Component name from the catalog. Must be a component that accepts an onSubmit prop.",
					Enum:        componentNameList(),
				},
				"props": {
					Type:        "object",
					Description: "Initial props for the component (e.g. patientId, defaults).",
				},
				"placement": {
					Type:        "string",
					Description: "Where to mount the component while waiting. Default `canvas`.",
					Enum:        []string{"canvas", "inline"},
				},
				"prompt": {
					Type:        "string",
					Description: "Optional human-readable hint shown alongside the form (e.g. \"Pick a time for the appointment\").",
				},
			},
			Required: []string{"name"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			name := strings.TrimSpace(asString(args["name"]))
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if !componentNameAllowed(name) {
				return "", fmt.Errorf("unknown component %q (allowed: %s)", name, strings.Join(componentNameList(), ", "))
			}
			props, _ := args["props"]
			prompt := strings.TrimSpace(asString(args["prompt"]))

			// Look up the runtime's interrupt gate so we can mint a resolution
			// token the frontend will POST against.
			gate, ok := hilRegistry.Get(chatID)
			if !ok || gate == nil {
				return "", fmt.Errorf("no interrupt gate for chat %d (must be wired in main.go)", chatID)
			}
			inner := gate.Inner()
			if inner == nil {
				return "", fmt.Errorf("interrupt gate has no inner gate")
			}

			// Use a fresh ID per call so the same component can be requested
			// multiple times in the same turn without token clashes.
			interruptID := fmt.Sprintf("int_form_%s_%d", name, time.Now().UnixNano())
			token, err := inner.IssueResolutionToken(interruptID, 0)
			if err != nil {
				return "", fmt.Errorf("issue resolution token: %w", err)
			}

			// Emit the component artifact wired with an interaction token so
			// the frontend renderer can post submissions back.
			art, err := ab.NewComponentArtifact(name, props)
			if err != nil {
				return "", fmt.Errorf("build component artifact: %w", err)
			}
			placement := strings.TrimSpace(asString(args["placement"]))
			switch placement {
			case "inline":
				art.Placement = ab.ArtifactPlacementInline
			default:
				art.Placement = ab.ArtifactPlacementCanvas
			}
			art.Interaction = &ab.ArtifactInteraction{Token: token, ChatID: chatID}
			if !ab.EmitArtifact(ctx, art) {
				return "", fmt.Errorf("artifact emitter not installed")
			}

			// Pause the loop. The frontend resolves via
			//   POST /api/interrupts/:token/resolve
			// which routes to inner.ResolveByToken(token, resp).
			req := ab.InterruptRequest{
				ID:        interruptID,
				Kind:      ab.InterruptKindFormInput,
				Reason:    prompt,
				CreatedAt: time.Now(),
				Form: &ab.FormPayload{
					Title:  prompt,
					UIHint: name,
				},
			}
			resp, err := ab.RequestInterrupt(ctx, req)
			if err != nil {
				return "", fmt.Errorf("await user input: %w", err)
			}
			if !resp.Approved {
				return "user cancelled the form", nil
			}
			if len(resp.Answer) == 0 {
				return "user submitted an empty form", nil
			}
			return string(resp.Answer), nil
		},
	}
}
