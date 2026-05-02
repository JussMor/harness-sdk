# backend-chat

Independent backend project for the chat app.

## Run Centrifugo

```bash
docker compose up -d centrifugo
```

Centrifugo config is in `centrifugo/config.json`.

## Run Backend

```bash
cp .env.example .env
# export vars from .env if desired
go run .
```

## API

- `GET /healthz`
- `GET /api/providers`
- `GET /api/chats`
- `POST /api/chats`
- `GET /api/chats/{id}/messages`
- `POST /api/chats/{id}/messages`
- `POST /api/chats/{id}/run`

### Dynamic provider per request

`POST /api/chats/{id}/run` accepts optional fields:

- `provider`: `anthropic` | `openai` | `ollama`
- `model`: model name, with or without provider prefix

Examples:

```json
{
  "prompt": "hola",
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514"
}
```

```json
{ "prompt": "hola", "model": "openai/gpt-4o" }
```

## Notes

- Messages are persisted in SQLite (`chat.db`).
- Backend publishes `message.created` events to Centrifugo channel `chat:{chatID}`.
