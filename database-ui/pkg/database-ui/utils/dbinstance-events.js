// Which Kubernetes Events belong to a DBInstance: the instance itself (the
// operator records its progress there) and the objects the operator creates
// for it, as named in status.resources. Matching on kind as well as name keeps
// unrelated objects that happen to share a name out.

export const EVENT_SOURCE = {
  INSTANCE: 'instance',
  VM:       'vm',
  POD:      'pod',
  DISK:     'disk',
};

// KubeVirt names the VM's pod "virt-launcher-<vm>-<5 random chars>". Matching
// the exact suffix stops VM "pg-a" from claiming the pods of VM "pg-a-b".
function isLauncherPodOf(podName, vmName) {
  const prefix = `virt-launcher-${ vmName }-`;

  return podName.startsWith(prefix) && /^[a-z0-9]{5}$/.test(podName.slice(prefix.length));
}

/**
 * @returns {string|null} the EVENT_SOURCE the event belongs to, or null if it is
 * not about this instance
 */
export function dbInstanceEventSource(event, instance) {
  const obj = event?.involvedObject || {};
  const namespace = event?.metadata?.namespace || obj.namespace;

  if (!obj.name || namespace !== instance?.metadata?.namespace) {
    return null;
  }

  if (obj.kind === 'DBInstance') {
    const uid = instance.metadata?.uid;

    return (obj.uid && uid ? obj.uid === uid : obj.name === instance.metadata?.name) ? EVENT_SOURCE.INSTANCE : null;
  }

  // Child objects are matched by name, so skip events from before this instance
  // existed (a deleted instance of the same name)
  const created = instance.metadata?.creationTimestamp;
  const when = event.lastTimestamp || event.eventTime || event.metadata?.creationTimestamp;

  if (created && when && when < created) {
    return null;
  }

  const resources = instance.status?.resources || {};
  const vmName = resources.vmName;

  if (vmName && (obj.kind === 'VirtualMachine' || obj.kind === 'VirtualMachineInstance') && obj.name === vmName) {
    return EVENT_SOURCE.VM;
  }
  if (vmName && obj.kind === 'Pod' && isLauncherPodOf(obj.name, vmName)) {
    return EVENT_SOURCE.POD;
  }
  if (obj.kind === 'PersistentVolumeClaim' && [resources.dataVolumeName, resources.osDiskPVCName].includes(obj.name)) {
    return EVENT_SOURCE.DISK;
  }

  return null;
}
