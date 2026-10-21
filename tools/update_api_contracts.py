#!/usr/bin/env python3
"""Regenerate reviewed management contracts from Go JSON DTOs, offline.

The manifest owns operation semantics, queries and required request fields.
The Go AST owns JSON field names/types/omitempty; no zero-value examples are
used to infer a schema. Unsupported types fail rather than becoming {}.
"""
import argparse
import copy
import json
import os
from pathlib import Path
import re
import subprocess
from ci_inputs import input_roots, read_ci_text

ROOT = Path(__file__).resolve().parent.parent
JSON_VALUE = {"type": ["object", "array", "string", "number", "boolean", "null"],
              "description": "Application-defined JSON value (Go json.RawMessage/any)."}


def nullable(schema):
    return {"anyOf": [schema, {"type": "null"}]}


class Schemas:
    def __init__(self, types):
        self.types, self.out = types, {}

    def named(self, name, mode, args=()):
        if "." not in name:
            name = "memory." + name
        key = "Wire" + mode + name.replace("memory.", "").replace(".", "_")
        if args:
            key += "_" + "_".join(a["name"] for a in args)
        if key not in self.out:
            dto = self.types[name]  # missing types must fail
            env = dict(zip(dto["params"], args, strict=True))
            self.out[key] = {}  # recursive reference; replaced before output
            shape = self.schema(dto["type"], mode, env)
            if name == "memory.Scope" and mode == "Request":
                shape["required"] = ["workspace_id", "project_id"]
                for field in ["workspace_id", "project_id", "user_id", "agent_id"]:
                    shape["properties"][field]["maxLength"] = 255
                for field in ["workspace_id", "project_id"]:
                    shape["properties"][field]["minLength"] = 1
            shape["x-go-type"] = name
            shape["x-go-source"] = dto["source"]
            if dto["comment"]:
                shape["description"] = dto["comment"]
            self.out[key] = shape
        return {"$ref": "#/components/schemas/" + key}

    def schema(self, node, mode, env=None, omit=False):
        env = env or {}
        kind = node["kind"]
        if kind == "name":
            name = node["name"]
            if name in env:
                return self.schema(env[name], mode)
            if name in ("any", "json.RawMessage"):
                return copy.deepcopy(JSON_VALUE)
            if name == "time.Time":
                return {"type": "string", "format": "date-time"}
            if name == "string":
                return {"type": "string"}
            if name == "bool":
                return {"type": "boolean"}
            if re.fullmatch(r"u?int(8|16|32|64)?|byte|rune", name):
                return {"type": "integer"}
            if name in ("float32", "float64", "json.Number"):
                return {"type": "number"}
            return self.named(name, mode)
        if kind == "generic":
            return self.named(node["name"], mode, tuple(node["args"]))
        if kind == "pointer":
            result = self.schema(node["elem"], mode, env)
            return result if omit and mode == "Response" else nullable(result)
        if kind == "array":
            if node["elem"] == {"kind": "name", "name": "byte"}:
                result = {"type": "string", "contentEncoding": "base64"}
            else:
                result = {"type": "array", "items": self.schema(node["elem"], mode, env)}
            return nullable(result) if node["slice"] and not omit else result
        if kind == "map":
            if node["key"] != {"kind": "name", "name": "string"}:
                raise ValueError(f"unsupported JSON map key: {node}")
            result = {"type": "object", "additionalProperties": self.schema(node["elem"], mode, env)}
            return result if omit else nullable(result)
        if kind == "interface":
            return copy.deepcopy(JSON_VALUE)
        if kind == "struct":
            return self.object(node["fields"], mode, env)
        raise ValueError(f"unsupported DTO syntax: {node}")

    def object(self, fields, mode, env):
        props, required = {}, []
        for field in fields:
            if field.get("embedded"):
                ref = self.schema(field["type"], mode, env)
                base = self.out[ref["$ref"].rsplit("/", 1)[1]]
                props.update(copy.deepcopy(base["properties"]))
                required.extend(base.get("required", []))
                continue
            name, optional = field["name"], field["optional"]
            prop = self.schema(field["type"], mode, env, omit=optional)
            if field["comment"]:
                prop["description"] = field["comment"]
            props[name] = prop
            if mode == "Response" and not optional:
                required.append(name)
        result = {"type": "object", "properties": props, "additionalProperties": False}
        if required:
            result["required"] = required
        return result


def envelope(properties, required=None):
    return {"type": "object", "properties": properties, "required": required or list(properties), "additionalProperties": False}


def wrappers(s):
    ref = lambda n: s.named(n, "Response")
    arr = lambda n: {"type": "array", "items": ref(n)}
    authority = {"type": "string", "const": "data_only_untrusted"}
    return {
        "CodeGraphReposEnvelope": envelope({"repos": nullable(arr("CodeRepo")), "authority": authority}),
        "ArchivedGalleryEnvelope": envelope({"items": arr("Record")}),
        "WikiGraphEnvelope": envelope({"links": nullable(arr("WikiLink")), "authority": authority}),
        "MessengerDestinationsEnvelope": envelope({"destinations": nullable(arr("notifycfg.Dest"))}),
        "MessengerResult": envelope({"ok": {"type": "boolean"}, "error": {"type": "string"}}, ["ok"]),
        "DeletedEnvelope": envelope({"deleted": {"type": "boolean"}}),
        "TwinClaimsPage": envelope({"items": nullable(arr("TwinClaim")), "authority": authority}),
    }


def build(doc, manifest, types):
    s = Schemas(types)
    extra = wrappers(s)
    for operation in manifest["operations"]:
        method, path = operation["method"], operation["path"]
        params = []
        if method == "get":
            params.extend({"in": "query", "name": n, "required": n in ("workspace_id", "project_id") or operation["path"].startswith("/v1/knowledge/"), "schema": {"type": "string"}} for n in ("workspace_id", "project_id", "user_id", "agent_id"))
        params.extend({"in": "path", "name": n, "required": True, "schema": {"type": "string"}} for n in re.findall(r"\{([^}]+)\}", path))
        params.extend({"in": "query", "name": n, "required": n in operation.get("required_query", []), "schema": v} for n, v in operation.get("query", {}).items())
        result = {"$ref": "#/components/schemas/" + operation["response"]} if operation["response"] in extra else s.named(operation["response"], "Response")
        responses = {}
        for code in [200, 400, 401, 403, 404, 409, 413, 429, 503]:
            responses[str(code)] = {"description": "Success" if code == 200 else "Request, authorization, concurrency, size, budget or dependency error; see Error.code.", "headers": {"X-Request-ID": {"$ref": "#/components/headers/RequestID"}}, "content": {"application/json": {"schema": result if code == 200 else {"$ref": "#/components/schemas/Error"}}}}
        op = {"operationId": method + "_" + re.sub(r"\W+", "_", path).strip("_"), "tags": ["Management"], "summary": operation["summary"], "description": operation.get("description", ""), "parameters": params, "responses": responses, "x-go-contract": True}
        if operation["request"]:
            request = s.named(operation["request"], "Request")
            if operation.get("required_body"):
                request = {"allOf": [request, {"required": operation["required_body"]}]}
            if operation.get("body_properties"):
                request = {"allOf": [request, {"properties": operation["body_properties"]}]}
            op["requestBody"] = {"required": True, "content": {"application/json": {"schema": request}}}
        doc["paths"].setdefault(path, {})[method] = op
    schemas = doc["components"]["schemas"]
    for name in list(schemas):
        if name.startswith("Wire"):
            del schemas[name]
    schemas.update(s.out)
    schemas.update(extra)
    codes = schemas["Error"]["properties"]["code"]["enum"]
    if "authentication_required" not in codes:
        codes.append("authentication_required")
    # The shared registrar enforces this before decoding every protected route.
    # Keep legacy operations consistent with the stricter management corpus.
    for path in doc["paths"].values():
        for method, operation in path.items():
            if method not in ("get", "post", "put", "patch", "delete", "head", "options") or operation.get("security") == []:
                continue
            operation["responses"]["401"] = {
                "description": "Missing bearer credentials.",
                "headers": {"WWW-Authenticate": {"schema": {"type": "string", "const": 'Bearer realm="arkism"'}}, "X-Request-ID": {"$ref": "#/components/headers/RequestID"}},
                "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}},
            }
    return doc


def patch_json(raw, old, new, depth=0):
    """Keep the existing hand-formatted JSON untouched outside changed values."""
    if old == new:
        return raw
    if not isinstance(old, dict) or not isinstance(new, dict) or not old.keys() <= new.keys():
        return json.dumps(new, indent=2, ensure_ascii=False).replace("\n", "\n" + "  " * depth)
    decoder = json.JSONDecoder()
    cursor, edits = raw.index("{") + 1, []
    for key in old:
        while raw[cursor].isspace() or raw[cursor] == ",":
            cursor += 1
        found, end = decoder.raw_decode(raw, cursor)
        if found != key:
            raise ValueError("JSON member order mismatch")
        cursor = end
        while raw[cursor].isspace() or raw[cursor] == ":":
            cursor += 1
        value, end = decoder.raw_decode(raw, cursor)
        edits.append((cursor, end, patch_json(raw[cursor:end], value, new[key], depth + 1)))
        cursor = end
    added = [key for key in new if key not in old]
    if added:
        chunks = [json.dumps(key) + ": " + json.dumps(new[key], indent=2, ensure_ascii=False).replace("\n", "\n" + "  " * (depth + 1)) for key in added]
        text = ("," if old else "") + "\n" + "  " * (depth + 1) + (",\n" + "  " * (depth + 1)).join(chunks)
        edits.append((cursor, cursor, text))
    for start, end, value in sorted(edits, reverse=True):
        raw = raw[:start] + value + raw[end:]
    return raw


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--types", type=Path, help="Use previously exported Go AST metadata without compiling a tool")
    args = parser.parse_args()
    types = json.loads(read_ci_text(args.types, input_roots(ROOT / "api"))) if args.types else json.loads(subprocess.check_output(["go", "run", "./tools/apitypes"], cwd=ROOT, env={**os.environ, "GOTOOLCHAIN": "local", "GOPROXY": "off"}))
    dest = ROOT / "api/openapi.yaml"
    original = json.loads(dest.read_text(encoding="utf-8"))
    manifest = json.loads((ROOT / "api/management-contracts.json").read_text(encoding="utf-8"))
    generated = build(copy.deepcopy(original), manifest, types)
    if args.check:
        if original != generated:
            raise SystemExit("Go DTO / OpenAPI drift: run python3 tools/update_api_contracts.py, then review the contract diff")
        print(f"Management contracts: {len(manifest['operations'])} operations and Go DTO fields match")
    else:
        dest.write_text(patch_json(dest.read_text(encoding="utf-8"), original, generated), encoding="utf-8")


if __name__ == "__main__":
    main()
