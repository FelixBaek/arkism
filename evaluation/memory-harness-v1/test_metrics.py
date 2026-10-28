"""R6 independent hand-calculated metrics and CLI regression boundaries."""
import contextlib
import copy
import hashlib
import io as stdio
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import contract_io as io
import metrics

CID = 'arkism-offline-b0-v1'
PID = 'b0-rrf-synthetic-v1'
SCOPE = {'workspace': 'ws', 'project': 'proj', 'user': 'usr', 'agent': 'agt'}
BINDINGS = dict(zip(('input_manifest_sha256', 'policy_sha256', 'predictions_sha256',
                     'evaluation_manifest_sha256'), ('1'*64, '2'*64, '3'*64, '4'*64)))


def ref(symbol):
    return {'origin': 'memory', 'collection': 'memories', 'id': symbol,
            'revision': 1, 'content_sha256': hashlib.sha256(symbol.encode()).hexdigest()}


def fixture():
    # Fixed original GV-MET-01..04 table; no implementation-generated oracle.
    table = [('s1', 'q1', 'relevant', ['a', 'b'], 'success', ['a', 'a', 'c']),
             ('s1', 'q2', 'relevant', ['b'], 'timeout', ['b']),
             ('s2', 'q3', 'relevant', ['d'], 'success', ['d']),
             ('s3', 'q4', 'clear_negative', [], 'success', []),
             ('s3', 'q5', 'clear_negative', [], 'success', ['e']),
             ('s3', 'q6', 'clear_negative', [], 'timeout', []),
             ('s3', 'q7', 'ambiguous', [], 'success', [])]
    probes, labels, predictions = [], [], []
    for stream, query, label, gold, status, selected in table:
        common = {'query_id': query, 'stream_id': stream, 'scope': dict(SCOPE)}
        probe = dict(common, at_ns=100, cutoff_accepted_seq=3,
                     candidate_snapshot_id='snapshot_' + query, final_k=3,
                     context_budget={'unit': 'fixture_units_v0', 'limit': 10}, policy_id=PID)
        probes.append(probe)
        labels.append(dict(common, at_ns=100, cutoff_accepted_seq=3, temporal_kind='current',
                           label_status=label, relevant_refs=[ref(x) for x in gold]))
        predictions.append(dict(common, status=status, selected_refs=[ref(x) for x in selected],
                                error_code={'success': 'none', 'timeout': 'timeout'}[status]))
    run = {'contract_id': CID, 'run_id': 'run', 'policy_id': PID,
           'input_manifest_sha256': BINDINGS['input_manifest_sha256'],
           'policy_sha256': BINDINGS['policy_sha256'], 'predictions': predictions}
    return run, {'contract_id': CID, 'probes': probes}, {'contract_id': CID, 'labels': labels}, dict(BINDINGS)


def subset(args, indices):
    run, probes, gold, bindings = copy.deepcopy(args)
    run['predictions'] = [run['predictions'][i] for i in indices]
    probes['probes'] = [probes['probes'][i] for i in indices]
    gold['labels'] = [gold['labels'][i] for i in indices]
    return run, probes, gold, bindings


class MetricsTests(unittest.TestCase):
    def calculate(self, args=None):
        return metrics.calculate_metrics(*(fixture() if args is None else args))

    def rejects(self, args, code):
        with self.assertRaises(io.ContractError) as caught:
            self.calculate(args)
        self.assertEqual(caught.exception.code, code)
        self.assertNotIn('secret', str(caught.exception))

    def test_fixed_query_golden_duplicate_and_failed_partial(self):
        result = self.calculate()
        q1, q2, q3 = result['queries'][:3]
        self.assertEqual((q1['raw_selected_count'], q1['unique_selected_count'], q1['relevant_count'],
                          q1['matched_count'], q1['recall_at_k'], q1['hit_any']), (3, 2, 2, 1, .5, 1))
        self.assertEqual((q2['raw_selected_count'], q2['unique_selected_count'], q2['matched_count'],
                          q2['recall_at_k'], q2['hit_any']), (1, 1, 0, 0, 0))
        self.assertEqual(q3['recall_at_k'], 1)

    def test_fixed_macro_and_query_weighted_are_distinct(self):
        result = self.calculate()
        summary = result['summary']
        self.assertEqual(summary['stream_macro_recall_at_k'],
                         {'numerator': 1.25, 'denominator': 2, 'value': .625, 'null_reason': 'none'})
        self.assertEqual(summary['query_weighted_recall_at_k']['value'], .5)
        self.assertEqual(summary['query_hit_any_rate']['value'], 2/3)
        self.assertEqual(summary['all_query_failure_rate']['value'], 2/7)
        self.assertEqual(summary['observed_negative_false_positive_rate']['value'], .5)
        self.assertEqual(summary['negative_service_failure_rate']['value'], 2/3)
        self.assertEqual(set(summary), {'stream_macro_recall_at_k', 'query_weighted_recall_at_k',
                                        'query_hit_any_rate', 'all_query_failure_rate',
                                        'observed_negative_false_positive_rate', 'negative_service_failure_rate'})
        self.assertEqual([s['recall_at_k']['value'] for s in result['streams']], [.25, 1, None])
        self.assertEqual(result['streams'][2]['recall_at_k']['null_reason'], 'no_denominator')

    def test_fixed_counts_and_schema(self):
        result = self.calculate()
        self.assertEqual(result['counts'], {'scheduled_queries': 7, 'streams': 3,
                         'relevant_queries': 3, 'clear_negative_queries': 3, 'ambiguous_queries': 1,
                         'successful_queries': 5, 'failed_queries': 2, 'streams_without_relevant': 1})
        io.validate_typed('MetricRun', result)
        for name in BINDINGS:
            self.assertEqual(result[name], BINDINGS[name])
        self.assertEqual([r['query_id'] for r in result['queries']], ['q1','q2','q3','q4','q5','q6','q7'])
        self.assertEqual([r['stream_id'] for r in result['streams']], ['s1','s2','s3'])

    def test_negative_and_ambiguous_are_not_relevance_zero(self):
        for row in self.calculate()['queries'][3:]:
            self.assertIsNone(row['recall_at_k'])
            self.assertIsNone(row['hit_any'])
            self.assertEqual(row['matched_count'], 0)

    def test_no_relevant_denominator_is_null(self):
        result = self.calculate(subset(fixture(), [3,4,5,6]))
        for name in ('stream_macro_recall_at_k','query_weighted_recall_at_k','query_hit_any_rate'):
            self.assertEqual(result['summary'][name],
                             {'numerator': 0.0, 'denominator': 0, 'value': None, 'null_reason': 'no_denominator'})
        self.assertEqual(result['counts']['streams_without_relevant'], 1)

    def test_no_negative_denominator_is_null(self):
        result = self.calculate(subset(fixture(), [0,1,2]))
        for name in ('observed_negative_false_positive_rate','negative_service_failure_rate'):
            self.assertIsNone(result['summary'][name]['value'])
            self.assertEqual(result['summary'][name]['denominator'], 0)

    def test_empty_schedule_all_denominators_null(self):
        result = self.calculate(subset(fixture(), []))
        self.assertEqual(result['queries'], [])
        self.assertEqual(result['streams'], [])
        self.assertTrue(all(s['value'] is None for s in result['summary'].values()))
        self.assertTrue(all(n == 0 for n in result['counts'].values()))

    def test_error_status_preserves_failed_recall_and_counts(self):
        args = fixture(); args[0]['predictions'][1].update(status='error', error_code='execution_error')
        result = self.calculate(args)
        self.assertEqual(result['summary']['stream_macro_recall_at_k']['value'], .625)
        self.assertEqual(result['counts']['failed_queries'], 2)

    def test_success_empty_is_not_timeout(self):
        args = fixture(); args[0]['predictions'][1].update(status='success', error_code='none', selected_refs=[])
        result = self.calculate(args)
        self.assertEqual(result['summary']['stream_macro_recall_at_k']['value'], .625)
        self.assertEqual(result['summary']['all_query_failure_rate']['value'], 1/7)

    def test_failed_partial_above_k_retains_raw_unique(self):
        args = fixture(); args[0]['predictions'][1]['selected_refs'] = [ref(x) for x in ['a','b','c','d']]
        row = self.calculate(args)['queries'][1]
        self.assertEqual((row['raw_selected_count'], row['unique_selected_count'], row['matched_count']), (4,4,0))

    def test_all_negative_failures_have_no_observed_denominator(self):
        args = subset(fixture(), [5]); row = self.calculate(args)
        self.assertIsNone(row['summary']['observed_negative_false_positive_rate']['value'])
        self.assertEqual(row['summary']['negative_service_failure_rate']['value'], 1)

    def test_success_raw_above_k_before_dedup(self):
        args = fixture(); args[1]['probes'][0]['final_k'] = 2
        self.rejects(args, 'schedule_mismatch')

    def test_prediction_missing_duplicate_unknown_and_reordered(self):
        for mutation in ('missing','duplicate','unknown','reordered'):
            with self.subTest(mutation=mutation):
                args = fixture(); rows = args[0]['predictions']
                if mutation == 'missing': rows.pop(1)
                elif mutation == 'duplicate': rows.append(copy.deepcopy(rows[0]))
                elif mutation == 'unknown': rows[0]['query_id'] = 'unknown'
                else: rows[0], rows[1] = rows[1], rows[0]
                self.rejects(args, 'schedule_mismatch')

    def test_gold_missing_duplicate_unknown(self):
        for mutation in ('missing','duplicate','unknown'):
            with self.subTest(mutation=mutation):
                args = fixture(); rows = args[2]['labels']
                if mutation == 'missing': rows.pop()
                elif mutation == 'duplicate': rows.append(copy.deepcopy(rows[0]))
                else: rows[0]['query_id'] = 'unknown'
                self.rejects(args, 'schedule_mismatch')

    def test_gold_order_is_not_prediction_order(self):
        args = fixture(); args[2]['labels'].reverse()
        self.assertEqual(self.calculate(args), self.calculate())

    def test_gold_all_schedule_binding_fields(self):
        for field, value in [('stream_id','other'),('scope',dict(SCOPE,user='other')),
                             ('at_ns',101),('cutoff_accepted_seq',4)]:
            with self.subTest(field=field):
                args=fixture();args[2]['labels'][0][field]=value
                self.rejects(args,'snapshot_binding')

    def test_prediction_stream_and_four_scope_fields(self):
        for field in ('stream_id','workspace','project','user','agent'):
            with self.subTest(field=field):
                args=fixture()
                if field=='stream_id':args[0]['predictions'][0][field]='other'
                else:args[0]['predictions'][0]['scope'][field]='other'
                self.rejects(args,'snapshot_binding')

    def test_full_reference_five_fields(self):
        for field,value in [('origin','fixture_alt'),('collection','memories'),('id','other'),
                            ('revision',2),('content_sha256','f'*64)]:
            if field=='collection':continue # v1 deliberately has one allowed collection.
            with self.subTest(field=field):
                args=subset(fixture(),[2]);args[0]['predictions'][0]['selected_refs'][0][field]=value
                self.assertEqual(self.calculate(args)['queries'][0]['recall_at_k'],0)
        args=subset(fixture(),[2]);args[0]['predictions'][0]['selected_refs'][0]['collection']='other'
        self.rejects(args,'invalid_value')

    def test_bindings_exact_four_and_format(self):
        for mutation in ('missing','extra','bad_sha','not_dict'):
            with self.subTest(mutation=mutation):
                args=list(fixture())
                if mutation=='missing':args[3].pop('predictions_sha256')
                elif mutation=='extra':args[3]['secret']='secret'
                elif mutation=='bad_sha':args[3]['predictions_sha256']='not_hex'
                else:args[3]=[]
                self.rejects(args,'invalid_value' if mutation!='not_dict' else 'invalid_type')

    def test_input_and_policy_hash_mismatch(self):
        for field in ('input_manifest_sha256','policy_sha256'):
            with self.subTest(field=field):
                args=fixture();args[3][field]='a'*64
                self.rejects(args,'hash_mismatch')

    def test_mutation_free_and_repeatable(self):
        args=fixture();before=copy.deepcopy(args)
        one=self.calculate(args);two=self.calculate(args)
        self.assertEqual(one,two);self.assertEqual(args,before)
        one['queries'][0]['query_id']='changed'
        self.assertEqual(args,before);self.assertEqual(two['queries'][0]['query_id'],'q1')


    def test_ambiguous_nonempty_failure_only_changes_all_query_counts(self):
        args=fixture();args[0]['predictions'][6].update(status='error',error_code='execution_error',selected_refs=[ref('a')])
        result=self.calculate(args)
        self.assertEqual(result['summary']['stream_macro_recall_at_k']['value'],.625)
        self.assertEqual(result['summary']['negative_service_failure_rate']['value'],2/3)
        self.assertEqual(result['summary']['all_query_failure_rate']['value'],3/7)
        self.assertIsNone(result['queries'][6]['recall_at_k'])
        self.assertEqual(result['queries'][6]['unique_selected_count'],1)

    def test_failed_negative_partial_is_not_successful_observation(self):
        args=fixture();args[0]['predictions'][5]['selected_refs']=[ref('a')]
        result=self.calculate(args)
        self.assertEqual(result['summary']['observed_negative_false_positive_rate']['denominator'],2)
        self.assertEqual(result['summary']['negative_service_failure_rate']['value'],2/3)

    def test_arbitrary_schedule_order_is_preserved(self):
        args=subset(fixture(),[2,0,1,6,3,4,5]);result=self.calculate(args)
        self.assertEqual([row['query_id']for row in result['queries']],['q3','q1','q2','q7','q4','q5','q6'])
        self.assertEqual([row['stream_id']for row in result['streams']],['s1','s2','s3'])
        self.assertEqual(result['summary']['stream_macro_recall_at_k']['value'],.625)

    def test_nanosecond_and_cutoff_bindings_are_exact_integers(self):
        for field in ('at_ns','cutoff_accepted_seq'):
            with self.subTest(field=field):
                args=fixture();args[1]['probes'][0][field]=9223372036854775807
                args[2]['labels'][0][field]=9223372036854775806
                self.rejects(args,'snapshot_binding')


def write_bundle(directory):
    run, probes, gold, bindings = fixture()
    policy={'contract_id':CID,'policy_id':PID,'rank_offset':60,'exact_reason_weight':2.0,
            'other_weight':1.0,'context_unit':'fixture_units_v0',
            'eligibility_order':'validate-filter-preserve-ordinal-fuse-pack'}
    candidates={'contract_id':CID,'snapshots':[{'snapshot_id':p['candidate_snapshot_id'],
                'stream_id':p['stream_id'],'scope':dict(SCOPE),'as_of_ns':100,'as_of_seq':3,
                'lanes':[[],[],[]]} for p in probes['probes']]}
    contract=Path(__file__).parent/'contracts'/'implementation_contract.json'
    (directory/'implementation_contract.json').write_bytes(contract.read_bytes())
    for name,obj in [('policy.json',policy),('probes.json',probes),('candidates.json',candidates),
                     ('gold.json',gold),('split_map.json',{'contract_id':CID,'streams':[
                      {'stream_id':s,'root_stream_id':s,'split_role':'unit_fixture'} for s in ['s1','s2','s3']]})]:
        io.write_json_new(directory/name,obj)
    def manifest(kind,names):
        value={'contract_id':CID,'manifest_kind':kind,'dataset_role':'unit_fixture','files':[]}
        for name in sorted(names):
            raw=(directory/name).read_bytes()
            value['files'].append({'logical_path':name,'sha256':io.sha256_bytes(raw),'byte_length':len(raw)})
        return value
    inputs=manifest('baseline_input',['implementation_contract.json','policy.json','probes.json','candidates.json'])
    inputs.update(seed=0,source_commit='27bb41807ff48d47559ba2cfc30feb300caacecf')
    io.write_json_new(directory/'input_manifest.json',inputs)
    io.write_json_new(directory/'evaluation_manifest.json',manifest('baseline_evaluation',['input_manifest.json','gold.json','split_map.json']))
    run['input_manifest_sha256']=io.sha256_bytes((directory/'input_manifest.json').read_bytes())
    run['policy_sha256']=io.sha256_bytes((directory/'policy.json').read_bytes())
    io.write_json_new(directory/'predictions.json',run)


class MetricsCLITests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(prefix='arkism-harness-metrics-')
        self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        write_bundle(self.root)
        self.args=['--evaluation-manifest',str(self.root/'evaluation_manifest.json'),
                   '--predictions',str(self.root/'predictions.json'),'--out',str(self.root/'metrics.json')]

    def invoke(self,args=None):
        out,err=stdio.StringIO(),stdio.StringIO()
        with contextlib.redirect_stdout(out),contextlib.redirect_stderr(err):
            code=metrics.main(self.args if args is None else args)
        return code,out.getvalue(),err.getvalue()

    def test_cli_success_status_file_and_raw_bindings(self):
        code,out,err=self.invoke();self.assertEqual((code,err),(0,''))
        status=json.loads(out);self.assertEqual(status['queries'],7)
        self.assertEqual(status['output'],str(self.root/'metrics.json'))
        data=(self.root/'metrics.json').read_bytes();self.assertTrue(data.endswith(b'\n'))
        value=io.decode_typed(data,'MetricRun');self.assertEqual(value['summary']['stream_macro_recall_at_k']['value'],.625)
        self.assertEqual(value['predictions_sha256'],io.sha256_bytes((self.root/'predictions.json').read_bytes()))
        self.assertEqual(value['evaluation_manifest_sha256'],io.sha256_bytes((self.root/'evaluation_manifest.json').read_bytes()))
        self.assertEqual(data,(json.dumps(value,ensure_ascii=False,sort_keys=True,indent=2)+'\n').encode())

    def test_cli_existing_output_not_overwritten(self):
        path=self.root/'metrics.json';path.write_text('sentinel')
        code,out,err=self.invoke();self.assertEqual((code,out),(2,''));self.assertEqual(json.loads(err)['code'],'output_exists')
        self.assertEqual(path.read_text(),'sentinel')

    def test_cli_usage_rejects_unknown_duplicate_missing_positional_abbreviation(self):
        cases=[self.args+['--secret','secret'],self.args+['--out','secret'],[],self.args+['secret'],
               ['--evaluation',str(self.root/'evaluation_manifest.json')]+self.args[2:]]
        for args in cases:
            with self.subTest(args=args):
                code,out,err=self.invoke(args);self.assertEqual((code,out),(2,''));self.assertEqual(set(json.loads(err)),{'code','path','detail'})
                self.assertNotIn('secret',err);self.assertFalse((self.root/'metrics.json').exists())

    def test_cli_help(self):
        code,out,err=self.invoke(['--help']);self.assertEqual((code,err),(0,''));self.assertIn('--evaluation-manifest',out)

    def test_cli_malformed_input_no_final_output(self):
        (self.root/'predictions.json').write_text('{')
        code,out,err=self.invoke();self.assertEqual((code,out),(2,''));self.assertEqual(json.loads(err)['code'],'invalid_json')
        self.assertFalse((self.root/'metrics.json').exists())

    def test_cli_hash_mismatch(self):
        (self.root/'gold.json').write_bytes(b'{}')
        code,out,err=self.invoke();self.assertEqual((code,out),(2,''));self.assertEqual(json.loads(err)['code'],'hash_mismatch')
        self.assertFalse((self.root/'metrics.json').exists())

    def test_cli_missing_file_exit_one(self):
        (self.root/'predictions.json').unlink()
        code,out,err=self.invoke();self.assertEqual((code,out),(1,''));self.assertEqual(json.loads(err)['code'],'io_error')

    def test_cli_symlink_prediction_rejected(self):
        source=self.root/'predictions.json';target=self.root/'real.json';source.rename(target);source.symlink_to(target)
        code,out,err=self.invoke();self.assertEqual((code,out),(2,''));self.assertEqual(json.loads(err)['code'],'unsafe_path')

    def test_cli_output_symlink_rejected_without_touching_target(self):
        target=self.root/'target';target.write_text('sentinel');(self.root/'metrics.json').symlink_to(target)
        code,out,err=self.invoke();self.assertEqual((code,out),(2,''));self.assertEqual(json.loads(err)['code'],'output_exists')
        self.assertEqual(target.read_text(),'sentinel')

    def test_cli_unexpected_error_is_sanitized(self):
        with mock.patch.object(metrics,'calculate_metrics',side_effect=RuntimeError('secret')):
            code,out,err=self.invoke()
        self.assertEqual((code,out),(1,''));self.assertEqual(json.loads(err),{'code':'io_error','path':'$','detail':''})

    def test_actual_script_exit_and_json(self):
        result=subprocess.run([sys.executable,str(Path(__file__).with_name('metrics.py'))]+self.args,
                              capture_output=True,text=True,check=False)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(len(result.stdout.splitlines()),1);json.loads(result.stdout)
        self.assertEqual(result.stderr,'')


    def test_cli_prediction_hash_bindings_mismatch_before_output(self):
        path=self.root/'predictions.json';run=json.loads(path.read_bytes())
        run['policy_sha256']='f'*64
        with path.open('w',encoding='utf-8')as stream:
            json.dump(run,stream)
        code,out,err=self.invoke();self.assertEqual((code,out),(2,''))
        self.assertEqual(json.loads(err)['code'],'hash_mismatch')
        self.assertFalse((self.root/'metrics.json').exists())

    def test_cli_output_parent_symlink_and_traversal_rejected(self):
        link=self.root/'linked';link.symlink_to(self.root,target_is_directory=True)
        for path in (link/'metrics.json',self.root/'sub'/'..'/'metrics.json'):
            with self.subTest(path=path):
                args=self.args[:-1]+[str(path)];code,out,err=self.invoke(args)
                self.assertEqual((code,out),(2,''));self.assertEqual(json.loads(err)['code'],'unsafe_path')
                self.assertFalse((self.root/'metrics.json').exists())

    def test_actual_script_contract_error_exit_two(self):
        args=self.args+['--unknown','secret']
        result=subprocess.run([sys.executable,str(Path(__file__).with_name('metrics.py'))]+args,
                              capture_output=True,text=True,check=False)
        self.assertEqual(result.returncode,2);self.assertEqual(result.stdout,'')
        self.assertEqual(json.loads(result.stderr)['code'],'invalid_value')
        self.assertNotIn('secret',result.stderr)


if __name__=='__main__':
    unittest.main()
