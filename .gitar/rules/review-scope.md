---
title: "Review scope"
description: "Gitar is the only AI reviewer: review logic, design and intent, and leave scanner work to the scanners"
type: check
when: "A pull request is opened or new commits are pushed"
actions: "Post inline comments on logic, design and intent problems only"
---

# Review scope

Gitar is the only AI reviewer on this repository. Review what a rule-based tool cannot see:

- Logic errors, missed edge cases, wrong error handling, data loss
- Design: a change that breaks the existing layers, duplicates a helper that already exists, or adds code nobody asked for
- Intent: the code does not do what the PR title and body say, or the docs in the same PR disagree with the code

Leave these to their own tools and do not comment on them:

- Security and secrets: CodeQL, GitHub secret scanning, Dependabot
- Lint and rule-based quality: SonarCloud, golangci-lint, ESLint, clippy
- Coverage: the coverage job

Skip these files: `**/Cargo.lock`, `**/go.sum`, `desktop/ui/fonts/**`, `desktop/src-tauri/icons/**`.

Keep comments few and concrete: say what breaks, for which input, and how to fix it. No praise, summaries of the diff, or style nits.

After five fix commits from a bot on the same pull request, stop starting new review rounds and leave the rest to a human, so automated fixes cannot loop.
