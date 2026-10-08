import { defaultSnapshotName, validateSnapshotName } from '../../utils/dbsnapshot';

describe('manual snapshot names', () => {
  it('default name is UTC and fits 63 characters', () => {
    expect(defaultSnapshotName('orders-db', new Date(Date.UTC(2026, 9, 8, 5, 7)))).toBe('orders-db-manual-20261008-0507');

    const long = defaultSnapshotName('a'.repeat(52), new Date(Date.UTC(2026, 0, 1)));

    expect(long.length).toBeLessThanOrEqual(63);
    expect(validateSnapshotName(long)).toBeUndefined();
  });

  it('rejects names reserved for automated snapshots', () => {
    expect(validateSnapshotName('db1-auto-20261008').key).toBe('dbaas.snapshot.validation.automatedName');
    expect(validateSnapshotName('Bad_Name').key).toBe('dbaas.instance.validation.dnsLabel');
    expect(validateSnapshotName('a'.repeat(64)).key).toBe('dbaas.snapshot.validation.tooLong');
  });
});
