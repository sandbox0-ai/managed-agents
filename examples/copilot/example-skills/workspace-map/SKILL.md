---
name: workspace-map
description: Inspect a repository or workspace and produce a concise map of important files, entry points, and likely next places to edit.
---

# Workspace Map

Use this skill when the user asks where code lives, how a project is organized, or what files are relevant before making a change.

## Process

1. List the top-level files and directories.
2. Identify package manifests, build files, generated directories, and test directories.
3. Read only the smallest set of files needed to understand the requested area.
4. Answer with concrete paths and a short explanation of why each path matters.

Avoid broad summaries when the user needs a specific next file to open or edit.
