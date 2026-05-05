# Memory System — Documentación

Documentación del sistema de memoria del backend, incluyendo estructura, lifecycle, BM25 search y configuración.

---

## Arquitectura

```
Usuario escribe mensaje
        ↓
Runtime — Phase 0: Orientation (cold turn only)
  ├── Lee user/profile/  → "## User profile & preferences"
  ├── Lee user/facts/    → "## Remembered facts"
  └── Lee project/       → "## Project context"
        ↓
LayerMemory del system prompt se inyecta con el contenido
(token cap: 8,000 tokens — evicts oldest entries si excede)
        ↓
LLM responde usando el contexto de memoria
        ↓
Runtime — Phase 5: Closure
  ├── MemoryTriggerDetector: detecta triggers explícitos en el mensaje
  │   ├── "Remember that X"  → create /facts/timestamp.md
  │   ├── "I now work at X"  → search similar → replace (no duplicate)
  │   └── "Forget about X"   → search similar → delete
  └── InferredMemoryWriter: LLM extrae hechos persistentes del turno
      ├── Confidence ≥ 0.75 requerido
      ├── WriteWithDedup: busca similar antes de crear (Dice coefficient)
      └── Merge: StrReplace si similarity ≥ 0.6
```

---

## Estructura de directorios

```
{BACKEND_MEMORY_ROOT}/          (default: ./memory/)
├── user/
│   ├── profile/                → preferencias, identidad, estilo de trabajo
│   │   └── work.md
│   │   └── preferences.md
│   └── facts/                  → hechos explícitos e inferidos sobre el usuario
│       └── 1234567890.md
│       └── inferred-9876543.md
└── project/                    → contexto del proyecto, decisiones, estado
    ├── architecture.md
    ├── decisions/
    └── facts/
```

Los archivos son markdown plano. `LayeredFilesystemMemory` puede añadir frontmatter YAML opcional:

```markdown
---
layer: explicit
confidence: 0.9
---
El usuario prefiere TypeScript sobre JavaScript.
```

---

## Scopes

| Scope | Persistencia | Uso |
|---|---|---|
| `user` | Cross-project | Preferencias, identidad, patrones de trabajo |
| `project` | Por proyecto | Decisiones, arquitectura, estado del workflow |
| `session` | Efímero (ObservationStore) | "Está depurando el auth ahora mismo" |

---

## Search — BM25

La búsqueda usa BM25 (k1=1.2, b=0.75) en lugar de substring naive:

```
Score(d, q) = Σ IDF(qt) × TF(qt, d) × (k1+1) / (TF + k1(1-b+b×dl/avgdl))
```

Los resultados se ordenan por score descendente — las entradas más relevantes al query aparecen primero. Esto alimenta `WriteWithDedup` (busca entradas similares antes de crear duplicados) y `handleMemoryTrigger` (actualiza en lugar de crear duplicados).

---

## Memory Trigger Detector

El `DefaultMemoryTriggerDetector` detecta intención de escritura/borrado en el mensaje del usuario:

| Patrón (EN/ES) | Operación |
|---|---|
| "Remember that X" / "Recuerda que X" | Create `/facts/timestamp.md` |
| "I now work at X" / "Ahora trabajo en X" | Search similar → Replace |
| "I no longer X" / "Ya no X" | Search similar → Replace |
| "Forget about X" / "Olvida X" | Search similar → Delete |
| "My name is X" / "Me llamo X" | Search similar → Replace/Create |

Si encuentra una entrada similar (similarity ≥ 0.5), usa `StrReplace` en lugar de `Create`. Esto evita "I work at Acme" y "I now work at Beta" coexistiendo como duplicados.

---

## Layers

```
Explicit  (priority 3) → instrucciones directas del usuario
Inferred  (priority 2) → hechos derivados de la conversación
Session   (priority 1) → ephemeral, ObservationStore only
```

`LayeredFilesystemMemory.SearchLayered` devuelve resultados ordenados por Explicit > Inferred > Session.

---

## Token eviction

Con `WithMaxMemoryTokens(8000)`:

1. El Runtime mide los tokens del contenido de memoria combinado
2. Si excede 8,000 tokens → `evictMemoryToTokenBudget()`
3. Estrategia: keep most recent paragraphs (bottom-up), truncate from top
4. Añade `[Earlier memory entries omitted — token budget exceeded]`

Esta es la misma estrategia que usa Claude: recency bias en memory eviction.

---

## InferredMemoryWriter

Después de cada turno, el LLM analiza la conversación y extrae hechos persistentes:

```go
&ab.InferredMemoryWriter{
    Provider:        provider,  // misma LLM que el agente
    Model:           model,
    MaxFacts:        3,         // máximo 3 hechos por turno
    MinConfidence:   0.75,      // descarta hechos de baja confianza
    DedupeThreshold: 0.6,       // Dice coefficient para dedup
}
```

Solo se escriben hechos con estas características:
- Revelan preferencias estables, identidad, o patrones de trabajo
- Representan decisiones que afectarán sesiones futuras
- No son efímeros ("está depurando X ahora")
- No son conocimiento general obvio

---

## Configuración

```bash
BACKEND_MEMORY_ROOT=./memory    # directorio raíz (default: ./memory)
```

El directorio se crea automáticamente al arrancar con la estructura correcta:
```
memory/user/profile/
memory/user/facts/
memory/project/
```

---

## API de tools del LLM

El LLM tiene acceso a `memory-operations` con las siguientes operaciones:

| Operación | Descripción |
|---|---|
| `view` | Leer un archivo o listar un directorio |
| `create` | Crear nuevo archivo (falla si existe) |
| `str_replace` | Reemplazar texto exacto (oldStr debe aparecer exactamente una vez) |
| `delete` | Eliminar archivo o directorio |
| `rename` | Mover archivo dentro del mismo scope |
| `list` | Listar todos los paths bajo un directorio |
| `search` | Buscar por query (BM25, devuelve resultados rankeados) |

El LLM debe buscar antes de crear para evitar duplicados.
