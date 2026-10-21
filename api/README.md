# Management HTTP contracts

`openapi.yaml` is JSON-compatible OpenAPI 3.1. It describes 271 production
registrations. The root paths are checked against actual Go route registrations
in both directions; there is no undocumented-route exception list.

The 60 formerly missing operations are maintained in `management-contracts.json`.
That manifest owns operation semantics, query parameters and required input
fields. `tools/update_api_contracts.py` extracts the referenced Go JSON DTOs
through the standard-library AST tool under `tools/apitypes`. The `WireRequest*`
and `WireResponse*` schemas preserve JSON tags, types, pointer/slice/map
nullability and omitempty. Open-ended JSON is explicit only for actual
`json.RawMessage` / `any` values and map values; unknown types fail generation.
The record envelope is documented as a record, not its nested payload alone.

```bash
python tools/update_api_contracts.py --check
python tools/check_openapi.py
```

Changes to a DTO require regeneration and review of the API diff. A valid schema
is not runtime evidence: the Windows handler test exports successful, anonymous
and unknown-field cases through `ARKISM_OPENAPI_CASES`; the validator checks
response bodies, successful request bodies, query parameters and complete
60-operation success/denial coverage. See [development](../guide/development.md)
for the exact commands. Storage/model/notification providers are fixtures;
these cases do not prove live PostgreSQL or external-provider behavior.

The `x-local-gateway` extension describes another process. It is not a substitute
for a root management path, nor counted as management-route coverage.
