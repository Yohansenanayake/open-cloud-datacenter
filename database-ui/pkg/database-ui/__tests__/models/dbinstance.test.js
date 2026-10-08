import DBInstance from '../../models/dbaas.opencloud.wso2.com.dbinstance';

jest.mock('@shell/plugins/steve/steve-class', () => require('../helpers/steve-class-mock'));
jest.mock('@shell/plugins/dashboard-store/resource-class', () => require('../helpers/shell-mocks').resourceClass());
jest.mock('@shell/utils/array', () => require('../helpers/shell-mocks').array());

const TRIGGER = 'dbaas.opencloud.wso2.com/repave-trigger';
const canCreateSnapshots = { 'cluster/schemaFor': () => ({ collectionMethods: ['GET', 'POST'] }), clusterId: 'c-test' };

const make = ({
  metadata = {}, spec = {}, status = {}, ctx = {}
} = {}) => new DBInstance({
  metadata: {
    name: 'db1', namespace: 'ns', uid: 'u1', generation: 1, ...metadata
  },
  spec,
  status: { phase: 'available', ...status },
}, ctx);

const actions = (m) => Object.fromEntries(m._availableActions.map((a) => [a.action, a]));

describe('state', () => {
  it('shows the operator phase, Pending before the first status', () => {
    expect(make({ status: { phase: undefined } }).stateDisplay).toBe('Pending');
    expect(make({ status: { phase: 'crash-loop-halted' } }).stateDisplay).toBe('Crash Loop Halted');
    expect(make().stateColor).toBe('text-success');
  });

  it('flags spec changes not yet reconciled', () => {
    expect(make({ metadata: { generation: 3 }, status: { observedGeneration: 2 } }).hasPendingChanges).toBe(true);
  });

  it('hides the operator status message for a healthy instance', () => {
    expect(make({ status: { message: 'ready', conditions: [{ type: 'Ready', status: 'True' }] } }).stateMessages).toEqual([]);
  });
});

describe('deletion', () => {
  const deleting = (conditions, spec = {}) => make({
    metadata: { deletionTimestamp: 'x' }, spec, status: { conditions }
  });

  it('shows Deleting straight away; waiting for a snapshot is not a problem', () => {
    const m = deleting([{
      type: 'DeletionBlocked', status: 'True', reason: 'DeletionWaitingForSnapshot', message: 'waiting'
    }]);

    expect(m.stateDisplay).toBe('Deleting');
    expect(m.hasProblem).toBe(false);
  });

  it('deletion protection blocks the delete dialog and turns red once deleting', () => {
    expect(make({ spec: { deletionProtection: true } }).preventDeletionMessage).toBe('dbaas.instance.delete.protected:{"name":"db1"}');

    const stuck = deleting([{
      type: 'DeletionBlocked', status: 'True', reason: 'DeletionProtected', message: 'Cannot delete'
    }], { deletionProtection: true });

    expect(stuck.stateColor).toBe('text-error');
    expect(stuck.preventDeletionMessage).toBeNull();
  });

  it('asks for the name and warns about automated snapshots', () => {
    expect(make().confirmRemove).toBe(true);
    expect(make({ spec: { backup: {} } }).warnDeletionMessage).toBe('dbaas.instance.delete.warningWithBackups');
  });
});

describe('start / stop', () => {
  it('offers Stop while running and Start while stopped', () => {
    expect(actions(make()).stopInstance.enabled).toBe(true);
    expect(actions(make()).startInstance.enabled).toBe(false);
    expect(actions(make({ spec: { running: false } })).startInstance.enabled).toBe(true);
    expect(actions(make({ ctx: { canUpdate: false } })).stopInstance.enabled).toBe(false);
  });

  it('patches spec.running; Stop asks first', async() => {
    const m = make({ spec: { running: false } });

    m.patch = jest.fn(() => Promise.resolve());
    m.$dispatch = jest.fn();
    await m.startInstance();
    expect(m.patch).toHaveBeenCalledWith([{
      op: 'add', path: '/spec/running', value: true
    }], {}, false, true);
    m.stopInstance();
    expect(m.$dispatch).toHaveBeenCalledWith('promptModal', expect.objectContaining({ component: 'DBaaSConfirmStopDialog' }));
  });
});

describe('snapshots', () => {
  it('can be taken only with backups enabled on an Available instance', () => {
    expect(make({ spec: { backup: {} }, ctx: { rootGetters: canCreateSnapshots } }).canTakeSnapshot).toBe(true);
    expect(make({ spec: {}, ctx: { rootGetters: canCreateSnapshots } }).takeSnapshotBlockedReason).toBe('dbaas.snapshot.blocked.backupDisabled');
    expect(make({
      spec: { backup: {} }, status: { phase: 'creating' }, ctx: { rootGetters: canCreateSnapshots }
    }).takeSnapshotBlockedReason).toBe('dbaas.snapshot.blocked.notAvailable');
  });

  it('lists only snapshots of this instance (by UID)', () => {
    const mine = {
      namespace: 'ns', sourceName: 'db1', source: { instanceUID: 'u1' }
    };
    const older = {
      namespace: 'ns', sourceName: 'db1', source: { instanceUID: 'u0' }
    };

    expect(make({ ctx: { getters: { all: () => [mine, older] } } }).snapshots).toEqual([mine]);
  });
});

describe('OS image (repave)', () => {
  const drift = (status, reason, message = 'm') => ({
    type: 'ImageDrift', status, reason, message
  });

  it('maps ImageDrift to an image status', () => {
    expect(make({ status: { conditions: [drift('False', 'ImageUpToDate')] } }).imageStatus).toBe('upToDate');
    expect(make({ status: { conditions: [drift('True', 'OSUpdateAvailable')] } }).imageStatus).toBe('osUpdate');
    expect(make({ status: { conditions: [drift('True', 'EngineVersionEOL')] } }).imageStatus).toBe('eol');
    expect(make().imageStatus).toBe('unknown');
  });

  it('is requested until the operator records the trigger', () => {
    const conditions = [drift('True', 'OSUpdateAvailable')];

    expect(make({ metadata: { annotations: { [TRIGGER]: 't2' } }, status: { conditions, lastAppliedRepaveTrigger: 't1' } }).imageStatus).toBe('requested');
    expect(make({ status: { conditions: [...conditions, { type: 'RepaveInProgress', status: 'True' }] } }).imageStatus).toBe('updating');
  });

  it('can be applied only for an OS update on an Available instance', () => {
    const conditions = [drift('True', 'OSUpdateAvailable')];

    expect(make({ status: { conditions } }).canApplyOSUpdate).toBe(true);
    expect(make({ status: { conditions, phase: 'stopped' } }).applyOSUpdateBlockedReason).toBe('dbaas.instance.image.blocked.notAvailable');
  });

  it('summarises the drift without the kubectl hint', () => {
    const msg = 'VM is on image revision "a"; revision "b" available — annotate with x=now to repave';

    expect(make({ status: { currentImageRevision: 'a', conditions: [drift('True', 'OSUpdateAvailable', msg)] } }).imageDriftSummary)
      .toBe('dbaas.instance.image.summary.osUpdate:{"current":"a","target":"b"}');
  });

  it('requests a repave with a merge patch and a fresh timestamp', async() => {
    const m = make();

    m.patch = jest.fn(() => Promise.resolve());
    await m.requestRepave();

    const [body, opt] = m.patch.mock.calls[0];

    expect(Object.keys(body.metadata.annotations)).toEqual([TRIGGER]);
    expect(opt.headers['content-type']).toBe('application/merge-patch+json');
  });
});
