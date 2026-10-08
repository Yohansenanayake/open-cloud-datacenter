import { dbInstanceEventSource as source } from '../../utils/dbinstance-events';

const instance = {
  metadata: {
    name: 'test-delete', namespace: 'ns', uid: 'u-1', creationTimestamp: '2026-10-07T22:17:00Z'
  },
  status: {
    resources: {
      vmName: 'pg-test-delete', dataVolumeName: 'pg-test-delete-9f59058e-data', osDiskPVCName: 'pg-test-delete-9f59058e-os'
    }
  },
};
const event = (kind, name, { uid, at = '2026-10-07T22:18:00Z', ns = 'ns' } = {}) => ({
  metadata:       { namespace: ns },
  involvedObject: {
    kind, name, uid
  },
  lastTimestamp: at
});

describe('which events belong to an instance', () => {
  it('matches the instance by UID', () => {
    expect(source(event('DBInstance', 'test-delete', { uid: 'u-1' }), instance)).toBe('instance');
    expect(source(event('DBInstance', 'test-delete', { uid: 'old' }), instance)).toBeNull();
  });

  it('matches the VM, launcher pod and disks', () => {
    expect(source(event('VirtualMachineInstance', 'pg-test-delete'), instance)).toBe('vm');
    expect(source(event('Pod', 'virt-launcher-pg-test-delete-plp2f'), instance)).toBe('pod');
    expect(source(event('PersistentVolumeClaim', 'pg-test-delete-9f59058e-os'), instance)).toBe('disk');
  });

  it('rejects look-alikes, other namespaces and events older than the instance', () => {
    const shortVm = { ...instance, status: { resources: { vmName: 'pg-test' } } };

    expect(source(event('Pod', 'virt-launcher-pg-test-delete-plp2f'), shortVm)).toBeNull();
    expect(source(event('VirtualMachine', 'pg-test-delete', { ns: 'other' }), instance)).toBeNull();
    expect(source(event('VirtualMachine', 'pg-test-delete', { at: '2026-10-01T00:00:00Z' }), instance)).toBeNull();
  });
});
