# Independent skill reuse and authorization review

Baseline: public v0.1.0, `9379ee5f493716e1c80027c0beb4fd12283100cf`.
The production change in this review is **after** that baseline. No main merge or deployment performed.

## Executed evidence

- `TestCrossAgentSkillHTTPLifecycle`: a real loopback TCP HTTP server runs the release handler with an isolated in-memory repository and token-keyed authorization fixture. A proposes a synthetic skill through `/v1/skills/propose`. B has a distinct bearer token and principal. B cannot retrieve it through `@latest-approved` before approval. Neither A nor B can approve (403). A synthetic human-role principal approves, another synthetic human-role principal supplies the comparison evaluation, and the first applies it. B retrieves matching instructions and source provenance. Other project/workspace/user/agent scopes do not return the body. Deprecation removes latest-approved availability.
- This is **API transport validation**, not a live Claude/Codex execution or a live person reviewing a proposal. Preparatory learning records use existing service fixtures; comparison scores are synthetic validation inputs, not measured model results. Returned instructions were checked by deterministic assertions, not followed by a model.
- `skills-baseline.log`: lifecycle passed on the baseline; three new resource-policy denial cases failed with HTTP 200 and the synthetic body.
- `skills-regression.jsonl`: new lifecycle/denial/identity regression tests pass with race detection after the fix.
- `skills-fixed.jsonl`: related skill proposal, version, availability, automation and trial tests pass after the fix, with race detection.
- `host-skills.jsonl`: the complete host skills package passes with race detection. These are adapter/component tests with fixture hubs and local temporary directories, not real provider sessions.
- `skill-results.json` and `.csv`: per-test outcomes and observed duration. Missing model token/cost and human review values are null; no fabricated savings.

## Confirmed defect and minimal fix

`ResolveSkills` checked only scope-level `read:skill_versions`. A policy denying a particular `skills`, `skill_versions`, or `sources` resource was enforced by generic `Service.Get` but bypassed by `/v1/skills/resolve`, returning the same protected content. All three cases were reproduced on the baseline in `TestSkillResolveHTTPHonorsResourceReadDenial`.

The fix checks each successfully resolved parent, version and published source with the resource-specific read action, and verifies the principal and role remain the same as the initial decision. `TestSkillResolveRejectsChangedReaderIdentity` also rejects principal/role changes. Scope-level production Postgres grants may not currently express this fine-grained policy; the confirmed flaw is a service authorization contract violation exercised through the real HTTP handler with an authorizer enforcing such denials. It is not evidence of an observed production account leak.

## Contract limits, not silently relabeled as defects

1. `internal/memory/skill_hub.go:resolveSkillRef` explicitly allows candidate and superseded exact pins for review/replay. `id@1` returns candidate instructions to ordinary read-authorized agent B, marked `validation_status=candidate`. Deprecation blocks `latest-approved` but exact pins still replay. These behaviors are tested by the new HTTP lifecycle and existing `TestSkillProposalErasure`, `TestSkillAvailabilityDeprecatesAndLimitsRuntimes`. Therefore **all unapproved or retired skills are invisible to other agents** is false; the supported narrower claim is default approved lookup/routing. No policy overhaul was made.
2. `internal/memory/skill_automation.go` and `TestSkillAutomationTrialPromotion` permit bounded service-principal automatic approval/promotion when explicit deployment policy and independent evidence satisfy the checks. Agent/pipeline role promotion is denied. The release is not an unconditional human-only skill approval system. This review did not enable any production policy.
3. `TestLoadSkillKeepsTheSessionPin` and `TestLoadSkillServesTheSessionStartVersion` show an existing session intentionally retains its starting version after a new approval; a new session gets the new version. This is not a guarantee that old instructions disappear from a live chat.
4. `router_bundle_test.go` verifies credential/hub-separated offline caches and online-unavailability revocation. Offline caches cannot learn a new server-side revocation while disconnected; no universal immediate offline deletion guarantee is established.
5. Existing host connection code supports native MCP settings for Claude/Codex and additional agents, but configuration support and fixture adapter tests do not establish successful live bidirectional transfer.

## Reproduce

From a clean checkout containing this review:

```sh
go test -race ./internal/memory -run '^Test(CrossAgentSkillHTTP|SkillResolve)' -count=1 -json
go test -race ./internal/memory -run 'TestCrossAgentSkillHTTP|TestSkillResolveHTTPHonors|TestSkillVersions|TestSkillAvailability|TestSkillProposal|TestSkillAutomation|TestSkillTrial' -count=1 -json
go test -race ./internal/host/skills -count=1 -json
```

To reproduce the baseline failure, create another isolated checkout at the baseline commit and copy only `internal/memory/cross_agent_skill_http_test.go` from this review (not `skill_hub.go`). Run `go test ./internal/memory -run 'TestCrossAgentSkillHTTP|TestSkillResolveHTTPHonors' -count=1 -v`. Expect lifecycle pass and three denial cases fail (200 instead of 403). All records and tokens in these tests are synthetic; no external server is contacted by the new tests.

## Remaining proof

Real human review, a fresh B actually carrying out the retrieved procedure, and bidirectional fresh Claude/Codex sessions remain separate evidence requirements. The deterministic tests cannot quantify task-quality or cost improvement. The immediate priority is integrating the authorization fix and its regressions, then independently measuring those live-agent outcomes with maintained static-document controls.
