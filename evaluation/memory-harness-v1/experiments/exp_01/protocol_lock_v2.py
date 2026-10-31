"""EXP-01 protocol v2: immutable final lock over owner-decided research values.

The draft v1 files stay untouched; this version only binds a separate values file.
"""
import importlib.util
import os
from pathlib import Path
import re
import sys


def _load_generator():
    alias = '_arkism_exp01_generate_streams'
    target = Path(__file__).with_name('generate_streams.py')
    cached = sys.modules.get(alias)
    if cached is not None:
        if Path(cached.__file__).absolute() != target.absolute():
            raise RuntimeError('fixed sibling mismatch')
        return cached
    spec = importlib.util.spec_from_file_location(alias, target)
    module = importlib.util.module_from_spec(spec)
    sys.modules[alias] = module
    try:
        spec.loader.exec_module(module)
    except BaseException:
        sys.modules.pop(alias, None)
        raise
    return module


try:
    G = _load_generator()
except Exception:
    if __name__ == '__main__':
        print('{"protocol":"exp01-error-v1","code":"dependency_mismatch","path":"$.external_dependencies"}')
        raise SystemExit(2) from None
    raise

PROTOCOL = 'exp01-protocol-final-v2'
STATE = 'final_locked'
LABEL_VERSION = 'labels_v2'
# Owner answers carry their decision date; proposal defaults (e.g. proposal_r2_default_not_asked) never match.
OWNER_DECIDED = re.compile(r'owner_[a-z]+_[0-9]{8}\Z', re.ASCII)
FIELDS = ('protocol', 'state', 'values_sha256', 'baseline_contract_sha256', 'dataset_contract_sha256', 'manifest_sha256',
          'dataset_id', 'dataset_version', 'label_version', 'research', 'provenance', 'lock_sha256')


def _research(values_raw):
    document = G.parse_raw(values_raw)
    G.require(type(document) is dict and type(document.get('values')) is dict, 'missing_field', '$.values')
    values = document['values']
    keys = G.ENUMS['ResearchDecisionID']
    for key in values:
        G.require(key in keys, 'unknown_field', '$.values.' + key)
    research, provenance = {}, {}
    for key in keys:
        entry = values.get(key)
        G.require(type(entry) is dict and set(entry) == {'value', 'provenance'}, 'research_unresolved', '$.values.' + key)
        G.require(entry['value'] is not None, 'research_unresolved', '$.values.' + key)
        G.require(type(entry['provenance']) is str and OWNER_DECIDED.match(entry['provenance']) is not None,
                  'provenance_not_owner_decided', '$.values.' + key)
        research[key], provenance[key] = entry['value'], entry['provenance']
    return research, provenance


def _manifest(manifest_raw):
    manifest = G.decode_typed(manifest_raw, 'SplitManifest')
    G.require(G.compact(manifest) + b'\n' == manifest_raw, 'invalid_value', '$.manifest')
    G.require(manifest['contract_sha256'] == G.CONTRACT_SHA256, 'dependency_mismatch', '$.contract_sha256')
    G.require(manifest['external_dependencies'] == G.DEPENDENCIES, 'dependency_mismatch', '$.external_dependencies')
    G.require(manifest['label_version'] == LABEL_VERSION, 'identity_conflict', '$.label_version')
    return manifest


def build_lock(values_raw, manifest_raw):
    research, provenance = _research(values_raw)
    manifest = _manifest(manifest_raw)
    record = dict(protocol=PROTOCOL, state=STATE, values_sha256=G.sha(values_raw),
                  baseline_contract_sha256=G.DEPENDENCIES['baseline_contract_sha256'],
                  dataset_contract_sha256=G.CONTRACT_SHA256, manifest_sha256=G.sha(manifest_raw),
                  dataset_id=manifest['dataset_id'], dataset_version=manifest['dataset_version'],
                  label_version=manifest['label_version'], research=research, provenance=provenance)
    record['lock_sha256'] = G.sha(G.compact(record))
    return record


def lock_bytes(record):
    return G.compact(record) + b'\n'


def verify_lock(lock_raw, values_raw, manifest_raw, requested_state=STATE):
    record = G.parse_raw(lock_raw)
    G.require(type(record) is dict and tuple(sorted(record)) == tuple(sorted(FIELDS)), 'invalid_value', '$.lock')
    G.require(lock_bytes(record) == lock_raw, 'invalid_value', '$.lock')
    body = {k: v for k, v in record.items() if k != 'lock_sha256'}
    G.require(record['lock_sha256'] == G.sha(G.compact(body)), 'hash_mismatch', '$.lock_sha256')
    expected = build_lock(values_raw, manifest_raw)
    for field in FIELDS:
        G.require(record[field] == expected[field], 'hash_mismatch' if field.endswith('sha256') else 'identity_conflict', '$.' + field)
    # A final lock has no successor state in this version: re-locking, editing or reverting to draft are all refused.
    G.require(requested_state == STATE, 'final_lock_immutable', '$.state')
    return dict(protocol='exp01-lock-verification-v2', status='valid_final_locked', lock_sha256=record['lock_sha256'],
                values_sha256=record['values_sha256'], manifest_sha256=record['manifest_sha256'], label_version=record['label_version'])


def write_lock(record, out_path):
    out_path = Path(out_path)
    G.require(out_path.is_absolute(), 'unsafe_path')
    try:
        fd = os.open(out_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o444)
    except FileExistsError:
        G.fail('output_exists')
    except OSError:
        G.fail('io_error')
    with os.fdopen(fd, 'wb') as handle:
        handle.write(lock_bytes(record))
    return dict(protocol='exp01-lock-written-v2', lock_sha256=record['lock_sha256'])


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv

    def run():
        G.require(bool(argv) and argv[0] in ('lock', 'verify'), 'invalid_argv')
        if argv[0] == 'lock':
            args = G.cli_args(argv[1:], ('--values', '--manifest', '--out'))
            return write_lock(build_lock(G.bounded_read(args['--values']), G.bounded_read(args['--manifest'])), args['--out'])
        args = G.cli_args(argv[1:], ('--lock', '--values', '--manifest', '--request-state'))
        return verify_lock(G.bounded_read(args['--lock']), G.bounded_read(args['--values']),
                           G.bounded_read(args['--manifest']), args['--request-state'])
    return G.cli_result(run)


if __name__ == '__main__':
    raise SystemExit(main())
