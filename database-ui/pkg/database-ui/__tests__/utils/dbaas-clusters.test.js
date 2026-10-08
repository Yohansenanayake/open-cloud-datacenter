import { CLUSTER_STATUS, checkClusterForDBaaS, fetchHarvesterVersion } from '../../utils/dbaas-clusters';

const storeWith = (impl) => ({ dispatch: jest.fn(impl) });
const fail = (props) => () => Promise.reject({ ...props }); // eslint-disable-line prefer-promise-reject-errors

describe('checking a cluster for DBaaS', () => {
  it('asks for the DBInstance schema through the cluster proxy', async() => {
    const store = storeWith(() => Promise.resolve({ attributes: { version: 'v1alpha1' } }));

    expect(await checkClusterForDBaaS(store, 'c-abc12')).toEqual({ status: CLUSTER_STATUS.AVAILABLE, apiVersion: 'v1alpha1' });

    const [action, opt] = store.dispatch.mock.calls[0];

    expect(action).toBe('management/request');
    expect(opt.url).toBe('/k8s/clusters/c-abc12/v1/schemas/dbaas.opencloud.wso2.com.dbinstance');
    expect(opt.redirectUnauthorized).toBe(false);
  });

  it('treats 404 and 403 as absent, other failures as errors', async() => {
    expect((await checkClusterForDBaaS(storeWith(fail({ _status: 404 })), 'a')).status).toBe('absent');
    expect((await checkClusterForDBaaS(storeWith(fail({ _status: 403 })), 'a')).status).toBe('absent');
    expect(await checkClusterForDBaaS(storeWith(fail({ _status: 503 })), 'a')).toEqual({ status: 'error', message: 'HTTP 503' });
  });

  it('reads the Harvester version, empty if not allowed', async() => {
    expect(await fetchHarvesterVersion(storeWith(() => Promise.resolve({ value: 'v1.9.0' })), 'a')).toBe('v1.9.0');
    expect(await fetchHarvesterVersion(storeWith(fail({ _status: 403 })), 'a')).toBe('');
  });
});
