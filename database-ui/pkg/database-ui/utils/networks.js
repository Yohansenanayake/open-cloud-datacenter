const NETWORK_TYPE_LABEL = 'network.harvesterhci.io/type';
const OVERLAY_NETWORK = 'OverlayNetwork';
const STORAGE_NETWORK_ANNOTATION = 'storage-network.settings.harvesterhci.io';

// Harvester networks a database VM can attach to: VLAN networks only. The
// storage network is Harvester's own, and overlay (Kube-OVN) networks are not
// supported (they need DNS servers set, which the operator only takes
// through spec.staticNetwork).
export function isDatabaseNetwork(network) {
  return !network.metadata?.annotations?.[STORAGE_NETWORK_ANNOTATION] &&
    network.metadata?.labels?.[NETWORK_TYPE_LABEL] !== OVERLAY_NETWORK;
}

export function databaseNetworkOptions(networks) {
  return networks
    .filter(isDatabaseNetwork)
    .map((n) => ({ label: n.id, value: n.id }))
    .sort((a, b) => a.label.localeCompare(b.label));
}
