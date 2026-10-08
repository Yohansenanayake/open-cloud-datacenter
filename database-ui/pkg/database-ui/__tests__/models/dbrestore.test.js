import DBRestore from '../../models/dbaas.opencloud.wso2.com.dbrestore';

jest.mock('@shell/plugins/steve/steve-class', () => require('../helpers/steve-class-mock'));

const rootGetters = { 'cluster/schemaFor': () => ({ collectionMethods: ['POST'] }), clusterId: 'c' };
const make = (status = {}, metadata = {}, byId = () => null) => new DBRestore({
  metadata: {
    name: 'r1', namespace: 'ns', uid: 'ru1', ...metadata
  },
  spec: { snapshotRef: { name: 's1' }, targetInstanceName: 'db-new' },
  status,
}, { getters: { byId }, rootGetters });

describe('DBRestore', () => {
  it('follows the operator stages; a running restore being deleted is Cancelling', () => {
    expect(make().stepIndex).toBe(0);
    expect(make({ stage: 'StartingDatabase' }).stepIndex).toBe(2);
    expect(make({ stage: 'Failed', message: 'exists' }).stateMessages[0].message).toBe('exists');
    expect(make({ stage: 'RestoringVolume' }, { deletionTimestamp: 'x' }).stage).toBe('Cancelling');
  });

  it('links the new instance only when this restore created it', () => {
    const mine = { spec: { restoredFrom: { dbRestoreUID: 'ru1' } } };
    const other = { spec: {} };

    expect(make({}, {}, () => mine).targetInstance).toBe(mine);
    expect(make({}, {}, () => other).targetInstance).toBeNull();
  });

  it('retry only for failed restores; delete warns while running', () => {
    const acts = (r) => Object.fromEntries(r._availableActions.map((a) => [a.action, a.enabled]));

    expect(acts(make({ stage: 'Failed' })).retryRestore).toBe(true);
    expect(acts(make({ stage: 'Succeeded' })).retryRestore).toBe(false);
    expect(make({ stage: 'RestoringVolume' }).confirmRemove).toBe(true);
    expect(make({ stage: 'Succeeded' }).warnDeletionMessage).toBe('dbaas.restore.delete.recordOnly:{"target":"db-new"}');
  });
});
