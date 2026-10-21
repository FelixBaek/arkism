#!/usr/bin/env python3
"""Offline OpenAPI structure, reference and real-response contract checks."""
import argparse
import hashlib
import json
from pathlib import Path
from urllib.parse import unquote
from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parent.parent
META_SHA256 = "da01ba28852cac0de53893797cb8d1942bc3b05084f526dcc216717dec314ed0"
METHODS = {"get", "put", "post", "delete", "options", "head", "patch", "trace"}


def read_json(path):
    def unique(pairs):
        out = {}
        for key, value in pairs:
            if key in out:
                raise ValueError(f"duplicate JSON key: {key}")
            out[key] = value
        return out
    return json.loads(Path(path).read_text(encoding="utf-8-sig"), object_pairs_hook=unique)


def pointer(doc, ref):
    if not ref.startswith("#/"):
        raise ValueError(f"external or unsupported reference: {ref}")
    item = doc
    for key in unquote(ref[2:]).split("/"):
        key = key.replace("~1", "/").replace("~0", "~")
        item = item[int(key)] if isinstance(item, list) else item[key]
    return item


def walk(item):
    yield item
    if isinstance(item, dict):
        for value in item.values():
            yield from walk(value)
    elif isinstance(item, list):
        for value in item:
            yield from walk(value)


def check_document(doc, meta):
    Draft202012Validator(meta).validate(doc)
    for item in walk(doc):
        if isinstance(item, dict):
            for name in ("$ref", "$dynamicRef"):
                if name in item:
                    pointer(doc, item[name])
    for name, schema in doc.get("components", {}).get("schemas", {}).items():
        Draft202012Validator.check_schema(schema)
    operations = 0
    for path, path_item in doc["paths"].items():
        for method, operation in path_item.items():
            if method not in METHODS:
                continue
            operations += 1
            parameters = path_item.get("parameters", []) + operation.get("parameters", [])
            resolved = [pointer(doc, p["$ref"]) if "$ref" in p else p for p in parameters]
            identities = [(p["in"], p["name"]) for p in resolved]
            if len(identities) != len(set(identities)):
                raise ValueError(f"duplicate parameter: {method} {path}")
            import re
            expected = set(re.findall(r"\{([^}]+)\}", path))
            actual = {p["name"] for p in resolved if p["in"] == "path" and p.get("required")}
            if expected != actual:
                raise ValueError(f"path parameter mismatch: {method} {path}: {expected} != {actual}")
            for item in walk(operation):
                if isinstance(item, dict) and "schema" in item:
                    Draft202012Validator.check_schema(item["schema"])
    return operations


def validate_value(doc, schema, value):
    bound = {**doc, **schema}
    Draft202012Validator(bound, format_checker=FormatChecker()).validate(value)


def check_cases(doc, cases, require_management=False):
    successful, anonymous = set(), set()
    for case in cases:
        method, path = case["operation"].split(" ", 1)
        operation = doc["paths"][path][method.lower()]
        response = operation["responses"][str(case["status"]) ]
        if "$ref" in response:
            response = pointer(doc, response["$ref"])
        schema = response["content"]["application/json"]["schema"]
        validate_value(doc, schema, case["body"])
        if case["status"] == 403:
            anonymous.add(case["operation"])
        if case["status"] == 200:
            successful.add(case["operation"])
            if "request" in case:
                request = operation["requestBody"]["content"]["application/json"]["schema"]
                validate_value(doc, request, case["request"])
            if "query" in case:
                check_query(doc, operation, case["query"])
    if require_management:
        expected = {method.upper() + " " + path for path, item in doc["paths"].items() for method, op in item.items() if isinstance(op, dict) and op.get("x-go-contract")}
        if expected - successful or expected - anonymous:
            raise ValueError(f"incomplete real management coverage: successful={sorted(expected-successful)}, anonymous={sorted(expected-anonymous)}")
    return len(cases)


def check_query(doc, operation, query):
    for param in operation.get("parameters", []):
        if "$ref" in param:
            param = pointer(doc, param["$ref"])
        if param["in"] != "query":
            continue
        values = query.get(param["name"], [])
        if not values:
            if param.get("required"):
                raise ValueError(f"missing actual query parameter: {param['name']}")
            continue
        if len(values) != 1:
            raise ValueError(f"duplicate actual query parameter: {param['name']}")
        value = int(values[0]) if param["schema"].get("type") == "integer" else values[0]
        validate_value(doc, param["schema"], value)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--spec", type=Path, default=ROOT / "api/openapi.yaml")
    parser.add_argument("--cases", type=Path)
    args = parser.parse_args()
    meta_path = ROOT / "api/schema/openapi-3.1-2022-10-07.json"
    if hashlib.sha256(meta_path.read_bytes()).hexdigest() != META_SHA256:
        raise ValueError("pinned official OpenAPI schema changed")
    doc = read_json(args.spec)
    count = check_document(doc, read_json(meta_path))
    cases = check_cases(doc, read_json(args.cases), require_management=True) if args.cases else 0
    print(f"OpenAPI: {count} operations, references and schemas valid; {cases} real HTTP responses and successful request bodies validated")


if __name__ == "__main__":
    main()
