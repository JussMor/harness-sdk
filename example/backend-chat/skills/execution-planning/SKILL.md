---
name: execution-planning
version: 1.1.0
description: Break down a multi-step task into an execution plan with dependencies, sequencing, and parallelization. Use when the task has 3+ executables or unclear ordering.
category: planning
triggers:
  - execution planning
  - implementation plan
  - dependency dag
  - break down the work
  - how to sequence
  - parallelization
  - plan the work
  - planificar
  - plan de ejecución
author: obvious-team
created: 2026-04-06
updated: 2026-05-04
---

# Execution Planning Skill

## When to use

Load this skill when:
- A task has 3 or more distinct steps that need ordering
- The user asks how to sequence or parallelize work
- Dependencies between steps need to be made explicit
- You need to identify what can run in parallel vs what must be serialized

## How to produce a plan

1. List all executables (concrete units of work)
2. For each executable, identify its blockers (what must complete first)
3. Draw the dependency DAG as text
4. Group into waves: Wave 1 = no deps, Wave 2 = blocked by Wave 1, etc.
5. State which executables in the same wave can run in parallel

## Plan template

```
## Plan: <title>

### Executables
| ID  | Name             | Depends on |
|-----|------------------|------------|
| E1  | <name>           | —          |
| E2  | <name>           | E1         |
| E3  | <name>           | E1         |
| E4  | <name>           | E2, E3     |

### Dependency DAG
E1 ──→ E2 ──→ E4
  └──→ E3 ──┘

### Waves (execution order)
- Wave 1: E1
- Wave 2: E2, E3 (parallel)
- Wave 3: E4
```

## Quality bar

- Every executable has a clear, concrete description
- No cycles in the DAG
- Parallel opportunities are named explicitly
- Total waves match DAG depth

## What this skill does NOT do

- It does not execute the plan — it only produces it
- It does not call any external tools
- It does not monitor CI or PRs
