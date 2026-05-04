---
name: writing
version: 1.1.0
description: Professional writing and document creation. BLUF-first structure, prose over bullets for arguments, clear hierarchy. Use for memos, reports, articles, and long-form content.
category: documents
triggers:
  - writing
  - document
  - rewrite
  - report
  - draft
  - prose
  - article
  - memo
  - redactar
  - documento
  - informe
  - artículo
author: obvious-team
created: 2026-03-15
updated: 2026-05-04
---

# Writing Skill

## When to use

Load this skill when:
- User asks to write, draft, rewrite, or edit a document
- The output is long-form prose (memo, report, article, proposal)
- The writing needs to be professional and structured

## Core principles

**1. BLUF first.** Every document starts with the Bottom Line Up Front — the answer, recommendation, or key finding. Supporting detail follows.

**2. Prose for arguments, bullets for reference.** Build arguments in paragraphs. Use lists for steps, options, or items the reader will scan back to.

**3. One idea per paragraph.** If a paragraph exceeds 5 sentences, split it.

**4. Active voice.** "The system validates tokens" not "tokens are validated by the system."

**5. No filler.** Cut: "in order to", "it should be noted that", "as previously mentioned", "needless to say."

## Document structure

```
[BLUF — answer or recommendation in 1-2 sentences]

## Background / Context
[Why this document exists]

## [Main sections — H2 for major, H3 for sub]

## Conclusion / Next steps
[What the reader should do or decide]
```

## Quality bar

- [ ] First paragraph contains the BLUF
- [ ] No heading is immediately followed by another heading
- [ ] Consistent tense throughout
- [ ] Acronyms defined on first use
- [ ] No orphan bullets (single-item lists → prose instead)

## Using document-operations

To save the document as a file, use the `document-operations` tool with `action: write` and the relative file path. Use `.md` for markdown, `.txt` for plain text.
