# Harness SDK — protocol generation
#
# All TypeScript types in clients/harness-client/src/generated/ are produced
# from the Go SDK so the wire protocol has a single source of truth.
#
# Targets:
#   make client-types         Run tygo to regenerate types.ts.
#   make client-events        Run gen-events to regenerate events.ts.
#   make client               Both of the above.
#   make client-types-check   CI guard: regenerate and fail on diff.
#
# Requires Go 1.22+ and a configured go workspace (go.work at repo root).

TYGO       ?= go run github.com/gzuidhof/tygo@latest
GEN_EVENTS ?= go run ./scripts/gen-events
GENERATED  := clients/harness-client/src/generated

.PHONY: client client-types client-events client-types-check

client: client-types client-events

client-types:
	$(TYGO) generate --config clients/tygo.yaml

client-events:
	$(GEN_EVENTS)

client-types-check: client
	@if ! git diff --exit-code -- $(GENERATED); then \
		echo ""; \
		echo "ERROR: generated client types are out of date."; \
		echo "       Run 'make client' and commit the result."; \
		exit 1; \
	fi
