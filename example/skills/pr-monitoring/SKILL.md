---
name: pr-monitoring
version: 1.0.0
description: Monitor pull requests, CI pipelines, and code review status. Check CI before transitioning executables to in_review. Ensure PRs meet merge criteria.
category: autobuild
triggers:
  - pr
  - pull request
  - ci
  - review
  - pipeline
  - merge check
  - ci status
grantedTools:
  - gh-pr-check
  - gh-ci-status
  - gh-review-summary
author: obvious-team
created: 2026-03-20
---

# PR Monitoring Skill

## When to Use

- An executable has produced a pull request.
- CI status needs to be checked before advancing an executable to `in_review`.
- Review comments need to be addressed or triaged.
- The orchestrator is deciding whether to merge or request changes.

## Non-Negotiables

1. **Always check CI before transitioning to `in_review`.** A PR with failing CI cannot be reviewed.
2. **Read all review comments.** Do not skip inline comments or suggestion threads.
3. **Re-run flaky tests once.** If CI fails on a test unrelated to the PR's changes, re-run once. If it fails again, report it.
4. **Never force-merge.** If required checks are failing, fix them — do not bypass branch protection.
5. **Summarize PR state.** When reporting to the orchestrator, include: CI status, reviewer count, open threads, blocking comments.

## Workflow

```
PR Created
  → Check CI status (gh-ci-status)
  → If failing → fix or report
  → If passing → request review
  → Monitor review comments (gh-review-summary)
  → Address blocking comments
  → Re-check CI after fixes
  → If all green + approved → merge
```

## Quality Bar

- [ ] CI is green before any merge attempt
- [ ] All reviewer threads are resolved or responded to
- [ ] PR description matches the executable's planned commits
- [ ] No unrelated changes are included in the diff
