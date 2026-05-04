# Chat App Features

## Resumen

Este documento lista funcionalidades implementadas en el frontend actual y su mapeo al backend.

## Core chat features

1. Crear chat nuevo

- Endpoint: `POST /api/chats`
- Comportamiento: crea chat automaticamente al primer prompt si no hay chat activo.

2. Listar chats

- Endpoint: `GET /api/chats`
- Comportamiento: carga y refresca historial; ordena por `updatedAt` descendente.

3. Seleccionar chat

- Endpoint relacionado: `GET /api/chats/:id/messages`
- Comportamiento: al seleccionar item en sidebar, carga mensajes historicos del chat.

4. Enviar prompt y recibir stream en vivo

- Endpoint: `POST /api/chats/:id/stream`
- Eventos SSE soportados:
  - `delta`
  - `tool_call`
  - `tool_result`
  - `done`
  - `error`

5. Cancelar stream

- Tecnica: `AbortController`
- UI: boton `Stop`
- Resultado: corta request activa y actualiza estado a `Canceled`.

## Runtime observability

6. Panel de eventos live

- Muestra eventos de tools y errores en tiempo real.
- Mantiene ventana rotativa de ultimos eventos.

7. Estado operacional visible

- Estados mostrados: `Ready`, `Streaming response...`, `Completed`, `Failed`, `Canceled`.
- Indicador visual con icono animado durante stream.

## Catalogos dinamicos

8. Selector de modos

- Endpoint: `GET /api/modes`
- Fallback local disponible si backend falla.

9. Selector de providers

- Endpoint: `GET /api/providers`
- Usa provider default cuando esta disponible.

## UX features

10. Sidebar con busqueda local

- filtra chats por texto en titulo
- agrupacion temporal automatica:
  - `Today`
  - `Yesterday`
  - `Last 7 Days`
  - `Older`

11. Feed de mensajes con roles

- estilos distintos para `user` y `assistant`
- marca de pending durante generacion de respuesta

12. Input multilinea optimizado

- `Enter` envia
- `Shift+Enter` inserta nueva linea
- bloqueo de acciones mientras hay stream activo

## Integracion y configuracion

13. URL backend configurable

- variable: `VITE_BACKEND_URL`
- fallback: `http://localhost:8080`

14. Cliente API dedicado

- clase `ChatAPI` centraliza llamadas HTTP y parseo SSE
- tipos compartidos en `features/chat/types.ts`

## Seguridad y robustez funcional

15. Manejo de errores por request

- lecturas JSON con validacion de `response.ok`
- error propagation con mensajes de backend cuando existen

16. Degradacion controlada

- si fallan modos/providers, la UI sigue operativa con defaults
- si falla stream, se conserva mensaje asistente con feedback de error

## Limites y alcance actual

- no hay markdown renderer enriquecido (texto plano)
- no hay soporte de adjuntos en esta version
- no hay persistencia local de preferencias de modo/provider

## Backlog sugerido

1. markdown renderer + bloques de codigo
2. retry/backoff para stream e inicializacion de catalogos
3. virtualizacion del feed para chats grandes
4. filtros avanzados en sidebar (provider, modo, rango fecha)
5. tests E2E para secuencia completa create -> stream -> done
