// Naming and validation for manual DBSnapshots.

// The Harvester VirtualMachineBackup the operator creates for a snapshot takes
// the snapshot's name, so keep it a DNS label.
export const MAX_SNAPSHOT_NAME_LENGTH = 63;

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

// The scheduler names automated snapshots "<instance>-auto-<YYYYMMDD>". A manual
// snapshot with such a name would make the scheduler skip that day's run.
const AUTOMATED_NAME = /-auto-\d{8}$/;

const pad = (n) => `${ n }`.padStart(2, '0');

// "<instance>-manual-<YYYYMMDD-HHMM>" in UTC, trimmed to fit the length limit
export function defaultSnapshotName(instanceName, now = new Date()) {
  const stamp = `${ now.getUTCFullYear() }${ pad(now.getUTCMonth() + 1) }${ pad(now.getUTCDate()) }-${ pad(now.getUTCHours()) }${ pad(now.getUTCMinutes()) }`;
  const suffix = `-manual-${ stamp }`;
  const base = instanceName.slice(0, MAX_SNAPSHOT_NAME_LENGTH - suffix.length).replace(/-+$/, '');

  return `${ base }${ suffix }`;
}

export function validateSnapshotName(name) {
  if (!name) {
    return { key: 'dbaas.instance.validation.required' };
  }
  if (name.length > MAX_SNAPSHOT_NAME_LENGTH) {
    return { key: 'dbaas.snapshot.validation.tooLong', args: { max: MAX_SNAPSHOT_NAME_LENGTH } };
  }
  if (!DNS_LABEL.test(name)) {
    return { key: 'dbaas.instance.validation.dnsLabel' };
  }
  if (AUTOMATED_NAME.test(name)) {
    return { key: 'dbaas.snapshot.validation.automatedName' };
  }
}
