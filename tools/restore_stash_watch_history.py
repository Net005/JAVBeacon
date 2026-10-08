"""Add missing Stash play timestamps from normalized backup exports.

Dry-run by default. Inputs contain scenes with files/fingerprints, stash_ids,
explicit events, title/date/studio. Never use titles or numeric scene IDs alone.
The companion audit can extract these records from read-only SQLite copies.
"""
import argparse
import collections
import datetime
import json
import os
import pathlib
import shutil
import sqlite3
import tempfile
import urllib.request


def normalized(value):
    return ''.join(c for c in (value or '').casefold() if c.isalnum())


def timestamp(value):
    result = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.tzinfo is None:
        raise ValueError('Watch timestamp has no timezone')
    return result.astimezone(datetime.timezone.utc).isoformat()


def fingerprint_keys(scene):
    keys = set()
    for file in scene.get('files', []):
        fp = file.get('fingerprints', {})
        if isinstance(fp, list):
            fp = {x['type']: x['value'] for x in fp}
        if fp.get('md5'):
            keys.add(('md5', str(fp['md5']).lower()))
        if fp.get('oshash') and file.get('size') and file.get('duration'):
            keys.add(('oshash', str(fp['oshash']).lower(), int(file['size']),
                      round(float(file['duration']), 3)))
    return keys


def stable_keys(scene):
    return {(x['endpoint'].rstrip('/'), x['stash_id'])
            for x in scene.get('stash_ids', []) if x.get('stash_id')}


def studio(scene):
    value = scene.get('studio') or ''
    return normalized(value.get('name', '') if isinstance(value, dict) else value)


def metadata_key(scene):
    title, date, name = normalized(scene.get('title')), scene.get('date'), studio(scene)
    return (title, date, name) if title and date and name else None


def identity(scene):
    return {'metadata': list(metadata_key(scene) or ()),
            'fingerprints': sorted([list(k) for k in fingerprint_keys(scene)]),
            'provider_ids': sorted([list(k) for k in stable_keys(scene)])}


def plan(sources, current, roots):
    by_id = {s['id']: s for s in current}
    hashes, stable, metadata = (collections.defaultdict(set) for _ in range(3))
    for s in current:
        for k in fingerprint_keys(s):
            hashes[k].add(s['id'])
        for k in stable_keys(s):
            stable[k].add(s['id'])
        if metadata_key(s):
            metadata[metadata_key(s)].add(s['id'])
    wanted, evidence, exceptions = collections.defaultdict(set), collections.defaultdict(list), []
    for s in sources:
        if not s.get('events'):
            continue
        strong = set().union(*(hashes[k] for k in fingerprint_keys(s)),
                             *(stable[k] for k in stable_keys(s)))
        method = 'file fingerprint or stable provider ID'
        candidates = strong
        if not strong:
            candidates = metadata[metadata_key(s)] if metadata_key(s) else set()
            method = 'unique studio + release date + exact normalized title'
        if len(candidates) != 1:
            exceptions.append({'source': s.get('id'), 'title': s.get('title'),
                               'reason': 'conflicting identity' if candidates else 'no confirmed identity'})
            continue
        target = by_id[next(iter(candidates))]
        # Provider IDs can be attached incorrectly. Require agreement in title
        # and date unless an actual file fingerprint independently confirms it.
        hash_ids = set().union(*(hashes[k] for k in fingerprint_keys(s)))
        if not hash_ids and (normalized(s.get('title')) != normalized(target.get('title'))
                             or not s.get('date') or s['date'] != target.get('date')):
            exceptions.append({'source': s.get('id'), 'title': s.get('title'), 'reason': 'metadata conflict'})
            continue
        if not any(f['path'].startswith(root.rstrip('/') + '/') for f in target.get('files', []) for root in roots):
            continue
        wanted[target['id']].update(timestamp(t) for t in s['events'])
        evidence[target['id']].append({'source': s.get('id'), 'method': method})
    changes = []
    for ident, times in sorted(wanted.items()):
        existing = {timestamp(t) for t in by_id[ident].get('play_history', [])}
        missing = sorted(times - existing)
        if missing:
            changes.append({'scene_id': ident, 'title': by_id[ident]['title'],
                            'times': missing, 'evidence': evidence[ident],
                            'target_identity': identity(by_id[ident])})
    return {'changes': changes, 'exceptions': exceptions, 'confirmed_scenes': len(wanted)}


def graphql(base, key, query, variables=None):
    request = urllib.request.Request(base.rstrip('/') + '/graphql',
        data=json.dumps({'query': query, 'variables': variables or {}}).encode(),
        headers={'ApiKey': key, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=180) as response:
        result = json.load(response)
    if result.get('errors'):
        raise RuntimeError('Stash GraphQL request failed')
    return result['data']


def apply(base, key, changes):
    updated = plays = 0
    for change in changes:
        ident = change['scene_id']
        # Recheck immediately before every write; replaying a plan is additive
        # and idempotent even if another process has imported the same events.
        target = graphql(base, key, 'query($id:ID!){findScene(id:$id){title date studio{name} stash_ids{endpoint stash_id} files{size duration fingerprints{type value}} play_history}}', {'id': ident})['findScene']
        if target is None or identity(target) != change['target_identity']:
            raise RuntimeError('Target identity changed; regenerate the audit')
        present = {timestamp(t) for t in target['play_history']}
        times = [t for t in change['times'] if t not in present]
        if times:
            graphql(base, key, 'mutation($id:ID!,$times:[Timestamp!]!){sceneAddPlay(id:$id,times:$times){count}}', {'id': ident, 'times': times})
            updated += 1
            plays += len(times)
    return {'updated': updated, 'plays': plays}


def read_backup(path):
    """Read a private snapshot, including WAL, without opening the original DB."""
    with tempfile.TemporaryDirectory(prefix='stash-history-') as directory:
        copy = pathlib.Path(directory) / 'backup.sqlite'
        shutil.copyfile(path, copy)
        if pathlib.Path(str(path) + '-wal').exists():
            shutil.copyfile(str(path) + '-wal', str(copy) + '-wal')
        connection = sqlite3.connect('file:' + str(copy) + '?mode=ro', uri=True)
        connection.row_factory = sqlite3.Row
        try:
            events, files, identities = (collections.defaultdict(list) for _ in range(3))
            for row in connection.execute('SELECT scene_id,view_date FROM scenes_view_dates'):
                events[str(row[0])].append(row[1])
            for row in connection.execute('SELECT scene_id,endpoint,stash_id FROM scene_stash_ids'):
                identities[str(row[0])].append({'endpoint': row[1], 'stash_id': row[2]})
            query = ('SELECT sf.scene_id,f.id,f.basename,fo.path folder,f.size,v.duration '
                     'FROM scenes_files sf JOIN files f ON f.id=sf.file_id '
                     'JOIN folders fo ON fo.id=f.parent_folder_id '
                     'LEFT JOIN video_files v ON v.file_id=f.id')
            for row in connection.execute(query):
                file = dict(row)
                file['path'] = file['folder'].rstrip('/') + '/' + file['basename']
                file['fingerprints'] = {r[0]: r[1] for r in connection.execute(
                    'SELECT type,fingerprint FROM files_fingerprints WHERE file_id=?', (file['id'],))
                    if r[0] in ('md5', 'oshash')}
                files[str(file['scene_id'])].append(file)
            result = []
            for row in connection.execute('SELECT s.id,s.title,s.code,s.date,st.name studio '
                                          'FROM scenes s LEFT JOIN studios st ON st.id=s.studio_id'):
                scene = dict(row)
                scene['id'] = str(scene['id'])
                scene.update(events=events[scene['id']], files=files[scene['id']],
                             stash_ids=identities[scene['id']])
                result.append(scene)
            return result
        finally:
            connection.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sources', help='Optional normalized JSON source records')
    parser.add_argument('--database', action='append', default=[], help='Read-only Stash backup; repeat for multiple backups')
    parser.add_argument('--current', required=True)
    parser.add_argument('--root', action='append', required=True)
    parser.add_argument('--report', required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    sources = json.load(open(args.sources)) if args.sources else []
    for database in args.database:
        sources.extend(read_backup(database))
    if not sources:
        parser.error('Provide --database or --sources')
    current = json.load(open(args.current))['data']['findScenes']['scenes']
    result = plan(sources, current, args.root)
    with open(args.report, 'w') as report:
        json.dump(result, report, indent=2)
    print(json.dumps({'confirmed_scenes': result['confirmed_scenes'], 'changes': len(result['changes']), 'plays': sum(len(x['times']) for x in result['changes']), 'exceptions': len(result['exceptions'])}))
    if args.apply:
        print(json.dumps(apply(os.environ['STASH_URL'], os.environ['STASH_API_KEY'], result['changes'])))


if __name__ == '__main__':
    main()
