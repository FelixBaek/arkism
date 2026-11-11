# Independent lexical evidence / 독립 검색 근거 계수

Related work: PR #552.

The relevance gate counted a word with a Korean particle and its stem as two
pieces of evidence, even though the query builder placed both in one concept.
For the query `도서관마다 운영 안내`, a record containing only `도서관마다 도서관`
and tagged `도서관마다` passed the exact-keyword lane's relevance gate. It had one
subject, but the counter reported two of three concepts. Adding `운영` incorrectly
raised that count to three rather than two.

A read-only diagnostic against real PostgreSQL reproduced this behavior both
before and after the separate standalone-OR correction. The counter bug was an
existing problem, not introduced by restoring Korean particle alternatives.
The committed regression then failed on the original counter: both count
assertions failed, and the actual service returned the unsupported record
alongside the genuinely supported one.

`conceptsFound` now uses the query builder's concept heads for both the total and
the matching evidence. Each term belongs to its first matching head, and each
group counts at most once. A shared short stem cannot support two different
compound heads. Matching spellings, document normalization, query thresholds,
fixture expectations and English-prefix boundaries remain unchanged.

The new regression covers the particle/stem pair, two independent subjects,
English plural alternatives, distinct English prefixes, a shared Korean stem,
and actual PostgreSQL-backed `Service.Search` through the exact and FTS lanes.
The independently supported record must remain while the repeated subject is
excluded. Independent review found no blocking issue in the change.

같은 단어의 조사 포함형과 어근을 두 근거로 세던 문제를 실제 PostgreSQL
검색으로 재현했다. 쿼리를 구성한 개념 묶음에 맞춰 한 번씩만 세도록 고쳤으며,
실제로 다른 근거 두 개를 가진 결과를 유지하는 회귀 검사도 추가했다.

## Verification / 검증

The final scoped race run passed 21 top-level tests and 32 subtests (6.742s),
with zero failures or skips. It includes the new unit and actual PostgreSQL
service regressions, existing no-answer and held-out relevance checks, keyword
coverage, number forms, compound evidence, and Korean recall. Korean memory
queries passed 10/10 ordinary and 10/10 rare-particle cases; skill queries passed
8/8 ordinary and 6/6 rare-particle cases. All six unrelated queries returned
nothing. The four paraphrase cases remain 0/4 and are not counted as successes.

gocognit v1.2.1 found no function above 20 in the changed production and test
files. `git diff --check` passed. The test run used cached Go 1.27.4 with
`GOTOOLCHAIN=local`, `GOPROXY=off`, `GOMAXPROCS=2` and an explicitly disposable
loopback PostgreSQL parent; each database test creates and removes its own child
database. No downloads were needed.

```sh
go test -race ./internal/memory -run '^(TestConceptEvidence.*|TestPostgresConceptEvidence.*|TestRelevanceGate.*|TestPostgresRelevanceGateEval|TestExpandedKeywordCoverage.*|TestSearchQuery.*|TestSearchConcept.*|TestQueryOffersNumberFormsAndCompoundSplits|TestPostgresLexicalAliasesRemainOneConcept|TestPostgresLongLexicalQueryNeedsDistinctEvidence|TestPostgresKoreanRecallQuality|TestPostgresParticleStemsPreserveCompoundEvidence)$' -count=1 -timeout=240s -json
```

## Limits / 한계

This is a lexical evidence-count correction, not a semantic-quality or whole
roadmap completion claim. Existing compound matching and supported spelling
rules are retained. The separate Korean recall fixture still treats four
paraphrase questions as unsupported by the lexical-only path. No product build,
external model call or production deployment is part of this verification.

## Windows test portability follow-up

A later cross-platform check and Windows CI found that the new PostgreSQL test
used an ID-list helper defined only in a Linux test file. The assertion now
checks the same one-result length and ID directly, without that helper or the
unused slices import. Production logic and the PostgreSQL fixture are unchanged.
`GOOS=windows go vet ./internal/memory` exited zero, and the concept-count unit
regression passed with race (1.183s, one top-level test and four subtests).
This is not local execution of Windows or a repeated PostgreSQL run; CI runs
those paths again. The previous evaluation-job download failure was an external
connection reset before schema checking, and the superseding commit starts a
new CI attempt without changing dependency or validation rules.
