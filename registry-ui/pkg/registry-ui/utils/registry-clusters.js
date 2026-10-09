import { REGISTRY } from '../types';

export const CLUSTER_STATUS = {
  AVAILABLE:   'available',
  // CRD not installed, or the user may not see it. Steve does not tell these apart.
  ABSENT:      'absent',
  // Rancher reports the cluster as not connected/ready, so it was not checked
  UNAVAILABLE: 'unavailable',
  ERROR:       'error',
};

const REQUEST_TIMEOUT_MS = 15000;

// Cluster actions offered in the cluster list's menu. Cluster lifecycle actions
// (edit, delete, ...) stay in Virtualization/Cluster Management.
export const CLUSTER_MENU_ACTIONS = ['openShell', 'downloadKubeConfig', 'copyKubeConfig'];

function requestFromCluster(store, clusterId, path) {
  return store.dispatch('management/request', {
    url:                  `/k8s/clusters/${ encodeURIComponent(clusterId) }/v1/${ path }`,
    method:               'get',
    timeout:              REQUEST_TIMEOUT_MS,
    // A downstream 401 must not log the user out of Rancher
    redirectUnauthorized: false,
  });
}

/**
 * Ask a downstream cluster (through Rancher's proxy) whether it serves the Registry
 * type for the current user. Steve only returns schemas the user can access, so this
 * covers both "operator installed" and "user has access".
 *
 * @returns {Promise<{ status: string, apiVersion?: string, message?: string }>}
 */
export async function checkClusterForRegistry(store, clusterId) {
  try {
    const schema = await requestFromCluster(store, clusterId, `schemas/${ REGISTRY }`);

    return { status: CLUSTER_STATUS.AVAILABLE, apiVersion: schema?.attributes?.version || '' };
  } catch (err) {
    if (err?._status === 404 || err?._status === 403) {
      return { status: CLUSTER_STATUS.ABSENT };
    }

    return {
      status:  CLUSTER_STATUS.ERROR,
      message: err?.message || err?._statusText || (err?._status ? `HTTP ${ err._status }` : ''),
    };
  }
}

/**
 * Harvester version from its server-version setting (the same source Virtualization
 * Management uses). Returns '' if the user may not read Harvester settings.
 */
export async function fetchHarvesterVersion(store, clusterId) {
  try {
    const setting = await requestFromCluster(store, clusterId, 'harvesterhci.io.settings/server-version');

    return setting?.value || '';
  } catch {
    return '';
  }
}
