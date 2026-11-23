---
paths:
  - "prompts/**"
---

# Prompt conventions

- Prompts are versioned Markdown files loaded by the Python worker from `prompts/`.
- Keep system prompts explicit about agent identity, allowed tools, JSON output contract, and forbidden actions (never external URLs, never secrets).
- Never embed dynamic runtime tokens or local absolute paths in static prompt files.
