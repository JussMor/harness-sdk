# Obvious — Patrón de Memoria y Contexto

El agente no tiene memoria entre conversaciones por defecto — cada thread empieza en blanco. La memoria en Obvious es un sistema explícito de archivos markdown que el agente lee y escribe conscientemente. El contexto, en cambio, es todo lo que está activo *dentro* de un turno: memoria cargada, skills activas, artifacts visibles, tags de situación. Este documento explica cómo funciona cada capa y cómo interactúan.

---

## Las dos capas: memoria vs. contexto

**Memoria** es persistente. Sobrevive entre conversaciones, entre proyectos, entre días. Es un sistema de archivos markdown con dos scopes: usuario y proyecto. El agente la lee al inicio de cada conversación y la escribe cuando descubre algo que vale la pena recordar.

**Contexto** es efímero. Existe solo dentro del turno actual. Incluye el historial del thread, los resultados de herramientas, las skills cargadas, los tags `<context>` del sistema, y cualquier artifact que el agente haya inspeccionado. Cuando el thread termina, el contexto desaparece — lo que no se guardó en memoria se pierde.

La distinción importa porque define qué información está disponible cuándo. Un agente que confunde las dos capas asume que "recuerda" algo que en realidad solo vio una vez en un thread anterior.

---

## Memoria: el sistema de archivos persistente

### Estructura de scopes

```
User scope  — /profile/communication.md
             /profile/context.md
             /profile/identity.md
             /profile/preferences.md
             /profile/work.md

Project scope — /README.md
                /[archivos específicos del proyecto]
```

El **user scope** es cross-proyecto. Contiene preferencias de comunicación, estilo de trabajo, contexto profesional, y cualquier hecho sobre el usuario que sea útil en cualquier proyecto. Se consulta en cada conversación nueva independientemente del proyecto activo.

El **project scope** es específico del proyecto. Contiene schemas de datos, decisiones técnicas, estado de workflows, convenciones acordadas, y contexto que solo aplica a ese proyecto. El archivo `/README.md` del project scope es el punto de entrada — el agente lo mantiene actualizado automáticamente con el estado del proyecto.

### Operaciones disponibles

```
memory({ operation: "view", path: "/", scope: "*" })         // ver todo
memory({ operation: "view", path: "/README.md", scope: "project" })
memory({ operation: "create", path: "/decisions/api.md", content: "..." })
memory({ operation: "str_replace", path: "/README.md", old_str: "...", new_str: "..." })
memory({ operation: "insert", path: "/file.md", line_number: 5, insert_text: "..." })
memory({ operation: "delete", path: "/archivo.md" })
memory({ operation: "rename", path: "/old.md", new_path: "/new.md" })
```

`scope: "*"` lista ambos scopes simultáneamente (solo para `view`). Para escrituras siempre se especifica el scope explícitamente.

### Cuándo se actualiza

La memoria se actualiza cuando se descubre algo con **leverage real** para conversaciones futuras. No todo va a memoria — el criterio es: ¿evitaría esto una pregunta redundante o un error en el futuro?

Ejemplos de qué sí va a memoria:

- Una decisión técnica que afecta múltiples archivos del proyecto


- Una preferencia del usuario que cambia el formato o tono del output


- Un schema de datos que se usará repetidamente en análisis


- El estado de un workflow largo que puede continuar en otro thread


- Convenciones de naming o estructura acordadas con el usuario



Ejemplos de qué no va a memoria:

- Resultados de análisis one-off que no se reutilizarán


- Contexto que ya está en un artifact del proyecto


- Información que el usuario puede encontrar fácilmente en otro lado



### El README del proyecto

`/README.md` en project scope es el artifact de memoria más importante. El agente lo mantiene con:

- Qué es el proyecto y su estado actual


- Links a artifacts clave con descripción de su propósito


- Qué está completado, en progreso, y como próximos pasos


- Cómo navegar el proyecto



Cuando un agente nuevo llega a un proyecto, leer este README es suficiente para orientarse sin tener que explorar todos los artifacts desde cero.

---

## Contexto: lo que está activo en el turno

### Tags `<context>`

El sistema inyecta automáticamente un bloque `<context>` en cada mensaje del usuario con información situacional:

```xml
<context>
  Current Date & Time: Saturday, May 2, 2026 at 11:22 AM
  Current Timezone: America/Guayaquil
  User: Junior Moreira
  Project: prj_G6uVTYyC
  Thread: th_QyqLAyj7
  Viewing Document: "Obvious — Modelo de Comunicación, Threads y Skills" (art_hGzrbF1T)
  Active Skills: [skills cargadas con su contenido]
  Other Artifacts Available: [lista de artifacts del proyecto]
</context>
```

El usuario **no ve** este bloque — es solo para el agente. Provee orientación situacional: qué artifact está mirando el usuario ahora mismo, qué proyecto está activo, qué hora es, qué skills están cargadas.

El agente usa este contexto para informar su comportamiento pero no lo referencia explícitamente en sus respuestas. "Viewing Document" dice qué tiene el usuario abierto — no es una instrucción de qué crear.

### Skills activas en contexto

Cuando una skill está cargada, su contenido aparece en el bloque `<context>` de cada turno como `Active Skills`. Esto significa que el agente tiene acceso a las instrucciones de la skill en todo momento sin tener que releerla. Una skill cargada persiste en el contexto del thread hasta que se descarga explícitamente.

La diferencia entre una skill cargada y una skill leída con shell:

- **Cargada** (`skills-operations load`): se inyecta automáticamente en cada turno. El agente nunca la pierde aunque el contexto crezca.


- **Leída con shell** (`cat /home/user/skills/...`): aparece como resultado de herramienta en el historial. Puede quedar enterrada o compactada si el thread es largo.



Para tareas largas o multi-step, siempre cargar la skill — no solo leerla.

### Historial del thread como contexto

El historial completo del thread está disponible para el agente dentro de la conversación. Esto incluye todos los mensajes del usuario, todas las respuestas del agente, y todos los resultados de herramientas. Sin embargo, los threads muy largos pueden ser **compactados** — el sistema resume el historial para liberar espacio de contexto, y los detalles específicos de tool calls anteriores pueden perderse.

Cuando hay compactación, el agente ve tags como `<compacted_tool_call>` en lugar del resultado original. Si necesita datos de una operación anterior, debe volver a ejecutarla o leer el artifact donde se guardaron los resultados.

### Artifacts como contexto extendido

Los artifacts del proyecto no están en el contexto por defecto — el agente los descubre y lee activamente. El bloque `<context>` lista qué artifacts existen (`Other Artifacts Available`), pero para leer su contenido el agente usa `explore-artifacts` o `search-workspace`.

Este es un patrón importante: el agente no "sabe" el contenido de los artifacts automáticamente. Debe inspeccionarlos. Asumir el contenido de un artifact sin leerlo es uno de los errores más comunes — especialmente con templates y ejemplos que el usuario provee como referencia.

---

## El patrón de arranque de conversación

Cada conversación nueva sigue este patrón de orientación:

1. **Leer memoria** — `memory({ operation: "view", path: "/", scope: "*" })` para ver qué existe, luego leer los archivos relevantes. El README del proyecto es el primero.


2. **Leer tags de contexto** — qué artifact está viendo el usuario, qué proyecto está activo, qué hora es.


3. **Escanear artifacts** — `explore-artifacts()` para un inventario rápido si la tarea lo requiere.


4. **Verificar skills** — si la tarea coincide con triggers de una skill, cargarla antes de cualquier acción.


5. **Evaluar claridad** — con toda esa información, decidir si hay ambigüedad genuina que requiere una pregunta, o si se puede proceder.



El orden importa. Preguntar antes de leer memoria y contexto genera preguntas redundantes. Actuar antes de leer skills genera outputs que no siguen las convenciones del proyecto.

---

## Memoria en threads programados

Los tasks programados (schedule o webhook) crean threads nuevos que empiezan sin historial. Pero sí tienen acceso a memoria. Esto significa que un workflow largo puede guardar su estado en project memory al final de cada run, y el siguiente run puede leerlo para continuar desde donde quedó.

Patrón para workflows multi-run:

```markdown
# /workflow-state/daily-report.md (project scope)

last_run: 2026-05-02T09:00:00Z
last_processed_record_id: rec_abc123
status: completed
records_processed: 847
```

El siguiente run lee este archivo, verifica el estado, y continúa o recrea según corresponda. Sin este patrón, cada run empieza desde cero y crea duplicados.

---

## Memoria vs. artifacts: cuándo usar cada uno

| Situación | Usar memoria | Usar artifact |
| --- | --- | --- |
| Estado de un workflow que continúa en otro thread | ✅ | ❌ |
| Preferencia del usuario sobre formato de outputs | ✅ | ❌ |
| Decisión técnica que afecta múltiples sesiones | ✅ | ❌ |
| Análisis de datos con 500+ filas | ❌ | ✅ workbook |
| Documento que el usuario necesita ver y editar | ❌ | ✅ document |
| Schema de una sheet que se usa repetidamente | ✅ referencia | ✅ sheet |
| Resultado de una investigación one-off | ❌ | ✅ document |
| Convención de naming del proyecto | ✅ | ❌ |

La regla de oro: si el usuario necesita verlo o interactuar con ello, va en artifact. Si el agente necesita recordarlo entre sesiones para trabajar mejor, va en memoria.

---

## Límites del sistema

**La memoria no es un log.** No se debe guardar todo — solo lo que tiene leverage. Una memoria sobrecargada de detalles irrelevantes es tan inútil como una memoria vacía.

**El contexto no es memoria.** Lo que el agente vio en un thread anterior no está disponible en el siguiente a menos que se haya guardado explícitamente en memoria o en un artifact.

**Las skills no son contexto automático.** Una skill existe en disco pero no está activa hasta que se carga. El agente no "sabe" el contenido de una skill solo porque existe.

**Los artifacts no son contexto automático.** Saber que un artifact existe (desde el tag `<context>`) no es lo mismo que conocer su contenido. Siempre leer antes de asumir.