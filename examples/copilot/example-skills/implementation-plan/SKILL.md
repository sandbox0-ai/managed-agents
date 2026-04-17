---
name: implementation-plan
description: Turn a requested code change into a small implementation plan with files to inspect, files to edit, and focused verification steps.
---

# Implementation Plan

Use this skill before changing code when the request spans more than one file, affects behavior, or needs verification.

## Process

1. Restate the concrete behavior to implement.
2. Identify existing code paths and tests that should guide the change.
3. Propose a short edit sequence.
4. Name the verification commands to run after editing.

Keep the plan short. Do not invent architecture that is not already present in the repository.
