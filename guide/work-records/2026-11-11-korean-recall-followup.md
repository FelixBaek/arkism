# Korean recall regression follow-up / 한국어 조사 검색 회귀 수정

Related work: PR #552, coverage job 130416740855, following the PR #551
review correction integrated during completion.

The integration owner's earlier correction correctly stopped unrelated English
prefixes such as `java` and `javascript` from becoming one search concept. Its
accompanying alias deduplication change introduced a separate regression: it
searched the rendered tsquery for a quoted word without distinguishing a
standalone OR alternative from a word inside a compound AND branch. For example,
`도서관마다` already contains a generated `('도서관' & '마다')` branch. Finding
`'도서관'` there incorrectly suppressed the independent particle-stripped
`'도서관'` alternative. The complete word was then required even though the
normalizer had correctly supplied the stem.

The repair tracks independent alternatives as structured strings per concept.
Compound components no longer suppress an independent stem. The existing
English prefix separation, known plural aliases, Korean particle list,
compound evidence requirement and candidate thresholds are unchanged. This is
a query-expression repair; stored token normalization does not change and no
index version bump or backfill is required. The quality fixture and all its
expectations remain unchanged; no answer-specific aliases were added.

A new disposable PostgreSQL 18 instance reproduced all six CI misses before the
repair: rare-particle memory recall was 7/10 and skill recall was 3/6. Ordinary
particle queries were already 10/10 and 8/8 respectively; the six unrelated
requests returned no results. A separate pre-fix unit regression also failed
for five words outside the quality fixture: 선생님께서, 동료한테서, 담당자에게서,
자료집이랑 and 도서관마다. The new SQL regression checks that 도서관마다 can
match 도서관 while unrelated 도서 cannot, and that compound parts still supply
only one concept toward a longer query's evidence threshold.

This lexical-only fix does not implement semantic paraphrase recall. Four
paraphrase requests in the existing fixture are reported observations rather
than required lexical expectations. It also does not widen the stored rare
particle normalizer, guarantee arbitrary Korean morphology, or establish a
production-wide recall percentage. No live model, external provider or product
build is involved.

After the repair, the unchanged real PostgreSQL fixture recovered every missing
case: rare-particle memory recall reached 10/10 and skill recall 6/6; ordinary
particle recall remained 10/10 and 8/8. All six negative queries still returned
nothing. Semantic paraphrase recall remained 0/4 and is not counted as a passed
quality requirement.

Focused race checks passed 19 top-level tests and 28 subtests across three
commands, with zero failures or skips. The main selection passed 17 top-level
and 28 subtests (3.082s), including six-word recovery, independent fixture-free
particle/compound evidence, alias counting, unrelated English prefixes, token
normalization, query bounds and procedural-topic precision. Two additional
existing checks passed: number-form/compound aliases (1.189s) and real
PostgreSQL long-query distinct-evidence enforcement (1.300s). The latter also
covers unbackfilled rows. These are targeted regression checks, not the whole
suite. `git diff --check` passed and the existing Korean fixture/test files have
no changes.

```sh
go test -race ./internal/memory -run '^(TestPostgres(KoreanRecallQuality|ParticleStemsPreserveCompoundEvidence|SearchKoreanParticlesIntegration|Lexical.*)|TestSearch(Tokens.*|Query.*|Concept.*))$' -count=1 -timeout=240s -json
go test -race ./internal/memory -run '^TestQueryOffersNumberFormsAndCompoundSplits$' -count=1 -timeout=120s -json
go test -race ./internal/memory -run '^TestPostgresLongLexicalQueryNeedsDistinctEvidence$' -count=1 -timeout=180s -json
```

Checks used cached Go 1.27.4, `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOMAXPROCS=2`
and disposable child databases on the dedicated loopback PostgreSQL instance.
