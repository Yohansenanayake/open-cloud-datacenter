import * as v from '../../utils/dbinstance-validation';

const key = (r) => r?.key;

describe('defaultDBName matches the operator DefaultDBName (api/v1alpha1/defaults_test.go)', () => {
  const table = {
    orders:                                                                   'orders',
    'orders-db':                                                              'orders_db',
    'rt-src-1791115069':                                                      'rt_src_1791115069',
    'a.b.c':                                                                  'a_b_c',
    '9lives':                                                                 'db_9lives',
    postgres:                                                                 'db_postgres',
    template1:                                                                'db_template1',
    'x123456789-123456789-123456789-123456789-123456789-123456789-123456789': 'x123456789_123456789_123456789_123456789_123456789_123456789_12',
  };

  Object.entries(table).forEach(([input, want]) => {
    it(input, () => expect(v.defaultDBName(input)).toBe(want));
  });
});

describe('validators', () => {
  it('instance name: DNS label, at most 52 characters', () => {
    expect(v.validateInstanceName('orders-db')).toBeUndefined();
    expect(key(v.validateInstanceName(''))).toBe('dbaas.instance.validation.required');
    expect(key(v.validateInstanceName('Orders'))).toBe('dbaas.instance.validation.dnsLabel');
    expect(key(v.validateInstanceName('a.b'))).toBe('dbaas.instance.validation.dnsLabel');
    expect(v.validateInstanceName('a'.repeat(52))).toBeUndefined();
    expect(key(v.validateInstanceName('a'.repeat(53)))).toBe('dbaas.instance.validation.nameTooLong');
  });

  it('storage can only grow', () => {
    expect(v.validateStorage(20)).toBeUndefined();
    expect(key(v.validateStorage(19, 20))).toBe('dbaas.instance.validation.storageMin');
    expect(v.validateStorage(25, 20)).toBeUndefined();
    expect(key(v.validateStorage(1.5))).toBe('dbaas.instance.validation.storageMin');
  });

  it('dbName and masterUsername follow the CRD pattern and reserved names', () => {
    expect(v.validateDBName('')).toBeUndefined();
    expect(key(v.validateDBName('app-db'))).toBe('dbaas.instance.validation.pgIdentifier');
    expect(key(v.validateDBName('template0'))).toBe('dbaas.instance.validation.reservedDBName');
    expect(v.validateMasterUsername('dbadmin')).toBeUndefined();
    expect(key(v.validateMasterUsername('postgres_exporter'))).toBe('dbaas.instance.validation.reservedUsername');
    expect(key(v.validateMasterUsername('pg_admin'))).toBe('dbaas.instance.validation.reservedUsername');
  });

  it('port, backup window and retain count', () => {
    expect(key(v.validatePort(70000))).toBe('dbaas.instance.validation.port');
    expect(v.validateBackupWindow('02:00-03:00')).toBeUndefined();
    expect(key(v.validateBackupWindow('24:00-01:00'))).toBe('dbaas.instance.validation.backupWindow');
    expect(key(v.validateRetainCount(0))).toBe('dbaas.instance.validation.retainCount');
  });
});
