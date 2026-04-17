---
name: regression-check
description: Choose and run focused checks after a code change, then summarize what passed, what failed, and any remaining risk.
---

# Regression Check

Use this skill after editing code or when the user asks whether a change is safe.

## Process

1. Prefer the narrowest relevant unit test, typecheck, or lint command.
2. If no focused test exists, run the smallest available project check and explain the limitation.
3. Include exact commands and high-signal output.
4. If a check fails, report the failing command, the likely cause, and the next fix.

Do not claim the change is verified unless at least one relevant check completed successfully.
