"""SYS-05 offline request-plan generator. No service calls or queue simulation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import sys

CONTRACT_SHA256 = "398df795b2eb03fc421158a10ca4094cdb199f3577545f28715b3d2d8c9ea2bc"
MAX_BYTES = 64 * 1024 * 1024
MAX_I64 = (1 << 63) - 1
MIN_I64 = -(1 << 63)
MAX_ROWS = 100000


class LoadError(Exception):
    def __init__(self, code, path="$,"):
        self.code = code
        self.path = path.rstrip(",")
        super().__init__(code)


def require(condition, code, path):
    if not condition:
        raise LoadError(code, path)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=False, allow_nan=False, sort_keys=True,
                      separators=(",", ":")).encode("utf-8")


def _duplicates(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate_key", "$")
        result[key] = value
    return result


def _number(text):
    raise LoadError("invalid_number", "$")


def _integer(text):
    require(len(text) <= 20, "invalid_integer", "$")
    value = int(text)
    require(MIN_I64 <= value <= MAX_I64, "invalid_integer", "$")
    return value


def _depth(text):
    depth = 0
    quoted = escaped = False
    for char in text:
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in "[{":
            depth += 1
            require(depth <= 32, "limit_exceeded", "$")
        elif char in "]}":
            depth -= 1


def _plain(value, depth=0, count=None):
    if count is None:
        count = [0]
    require(depth <= 32, "limit_exceeded", "$")
    if isinstance(value, str):
        try:
            size = len(value.encode("utf-8"))
        except UnicodeError as exc:
            raise LoadError("invalid_unicode", "$") from exc
        require(size <= 4096, "limit_exceeded", "$")
    elif isinstance(value, list):
        count[0] += len(value)
        require(len(value) <= MAX_ROWS and count[0] <= 200000, "limit_exceeded", "$")
        for item in value:
            _plain(item, depth + 1, count)
    elif isinstance(value, dict):
        for key, item in value.items():
            _plain(key, depth + 1, count)
            _plain(item, depth + 1, count)


def parse(raw):
    require(type(raw) is bytes and len(raw) <= MAX_BYTES, "limit_exceeded", "$")
    try:
        text = raw.decode("utf-8", errors="strict")
        _depth(text)
        value = json.loads(text, object_pairs_hook=_duplicates, parse_int=_integer,
                           parse_float=_number, parse_constant=_number)
        _plain(value)
        return value
    except LoadError:
        raise
    except (UnicodeError, ValueError, RecursionError) as exc:
        raise LoadError("invalid_json", "$") from exc


def validate(value, schema, path="$"):
    if isinstance(schema, dict):
        if "nullable" in schema:
            if value is not None:
                validate(value, schema["nullable"], path)
        elif "enum" in schema:
            require(type(value) is str and value in schema["enum"], "invalid_value", path)
        elif "array" in schema:
            require(type(value) is list and len(value) <= MAX_ROWS, "invalid_type", path)
            for index, row in enumerate(value):
                validate(row, schema["array"], path + "/" + str(index))
        else:
            fields = schema["object"]
            require(type(value) is dict and set(value) == set(fields), "invalid_fields", path)
            for key, item_schema in fields.items():
                validate(value[key], item_schema, path + "/" + key)
        return
    if schema in ("int64", "nonnegative", "positive"):
        require(type(value) is int and MIN_I64 <= value <= MAX_I64, "invalid_type", path)
        if schema != "int64":
            require(value >= (1 if schema == "positive" else 0), "invalid_value", path)
    elif schema == "bool":
        require(type(value) is bool, "invalid_type", path)
    elif schema == "sha256":
        require(type(value) is str and re.fullmatch("[0-9a-f]{64}", value) is not None,
                "invalid_value", path)
    elif schema == "label":
        require(type(value) is str and 1 <= len(value.encode("utf-8")) <= 256
                and value.strip() == value and not any(ord(c) < 32 or 127 <= ord(c) <= 159 for c in value),
                "invalid_value", path)
    else:
        raise LoadError("internal_error", "$")


def _directory(path):
    require(type(path) is str and path.startswith("/") and len(path.encode()) <= 4096,
            "unsafe_path", "$")
    parts = path.split("/")[1:]
    require(bool(parts) and all(x and x not in (".", "..") for x in parts), "unsafe_path", "$")
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in parts[:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        return fd, parts[-1]
    except BaseException:
        os.close(fd)
        raise


def read_raw(path):
    fd = parent = None
    try:
        parent, name = _directory(path)
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        require(stat.S_ISREG(os.fstat(fd).st_mode), "unsafe_path", "$")
        with os.fdopen(fd, "rb") as stream:
            fd = None
            raw = stream.read(MAX_BYTES + 1)
        require(len(raw) <= MAX_BYTES, "limit_exceeded", "$")
        return raw
    except OSError as exc:
        raise LoadError("io_error", "$") from exc
    finally:
        if fd is not None:
            os.close(fd)
        if parent is not None:
            os.close(parent)


def publish_new(path, raw):
    require(type(raw) is bytes and len(raw) <= MAX_BYTES, "limit_exceeded", "$")
    parent = None
    temporary = None
    try:
        parent, name = _directory(path)
        temporary = ".sys05-" + secrets.token_hex(16)
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                     0o600, dir_fd=parent)
        with os.fdopen(fd, "wb") as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.link(temporary, name, src_dir_fd=parent, dst_dir_fd=parent, follow_symlinks=False)
        os.fsync(parent)
    except OSError as exc:
        raise LoadError("io_error", "$") from exc
    finally:
        if parent is not None:
            if temporary is not None:
                try:
                    os.unlink(temporary, dir_fd=parent)
                except FileNotFoundError:
                    pass
            os.close(parent)


def contract():
    raw = read_raw(str(Path(__file__).resolve().with_name("load_contract.json")))
    require(sha(raw) == CONTRACT_SHA256, "contract_mismatch", "$")
    return parse(raw)


def load_config(raw):
    config = parse(raw)
    validate(config, contract()["schemas"]["config"])
    end = config["start_ns"] + config["duration_ns"]
    require(end <= MAX_I64, "time_overflow", "$/duration_ns")
    count = 0
    for segment in config["arrival_schedule"]:
        count += segment["count"]
        final = segment["offset_ns"] + (segment["count"] - 1) * segment["spacing_ns"]
        require(final < config["duration_ns"] and count <= MAX_ROWS,
                "invalid_schedule", "$/arrival_schedule")
    require(count > 0, "invalid_schedule", "$/arrival_schedule")
    kinds = []
    for fault in config["fault_schedule"]:
        require(fault["offset_ns"] + fault["duration_ns"] <= config["duration_ns"],
                "invalid_schedule", "$/fault_schedule")
        kinds.append(fault["kind"])
    scenario = config["scenario"]
    if scenario in ("restart", "publish_delay", "store_failure"):
        require(scenario in kinds, "missing_fault", "$/fault_schedule")
    if scenario in ("cold", "warm"):
        require(scenario == config["cache"], "invalid_cache", "$/cache")
    return config


def common_input_sha(config):
    return sha(b"sys05-common-input-v1\x00" + canonical({k: v for k, v in config.items()
                                                      if k not in ("mode", "run_id")}))


def generate(raw_config):
    config = load_config(raw_config)
    common = common_input_sha(config)
    rows = []
    for segment_index, segment in enumerate(config["arrival_schedule"]):
        for index in range(segment["count"]):
            identity = sha(("sys05-request-id-v1\x00" + common + "\x00" + str(segment_index)
                            + "\x00" + str(index)).encode())
            rows.append({"protocol": "sys05-request-v1", "config_sha256": sha(raw_config),
                         "request_id": identity, "scheduled_ns": config["start_ns"] + segment["offset_ns"]
                         + index * segment["spacing_ns"], "operation": segment["operation"],
                         "payload_sha256": sha(("sys05-payload-v1\x00" + config["corpus_sha256"]
                                               + "\x00" + identity).encode())})
    rows.sort(key=lambda row: (row["scheduled_ns"], row["request_id"]))
    raw = b"".join(canonical(row) + b"\n" for row in rows)
    require(len(raw) <= MAX_BYTES, "limit_exceeded", "$")
    return raw


def parse_jsonl(raw, model):
    require(type(raw) is bytes and len(raw) <= MAX_BYTES, "limit_exceeded", "$")
    lines = raw.splitlines()
    require(len(lines) <= MAX_ROWS and all(lines), "invalid_jsonl", "$")
    rows = [parse(line) for line in lines]
    schema = contract()["schemas"][model]
    for row in rows:
        validate(row, schema)
    return rows


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise LoadError("invalid_cli", "$")


def cli_main(action):
    try:
        return action()
    except LoadError as exc:
        print(json.dumps({"error": exc.code, "path": exc.path}, separators=(",", ":")))
    except Exception:
        print('{"error":"internal_error","path":"$"}')
    return 2


def main():
    parser = Parser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    raw = generate(read_raw(args.config))
    publish_new(args.out, raw)
    print(json.dumps({"protocol": "sys05-generated-v1", "workload_sha256": sha(raw),
                      "requests": len(raw.splitlines()), "service_executed": False}, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(cli_main(main))
