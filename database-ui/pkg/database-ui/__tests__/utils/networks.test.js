import { databaseNetworkOptions, isDatabaseNetwork } from '../../utils/networks';

const network = (id, { type, storage } = {}) => ({
  id,
  metadata: {
    labels:      type ? { 'network.harvesterhci.io/type': type } : {},
    annotations: storage ? { 'storage-network.settings.harvesterhci.io': 'true' } : {},
  },
});

describe('database networks', () => {
  it('offers VLAN networks only', () => {
    expect(isDatabaseNetwork(network('default/vlan', { type: 'L2VlanNetwork' }))).toBe(true);
    expect(isDatabaseNetwork(network('default/plain'))).toBe(true);
    expect(isDatabaseNetwork(network('default/ovn', { type: 'OverlayNetwork' }))).toBe(false);
    expect(isDatabaseNetwork(network('harvester-system/storage', { storage: true }))).toBe(false);
  });

  it('options are sorted by namespace/name', () => {
    const options = databaseNetworkOptions([
      network('default/vlan-b'), network('default/ovn', { type: 'OverlayNetwork' }), network('default/vlan-a'),
    ]);

    expect(options).toEqual([
      { label: 'default/vlan-a', value: 'default/vlan-a' },
      { label: 'default/vlan-b', value: 'default/vlan-b' },
    ]);
  });
});
