---
title: "Repository writing rules"
description: "Catch writing rules that CI does not check"
type: check
when: "A pull request is opened, its title or body changes, or new commits are pushed"
actions: "Post one comment listing each violation with the file, line or commit it is in"
---

# Repository writing rules

Check the commits, the PR title and body, and every changed text file (code, comments, docs) for:

1. Names of outside products, companies or papers used as reference or comparison. Names of dependencies the code actually imports, file formats and protocols are fine.
2. AI co-author or attribution lines in commit messages, such as `Co-Authored-By:` naming an AI model, or "Generated with" lines in the PR body.
3. A table or list of CI or test run times in the PR body.

Report each violation once. If there are none, post nothing.
