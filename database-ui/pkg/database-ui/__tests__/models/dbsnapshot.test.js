import DBSnapshot from '../../models/dbaas.opencloud.wso2.com.dbsnapshot';

jest.mock('@shell/plugins/steve/steve-class', () => require('../helpers/steve-class-mock'));

const make = (status = {}, metadata = {}, ctx = {}) => new DBSnapshot({
  metadata: {
    name: 's1', namespace: 'ns', ...metadata
  },
  spec: { sourceInstanceRef: { name: 'db1' } },
  status,
}, ctx);
const ready = (status, reason) => ({
  conditions: [{
    type: 'Ready', status, reason, lastTransitionTime: '2026-10-08T01:00:00Z'
  }]
});

describe('DBSnapshot', () => {
  it('uses status.phase, or derives it like the operator for older snapshots', () => {
    expect(make({ phase: 'InProgress' }).stateDisplay).toBe('In Progress');
    expect(make(ready('True', 'BackupReady')).phase).toBe('Ready');
    expect(make(ready('False', 'BackupTimedOut')).phase).toBe('Failed');
    expect(make(ready('False', 'SnapshotHoldWaiting')).phase).toBe('Queued');
    expect(make({ phase: 'Ready' }, { deletionTimestamp: 'x' }).phase).toBe('Deleting');
  });

  it('links the source only when the UID matches', () => {
    const instance = { metadata: { uid: 'u1' } };
    const getters = { byId: (type, id) => (id === 'ns/db1' ? instance : null) };

    expect(make({ source: { instanceUID: 'u1' } }, {}, { getters }).sourceInstance).toBe(instance);
    expect(make({ source: { instanceUID: 'old' } }, {}, { getters }).sourceInstance).toBeNull();
  });

  it('offers Restore only when Ready and allowed, and hides edit actions', () => {
    const rootGetters = { 'cluster/schemaFor': () => ({ collectionMethods: ['POST'] }), clusterId: 'c' };
    const acts = make({ phase: 'Ready' }, {}, { rootGetters })._availableActions;

    expect(acts.map((a) => a.action)).toEqual(['restoreToNewInstance', 'promptRemove']);
    expect(acts[0].enabled).toBe(true);
    expect(make({ phase: 'InProgress' }, {}, { rootGetters })._availableActions[0].enabled).toBe(false);
  });
});
