// Client-side checks for the DBInstance form. They mirror the CRD's own
// validation (pattern, CEL rules) so problems show up next to the field instead
// of as an API error on save. Each validator returns an i18n key (plus args) for
// the problem, or undefined when the value is fine.

// The operator names the metrics Service "pg-<name>-metrics", and Service names
// are DNS labels (max 63), so the instance name can use at most 52 characters.
// The operator does not check this itself (yohan-docs known-gaps
// instance-name-length-not-validated.md in the operator repo).
export const MAX_INSTANCE_NAME_LENGTH = 52;

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const PG_IDENTIFIER = /^[a-z_][a-z0-9_]{0,62}$/;
const RESERVED_DB_NAMES = ['postgres', 'template0', 'template1'];
const RESERVED_USERNAMES = ['postgres', 'postgres_exporter'];
const UTC_WINDOW = /^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$/;

const isEmpty = (val) => val === undefined || val === null || val === '';

export function validateInstanceName(name) {
  if (isEmpty(name)) {
    return { key: 'dbaas.instance.validation.required' };
  }
  if (name.length > MAX_INSTANCE_NAME_LENGTH) {
    return { key: 'dbaas.instance.validation.nameTooLong', args: { max: MAX_INSTANCE_NAME_LENGTH } };
  }
  if (!DNS_LABEL.test(name)) {
    return { key: 'dbaas.instance.validation.dnsLabel' };
  }
}

export function validateRequired(val) {
  if (isEmpty(val)) {
    return { key: 'dbaas.instance.validation.required' };
  }
}

// min is the current size when editing (storage can only grow)
export function validateStorage(val, min = 1) {
  if (isEmpty(val)) {
    return { key: 'dbaas.instance.validation.required' };
  }
  if (!Number.isInteger(Number(val)) || Number(val) < min) {
    return { key: 'dbaas.instance.validation.storageMin', args: { min } };
  }
}

export function validateDBName(val) {
  if (isEmpty(val)) {
    return;
  }
  if (!PG_IDENTIFIER.test(val)) {
    return { key: 'dbaas.instance.validation.pgIdentifier' };
  }
  if (RESERVED_DB_NAMES.includes(val)) {
    return { key: 'dbaas.instance.validation.reservedDBName', args: { names: RESERVED_DB_NAMES.join(', ') } };
  }
}

export function validateMasterUsername(val) {
  if (isEmpty(val)) {
    return;
  }
  if (!PG_IDENTIFIER.test(val)) {
    return { key: 'dbaas.instance.validation.pgIdentifier' };
  }
  if (RESERVED_USERNAMES.includes(val) || val.startsWith('pg_')) {
    return { key: 'dbaas.instance.validation.reservedUsername' };
  }
}

export function validatePort(val) {
  if (isEmpty(val)) {
    return;
  }
  const port = Number(val);

  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    return { key: 'dbaas.instance.validation.port' };
  }
}

export function validateBackupWindow(val) {
  if (!isEmpty(val) && !UTC_WINDOW.test(val)) {
    return { key: 'dbaas.instance.validation.backupWindow' };
  }
}

export function validateRetainCount(val) {
  if (isEmpty(val)) {
    return;
  }
  if (!Number.isInteger(Number(val)) || Number(val) < 1) {
    return { key: 'dbaas.instance.validation.retainCount' };
  }
}

// Same rule as the operator's DefaultDBName (api/v1alpha1/defaults.go): the
// database created when spec.dbName is left empty.
export function defaultDBName(instanceName = '') {
  let name = instanceName.toLowerCase().replace(/[^a-z0-9_]/g, '_');

  if (!name || /^[0-9]/.test(name)) {
    name = `db_${ name }`;
  }
  name = name.slice(0, 63);
  if (RESERVED_DB_NAMES.includes(name)) {
    name = `db_${ name }`;
  }

  return name;
}
