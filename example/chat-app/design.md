# Chat App Design

## Objetivo

El frontend esta disenado para una experiencia de chat en tiempo real conectada al backend de `backend-chat`, con foco en:

- streaming visible token a token
- control claro de modo y provider
- feedback de eventos internos (tool calls, tool results)
- navegacion rapida entre chats

## Principios de diseno

1. Claridad operacional: el usuario siempre ve estado actual (`Ready`, `Streaming`, `Completed`, `Failed`, `Canceled`).
2. Bajo ruido: UI limpia, una jerarquia fuerte entre sidebar, feed y panel de eventos.
3. Transparencia del runtime: panel lateral de eventos live para no ocultar actividad de tools.
4. Flujo continuo: no hay salto de pantalla durante una corrida; todo ocurre en contexto.
5. Mobile-first practico: layout de una columna en viewport pequeno y dos columnas en desktop.

## Estructura visual

### Layout

- `chat-app-shell`: contenedor principal full-height.
- `chat-app-sidebar`: panel fijo para historial y busqueda de chats.
- `chat-app-main`: area de conversacion y controles.

### Sidebar

- boton `New Chat`
- caja `Search chat...`
- grupos temporales: `Today`, `Yesterday`, `Last 7 Days`, `Older`
- item activo destacado visualmente

### Main area

- bloque de bienvenida (solo cuando no hay mensajes)
- toolbar con selects de `mode` y `provider`
- indicador de estado con icono animado durante stream
- feed de mensajes con burbujas separadas por rol
- panel `Live Events` para tool_call/tool_result/errores/hitos
- input multilinea con acciones `Send` y `Stop`

## Lenguaje visual

- fondo atmosferico con capas de gradientes y paneles glassmorphism suaves
- contraste alto en componentes criticos de accion
- animaciones cortas para nuevos eventos y mensajes (`fade-in-up`)
- microinteracciones solo donde aportan contexto (spin en estado activo)

## Look and Feel

### Direccion estetica

- estilo: productivo + editorial, con aire tecnico pero amigable
- tono: claro, enfocado, con acento moderno en componentes de estado
- objetivo: que el usuario sienta "control en tiempo real" del runtime

### Paleta de color (practica)

1. Fondo global

- base: `#f8fafc -> #f0f6ff -> #fff6ea` (gradiente lineal)
- acentos atmosfericos: `#d9f6ff` y `#ffe9c9` (radiales)

2. Neutros de UI

- texto primario: `zinc-900`
- texto secundario: `zinc-600` / `zinc-500`
- bordes suaves: `zinc-200` / `white/60`
- superficies claras: `white`, `white/65`, `white/80`

3. Acentos funcionales

- foco/seleccion interactiva: `cyan-600` / `cyan-700`
- badge runtime: `cyan-50`, `cyan-200`, `cyan-700`
- CTA principal: `zinc-900` con hover `zinc-700`

4. Estados en panel de eventos

- info: `zinc-800`
- success: `emerald-800/70`
- error: `rose-800/80`

### Tipografia

- familia principal: `IBM Plex Sans`, fallback `Avenir Next`, `Segoe UI Variable`, sans-serif
- familia heading: `Space Grotesk` (fallbacks iguales)
- jerarquia:
  - heading hero: `text-3xl`, semibold, tracking-tight
  - labels de sistema: `text-xs` / `text-sm`
  - cuerpo de mensajes: `text-sm`, `leading-relaxed`

### Superficies y profundidad

- uso de panels translcidos con `backdrop-blur` para separar capas sin ruido
- radios amplios (`rounded-xl` y `rounded-2xl`) para coherencia visual
- sombra concentrada solo en elementos de foco (ej. chat activo en sidebar)
- panel de eventos con fondo oscuro para contraste semantico contra el feed claro

### Layout y densidad

- sidebar fija y compacta para navegacion rapida
- feed principal con espacio respirable (`gap` y padding medios)
- panel de eventos dedicado para observabilidad sin ensuciar mensajes
- desktop: 2 columnas (`1fr + 320px`), mobile: stack vertical

### Motion y microinteracciones

- entrada de mensajes/eventos con `fade-in-up` (220ms)
- spinner continuo para estados de streaming
- transiciones cortas en hover/focus para acciones criticas
- no se usan animaciones decorativas largas para preservar sensacion de velocidad

## Arquitectura de UI

- Route container: `src/routes/index.tsx`
- Sidebar component: `src/components/chat-sidebar/chat-sidebar.tsx`
- Main chat orchestrator: `src/components/chat-main/chat-main.tsx`
- API/SSE client: `src/features/chat/api.ts`
- Shared contracts: `src/features/chat/types.ts`
- Global skin/theme/layout: `src/styles.css`

## Datos y estado

### Estado principal

- chat activo (`activeChatID`, `chatID`)
- lista de mensajes
- input actual
- estado de stream (`isStreaming`, `statusText`)
- timeline de eventos live
- catalogos de modos y providers

### Carga inicial

- modos desde `GET /api/modes` con fallback local
- providers desde `GET /api/providers`
- mensajes de chat activo desde `GET /api/chats/:id/messages`

### Stream

- envio por `POST /api/chats/:id/stream`
- parseo SSE por eventos `delta`, `tool_call`, `tool_result`, `done`, `error`
- abort control via `AbortController` para boton `Stop`

## Responsive behavior

- Desktop: grid `feed + event panel` (2 columnas)
- Mobile/Tablet: stack vertical automatico
- Sidebar mantiene jerarquia y scrolleo independiente

## Restricciones tecnicas

- todos los archivos nuevos/modificados de esta iteracion estan por debajo de 500 lineas
- integracion backend via `VITE_BACKEND_URL` (fallback `http://localhost:9090`)

## Notas para evolucion

1. agregar markdown rendering seguro para respuestas largas
2. virtualizar feed para historiales muy extensos
3. opcion de colapsar/expandir panel de eventos en mobile
4. telemetria de latencia por run (`time to first token`, `time to done`)
