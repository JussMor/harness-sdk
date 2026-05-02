---
name: execution-planning
version: 1.0.0
description: Turn an approved spec into an execution plan with features, executables, planned commits, dependency DAGs, sequencing, parallelization, and visibility for implementation.
category: autobuild
triggers:
  - execution planning
  - autobuild plan
  - implementation plan
  - dependency dag
  - planned commits
  - sequencing
  - parallelization
  - break down the work
  - how we will execute
author: obvious-team
created: 2026-04-06
---

# Execution Planning Skill

## When to Use

- The orchestrator needs to convert an approved spec into a plan.
- Work has multiple executables, dependencies, or PRs.
- The user asks how work will be sequenced, parallelized, or tracked.
- A plan needs to show commit lists, dependency DAGs, or what can run in parallel.

## Non-Negotiables

1. **Consume the spec; do not rewrite it.** Planning starts from locked design decisions. Reopen the spec only if execution reveals a material contradiction.
2. **Make dependencies explicit.** Every executable must list blockers and downstream dependents.
3. **Show the DAG.** Provide a text DAG or table that makes sequencing reviewable.
4. **List planned commits.** Each executable should include a commit sequence so reviewers know the intended diff boundaries.
5. **Identify parallel work.** State which executables can run concurrently and which must be serialized.

## Plan Template

```
## Execution Plan: <title>

### Features
| # | Feature | Executables |
|---|---------|-------------|
| 1 | <name>  | exe_01, exe_02 |

### Dependency DAG
exe_01 (schema) ──→ exe_02 (API) ──→ exe_03 (tests)
                                  ──→ exe_04 (docs)

### Parallelization
- Wave 1: exe_01 (no deps)
- Wave 2: exe_02 (blocked by exe_01)
- Wave 3: exe_03, exe_04 (parallel, both blocked by exe_02)

### Planned Commits per Executable
exe_01:
  1. Add migration file
  2. Run migrate up
  3. Seed test data
```

## Quality Bar

- [ ] Every executable has a clear owner (thread/runner)
- [ ] Dependencies form a valid DAG (no cycles)
- [ ] Parallel opportunities are identified
- [ ] Planned commits are listed per executable
- [ ] Total estimated waves match DAG depth
