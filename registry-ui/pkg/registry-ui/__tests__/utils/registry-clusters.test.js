import { CLUSTER_STATUS, checkClusterForRegistry, fetchHarvesterVersion } from '../../utils/registry-clusters';

const storeReturning = (fn) => ({ dispatch: jest.fn((action, opts) => fn(opts)) });

// Rancher's request errors carry the HTTP status as _status and _statusText
const httpError = (status, statusText) => Object.assign(new Error(), {
  message: '', _status: status, _statusText: statusText
});

describe('checkClusterForRegistry', () => {
  it('is available when the cluster serves the Registry schema', async() => {
    const store = storeReturning(() => Promise.resolve({ attributes: { version: 'v1alpha1' } }));

    await expect(checkClusterForRegistry(store, 'c-1')).resolves.toEqual({ status: CLUSTER_STATUS.AVAILABLE, apiVersion: 'v1alpha1' });
    expect(store.dispatch).toHaveBeenCalledWith('management/request', expect.objectContaining({
      url:                  '/k8s/clusters/c-1/v1/schemas/registry.opencloud.wso2.com.registry',
      redirectUnauthorized: false,
    }));
  });

  it('is absent when the schema is missing or hidden from the user', async() => {
    for (const status of [403, 404]) {
      const store = storeReturning(() => Promise.reject(httpError(status)));

      await expect(checkClusterForRegistry(store, 'c-1')).resolves.toEqual({ status: CLUSTER_STATUS.ABSENT });
    }
  });

  it('reports other failures as an error with a message', async() => {
    const store = storeReturning(() => Promise.reject(httpError(502, 'Bad Gateway')));

    await expect(checkClusterForRegistry(store, 'c-1')).resolves.toEqual({ status: CLUSTER_STATUS.ERROR, message: 'Bad Gateway' });
  });
});

describe('fetchHarvesterVersion', () => {
  it('returns the server version, or an empty string when unreadable', async() => {
    await expect(fetchHarvesterVersion(storeReturning(() => Promise.resolve({ value: 'v1.9.0' })), 'c-1')).resolves.toBe('v1.9.0');
    await expect(fetchHarvesterVersion(storeReturning(() => Promise.reject(httpError(403))), 'c-1')).resolves.toBe('');
  });
});
