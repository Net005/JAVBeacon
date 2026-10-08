import unittest
from unittest.mock import patch
from restore_stash_watch_history import plan, apply

ROOT = '/collections/misc/whisparr'


def scene(ident, title='Scene', date='2024-01-01', studio='Studio', **kw):
    return dict(id=ident, title=title, date=date, studio=studio,
                files=[{'path': ROOT+'/moved.mp4'}], **kw)


class RestoreTests(unittest.TestCase):
    def test_provider_id_preserves_moved_file_and_deduplicates_backup_events(self):
        identity = [{'endpoint': 'https://stashdb.org/graphql', 'stash_id': 'stable'}]
        source = scene('old', stash_ids=identity, events=['2024-02-01T12:00:00+02:00', '2024-02-02T10:00:00Z'])
        target = scene('new', stash_ids=identity, play_history=['2024-02-01T10:00:00Z'])
        result = plan([source, source], [target], [ROOT])
        self.assertEqual(result['changes'][0]['times'], ['2024-02-02T10:00:00+00:00'])

    def test_title_alone_never_matches_and_numbered_parts_stay_separate(self):
        old = scene('old', title='Scene Part 1', events=['2024-02-01T10:00:00Z'])
        self.assertEqual(plan([old], [scene('new', title='Scene Part 2')], [ROOT])['changes'], [])
        self.assertEqual(plan([old], [scene('new', title='Scene Part 1', studio='Other')], [ROOT])['changes'], [])

    def test_ambiguous_metadata_and_conflicting_strong_ids_are_rejected(self):
        old = scene('old', events=['2024-02-01T10:00:00Z'])
        self.assertEqual(plan([old], [scene('one'), scene('two')], [ROOT])['changes'], [])
        old['files'][0]['fingerprints'] = {'md5': 'a'}
        old['stash_ids'] = [{'endpoint': 'x', 'stash_id': 'b'}]
        one, two = scene('one'), scene('two', stash_ids=old['stash_ids'])
        one['files'][0]['fingerprints'] = [{'type': 'md5', 'value': 'a'}]
        result = plan([old], [one, two], [ROOT])
        self.assertEqual(result['changes'], [])
        self.assertEqual(result['exceptions'][0]['reason'], 'conflicting identity')

    def test_old_numeric_id_and_title_do_not_override_metadata_conflicts(self):
        old = scene('same', events=['2024-02-01T10:00:00Z'])
        self.assertEqual(plan([old], [scene('same', date='2025-01-01')], [ROOT])['changes'], [])

    def test_identical_file_hash_survives_title_and_path_changes(self):
        old = scene('old', events=['2024-02-01T10:00:00Z'])
        old['files'][0]['fingerprints'] = {'md5': 'a'}
        target = scene('new', title='Renamed', date='2025-01-01')
        target['files'][0]['fingerprints'] = [{'type': 'md5', 'value': 'a'}]
        self.assertEqual(len(plan([old], [target], [ROOT])['changes']), 1)
        self.assertEqual(plan([old], [target], ['/other'])['changes'], [])

    def test_apply_rechecks_identity_and_existing_events(self):
        source = scene('old', events=['2024-02-01T10:00:00Z'])
        target = scene('new', play_history=[])
        changes = plan([source], [target], [ROOT])['changes']
        already = dict(target, play_history=['2024-02-01T10:00:00Z'])
        with patch('restore_stash_watch_history.graphql', return_value={'findScene': already}) as call:
            self.assertEqual(apply('url', 'key', changes), {'updated': 0, 'plays': 0})
            self.assertEqual(call.call_count, 1)
        with patch('restore_stash_watch_history.graphql', return_value={'findScene': scene('new', title='Other')}):
            with self.assertRaisesRegex(RuntimeError, 'identity changed'):
                apply('url', 'key', changes)

    def test_invalid_or_timezone_free_history_is_not_invented(self):
        old = scene('old', events=['2024-02-01T10:00:00'])
        with self.assertRaises(ValueError):
            plan([old], [scene('new')], [ROOT])


if __name__ == '__main__':
    unittest.main()
