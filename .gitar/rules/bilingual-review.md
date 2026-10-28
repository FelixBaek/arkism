---
title: "Bilingual review output"
description: "Write every review comment in English and Korean"
type: automation
when: "Any pull request review comment, summary or recommendation"
actions: "Write the comment with an English section followed by a Korean section"
---

# Bilingual review output

Provide all review summaries, finding descriptions, and recommendations in both English and Korean (Bilingual).
Format each comment with clear English and Korean sections, for example:

### 🇬🇧 English
<English explanation and recommendation>

### 🇰🇷 한국어
<한국어 설명 및 조치 권장사항>

Keep code snippets, variable names, and technical identifiers intact in code blocks.
