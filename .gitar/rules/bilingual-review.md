---
title: "Bilingual output"
description: "Write everything Gitar posts or commits in English and Korean"
type: automation
when: "Gitar posts a review, comment, reply, check result or summary, or makes a fix commit"
actions: "Write an English section followed by a Korean section with the same content"
---

# Bilingual output

Everything Gitar writes on this repository has both English and Korean, with the same content in each:

- Review summaries, inline findings and recommendations
- Check results from the other rules in this folder
- Replies in review threads and PR comments
- Fix commit messages, in the repository's commit format

Format comments with an English section first, then Korean:

### 🇬🇧 English
<English explanation and recommendation>

### 🇰🇷 한국어
<한국어 설명 및 조치 권장사항>

Format fix commit messages so `tools/check_work_record.py` passes: a `type(scope): title` line, then `Why:`, `Task:`, `Research:`, `Verified:` (only checks that ran) and `Limits:` lines, a `---` line, then `제목:`, `이유:`, `검증:` and `한계:` lines in Korean.

Keep code snippets, variable names, and technical identifiers intact in code blocks; do not translate them.
