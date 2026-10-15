#!/usr/bin/env python3
"""Independent deterministic scoring of live agent final answers and MCP events."""
import argparse
import csv
import json
from live_agents import artifact_path, confined_path


def valid_recall(body, validation):
    if isinstance(body, str):
        try:
            body = json.loads(body)
        except ValueError:
            return False
    if not isinstance(body, dict):
        return False
    return any(memory.get('id') == validation['memory_id'] and
               memory.get('text') == validation['fact']
               for memory in body.get('memories', []))


def claude_recall(message, recall_ids, validation):
    if not isinstance(message, dict):
        return False
    called = False
    for block in message.get('content', []):
        if not isinstance(block, dict):
            continue
        if block.get('type') == 'tool_use' and block.get('name', '').endswith('__recall'):
            recall_ids.add(block['id'])
        if (block.get('type') == 'tool_result' and
                block.get('tool_use_id') in recall_ids and not block.get('is_error')):
            called = called or valid_recall(block.get('content'), validation)
    return called


def codex_recall(item, validation):
    if (item.get('type') != 'mcp_tool_call' or item.get('tool') != 'recall' or
            item.get('status') != 'completed' or item.get('error')):
        return False
    return valid_recall((item.get('result') or {}).get('structured_content'), validation)


def read_answer(events, validation):
    final = ''
    called = False
    recall_ids = set()
    for event in events:
        if event.get('type') == 'result':
            final = event.get('result', '')
        item = event.get('item', {})
        if item.get('type') == 'agent_message':
            final = item.get('text', '')
        called = codex_recall(item, validation) or called
        called = claude_recall(event.get('message', {}), recall_ids, validation) or called
    try:
        answer = json.JSONDecoder().raw_decode(final[final.index('{'):])[0]
    except (ValueError, TypeError):
        answer = {}
    return answer, called


def score_direction(root, runs, direction):
    label = f'direction-{direction}'
    validation = next(row for row in runs if row['label'] == label + '-validation')
    events = [json.loads(line) for line in
              artifact_path(root, label + '-reader.jsonl').read_text().splitlines()]
    answer, called = read_answer(events, validation)
    correct = (answer.get('transformed_label') == 'MiXeD' if direction == 1 else
               answer.get('cache_zero') is False and answer.get('positive_ttl') == 15)
    return {'direction': validation['writer'] + ' -> ' + validation['reader'],
            'actual_recall': called, 'task_correct': correct,
            'source_id_correct': answer.get('source_id') in validation['source_ids'],
            'preapproval_search_count': validation['before_items'], 'answer': answer,
            'evidence': label + '-reader.jsonl'}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('root')
    args = parser.parse_args()
    root = confined_path(args.root)
    runs = json.loads(artifact_path(root, 'runs.json').read_text())
    scores = [score_direction(root, runs, direction) for direction in (1, 2)]
    with artifact_path(root, 'scores.json').open('w') as output:
        json.dump(scores, output, indent=2)
        output.write('\n')
    with artifact_path(root, 'scores.csv').open('w', newline='') as output:
        writer = csv.DictWriter(output, fieldnames=scores[0], lineterminator='\n')
        writer.writeheader()
        writer.writerows(scores)
    print(json.dumps(scores, indent=2))


if __name__ == '__main__':
    main()
