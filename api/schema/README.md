# Pinned OpenAPI validation schema

`openapi-3.1-2022-10-07.json` is the unmodified OpenAPI Initiative schema from
https://spec.openapis.org/oas/3.1/schema/2022-10-07 . SHA-256:
`da01ba28852cac0de53893797cb8d1942bc3b05084f526dcc216717dec314ed0`.

The checker validates offline and rejects external document references. It also
checks embedded JSON Schemas and validates captured real handler responses.
The management API paths are checked against actual Go registrations separately.
Gateway descriptions under `x-local-gateway` describe a different host process;
the management-route coverage check does not pretend to execute those APIs.

Upstream attribution: OpenAPI Initiative / The Linux Foundation. The upstream
Apache-2.0 license is retained in `LICENSE.OpenAPI`; source:
https://github.com/OAI/OpenAPI-Specification/blob/main/LICENSE .
