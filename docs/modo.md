# Obvious — Modos, Orientación y Carga de Contexto

El modo determina qué tan profundo trabaja el agente. El contexto determina qué sabe cuando empieza. Entender cómo interactúan estos dos sistemas explica por qué el agente se comporta diferente en tareas distintas — y cómo sacarle más provecho.

---

## Los modos disponibles

Obvious tiene tres modos base y soporte para modos personalizados.

### Balanced (default)

El modo estándar. Equilibra velocidad y calidad. Hace preguntas cuando hay ambigüedad genuina, usa herramientas en paralelo, produce artifacts de calidad profesional. Adecuado para el 80% de las tareas: análisis, documentos, código, coordinación.

### Analyst

Orientado a análisis profundo de datos. Prioriza SQL, exploración de schemas, visualizaciones, y razonamiento estadístico. Útil cuando la tarea principal es entender un dataset, identificar patrones, o producir insights cuantitativos.

### Deep Work

Máxima profundidad de razonamiento. Dedica más tiempo a planificación, reflexión entre pasos, y verificación. El output tarda más pero es más exhaustivo. Útil para arquitectura de sistemas, investigación compleja, o decisiones con consecuencias grandes.

### Modos personalizados

Cualquier modo puede clonarse y configurarse con un prompt propio, modelo específico, temperatura, y restricciones de herramientas. Un modo personalizado puede, por ejemplo, restringir el agente a solo leer datos sin escribir artifacts, o forzar que siempre use un modelo de razonamiento específico.

```javascript
custom -
  mode -
  operations({
    operation: "create",
    name: "Code Reviewer",
    baseModeId: "auto",
    promptStrategy: "additions",
    promptContent:
      "Enfócate exclusivamente en revisar código. No crees artifacts. Solo comenta en el chat.",
    modelSettingsOverride: { reasoning_effort: "high" },
  });
```

---

## Cómo el agente sabe qué cargar al arrancar

Al inicio de cada conversación, el agente no tiene estado. No recuerda el thread anterior, no sabe qué artifacts existen, no conoce las preferencias del usuario. Reconstruye ese contexto en los primeros turnos a partir de tres fuentes.

### Fuente 1 — El system prompt

El system prompt está siempre presente. Contiene las reglas de comportamiento, la lista de herramientas disponibles, la lista de skills instaladas con sus triggers, la estructura del workspace, los computers registrados, y los repo sandboxes activos. Es el conocimiento base que no cambia entre sesiones.

Lo que el system prompt **no** contiene: el estado actual del proyecto, los artifacts existentes, las decisiones tomadas en sesiones anteriores, las preferencias específicas del usuario.

### Fuente 2 — La memoria persistente

La memoria llena el gap entre sesiones. El agente la lee al inicio de cada conversación:

```
memory({ operation: "view", path: "/", scope: "*" })
```

Esto devuelve el índice de todos los archivos de memoria en dos scopes:

**User scope** — persiste entre proyectos. Contiene el perfil del usuario: preferencias de comunicación, estilo de trabajo, convenciones acordadas, hechos sobre su rol y contexto. Vive en `/profile/`.

**Project scope** — específico al proyecto activo. El archivo más importante es `/README.md`, que el agente mantiene actualizado con el estado del proyecto, artifacts clave, decisiones técnicas, y próximos pasos. Cuando un agente nuevo entra a un proyecto, el README es su briefing.

La memoria es texto plano en archivos `.md`. El agente puede leer, crear, editar, y hacer search/replace en estos archivos. Lo que va a memoria: decisiones que afectan múltiples sesiones, preferencias que cambian el formato del output, estado de workflows en curso, convenciones acordadas. Lo que no va: resultados intermedios, datos temporales, información que ya está en artifacts.

### Fuente 3 — El contexto del thread activo

Cada mensaje del usuario puede incluir un bloque `<context>` con información sobre el estado actual de la UI: qué artifact está abierto, qué thread está activo, qué artifacts existen en el proyecto, qué skills están cargadas. Este bloque es invisible para el usuario — solo lo ve el agente.

El `<context>` es situacional: describe el momento presente, no el historial. Combinado con la memoria, le da al agente tanto el estado actual como el histórico del proyecto.

---

## Cómo el agente decide qué skills cargar

Las skills no se cargan automáticamente. El agente evalúa si la tarea coincide con los triggers de alguna skill y decide cargarla.

### El mecanismo de triggers

Cada skill declara una lista de palabras clave que activan su carga. El agente compara la solicitud del usuario contra esos triggers:

```
writing:     "writing, enhance document, rewrite, improve writing, polish document, edit for clarity"
dataviz:     "chart, plotly, visualization, graph, plot, heatmap, bar chart, line chart"
folio-builder: "folio, presentation, landing page, report, article, dashboard, memo"
web-hosting: "web server, hosting, public url, serve files, port, localhost, iframe, embed"
canvas-builder: "canvas, drawing, diagram, flowchart, sketch, wireframe, excalidraw"
```

Si el usuario dice "crea un reporte ejecutivo", el agente reconoce "reporte" como trigger de `writing` y `folio-builder`, evalúa cuál es más apropiado según el contexto, y carga esa skill antes de actuar.

### Por qué las skills tienen prioridad sobre el training data

El training data del agente es genérico y tiene corte a finales de 2024. Las skills contienen patrones específicos para este proyecto y este usuario, validados y actualizados. Cuando hay conflicto entre lo que el agente "sabe" por training y lo que dice una skill, la skill gana siempre.

Ejemplo concreto: el agente podría generar un documento con una estructura razonable basada en training. Pero si la skill `writing` especifica BLUF-first con prosa en lugar de bullets para argumentos, esa regla aplica aunque el agente hubiera elegido diferente por defecto.

### Cargar vs. leer una skill

Hay dos formas de acceder a una skill:

**Cargar** — inyecta el contenido de la skill en el contexto activo del agente. Permanece visible en cada turno del thread mientras esté cargada. El agente no necesita releerla entre pasos.

```javascript
skills - operations({ operation: "load", skillName: "writing" });
```

**Leer** — accede al archivo de la skill una vez, como cualquier otro archivo. El contenido aparece en el historial del thread pero puede compactarse si el thread crece mucho.

```bash
cat /home/user/skills/writing/SKILL.md
```

Para tareas largas o multi-step, cargar es más confiable que leer. Para una consulta rápida de referencia, leer es suficiente.

**Descargar** cuando ya no se necesita:

```javascript
skills - operations({ operation: "unload", skillName: "writing" });
```

Descargar libera espacio en el contexto activo. En threads muy largos con múltiples skills cargadas, esto previene degradación de performance.

### Skills personalizadas (user-\*)

Además de las skills del sistema, el agente puede tener skills personalizadas instaladas en `/home/user/skills/user-{name}/`. Estas siguen el mismo mecanismo de triggers pero contienen conocimiento específico del usuario o proyecto.

En este proyecto, las skills personalizadas cubren el dominio completo de CutList Pro: modelo de dominio, optimizador guillotina, motor de pricing, preview isométrico, reglas por rol, editor de taller, API del worker, integración Odoo, y cliente HTTP. Cuando una tarea toca cualquiera de esos dominios, el agente carga la skill correspondiente antes de actuar.

---

## El orden de carga al inicio de un thread

Cuando el agente recibe el primer mensaje en un thread nuevo, este es el orden de operaciones:

```
1. Leer el system prompt (automático, siempre presente)
   └─ conoce: herramientas, skills disponibles, computers, reglas de comportamiento

2. Leer memoria en paralelo
   memory({ scope: "user", path: "/profile/..." })
   memory({ scope: "project", path: "/README.md" })
   └─ conoce: preferencias del usuario, estado del proyecto, artifacts clave

3. Evaluar el request contra triggers de skills
   └─ si hay match → cargar skill antes de responder

4. Explorar artifacts si la tarea lo requiere
   explore-artifacts()
   └─ conoce: qué sheets, docs, workbooks existen y su schema

5. Responder o preguntar con contexto completo
```

Los pasos 2 y 4 van en paralelo cuando ambos son necesarios. El agente no espera la memoria para empezar a explorar artifacts — los lanza simultáneamente.

---

## Cómo el contexto se degrada en threads largos

El contexto del agente tiene un límite de tokens. En threads muy largos, el historial de conversación se compacta automáticamente: los mensajes antiguos se resumen o se eliminan del contexto activo.

Esto tiene dos consecuencias importantes:

**Skills leídas (no cargadas) pueden perderse.** Si el agente leyó una skill al inicio del thread pero el thread creció mucho, el contenido de esa lectura puede compactarse. Las skills _cargadas_ con `skills-operations({ load })` están protegidas — se reinyectan en cada turno.

**Decisiones tomadas hace muchos mensajes pueden no estar disponibles.** Si una decisión importante se tomó en el turno 5 y el thread está en el turno 80, esa decisión puede haberse compactado. La solución es guardarla en memoria cuando se toma, no confiar en que el historial la preserve.

El patrón correcto para threads largos:

- Cargar skills con `skills-operations({ load })`, no solo leerlas

- Guardar decisiones importantes en memoria inmediatamente

- Usar el README del proyecto como fuente de verdad del estado actual

---

## Modos personalizados — cuándo crearlos

Un modo personalizado tiene sentido cuando un agente necesita comportarse de forma consistentemente diferente al default para un caso de uso específico. Ejemplos prácticos:

**Modo solo-lectura** — para análisis donde no se quiere que el agente modifique nada:

```javascript
promptContent: "Solo lees y analizas. No crees, edites, ni elimines artifacts. Reporta hallazgos en el chat."
toolsMode: "denylist",
toolsList: ["document-operations", "sheet-operations", "delete", "eval-js"]
```

**Modo código** — para un agente especializado en un repo específico:

```javascript
promptContent: "Trabajas exclusivamente en el repo cutlist-pro. Siempre usa computerId cmp_0t8dU8V7. Carga las skills user-cutlist-* relevantes antes de cada tarea.";
modelSettingsOverride: {
  reasoning_effort: "high";
}
```

**Modo revisión** — para code review sin ejecución:

```javascript
promptContent: "Revisas PRs. Lees el diff, identificas problemas, y comentas. No ejecutas código ni creas artifacts."
toolsMode: "allowlist",
toolsList: ["computer-ops", "web-operations", "memory", "document-operations"]
```

Los modos personalizados se pueden compartir a nivel de workspace o equipo, lo que permite estandarizar comportamientos para todos los miembros.
