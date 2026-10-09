import Registry from '../../models/registry.opencloud.wso2.com.registry';

jest.mock('@shell/plugins/steve/steve-class', () => require('../helpers/steve-class-mock'));

const make = (spec, status = {}) => new Registry({
  metadata: { name: 'web', namespace: 'team-a' },
  spec,
  status,
});

describe('Registry', () => {
  it('defaults to the starter plan and its quota', () => {
    const r = make(undefined);

    expect(r.plan).toBe('starter');
    expect(r.quotaGiB).toBe(5);
    expect(r.quotaDisplay).toBe('5 GiB');
  });

  it('maps each plan to its quota', () => {
    expect(make({ plan: 'professional' }).quotaGiB).toBe(20);
    expect(make({ plan: 'enterprise' }).quotaGiB).toBe(100);
    expect(make({ plan: 'unknown' }).quotaGiB).toBe(0);
  });

  it('reports Pending until the operator sets a phase', () => {
    expect(make({ plan: 'starter' }).phase).toBe('Pending');
    expect(make({ plan: 'starter' }).isReady).toBe(false);
    expect(make({ plan: 'starter' }, { phase: 'Ready' }).isReady).toBe(true);
  });

  it('derives the image host from the registry URL', () => {
    expect(make({}, { registryURL: 'https://harbor.example.com' }).registryHost).toBe('harbor.example.com');
    expect(make({}, { registryURL: 'https://harbor.example.com/' }).registryHost).toBe('harbor.example.com');
    expect(make({}).registryHost).toBe('');
  });

  it('fills in a spec when created without one', () => {
    const r = make(undefined);

    r.applyDefaults();
    expect(r.spec).toEqual({ plan: 'starter' });
  });
});
