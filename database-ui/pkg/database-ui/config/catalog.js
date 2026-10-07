// Choices offered by the DBInstance form. The operator does not publish these
// through its API, so they mirror its built-in defaults and must be kept in sync:
//
// - Instance classes: api/v1alpha1/dbinstance_types.go (built-in class table).
//   Operator config (`instanceClasses`) can replace the table at runtime, so the
//   form also accepts a class typed by hand; preflight rejects unknown ones with
//   reason InvalidClass.
// - Engine versions: internal/catalog/baked_images.go, for the default OS stream
//   (databaseDefaults.osVersion "22.04" → PostgreSQL 15, 16, 17; default 17).
//   Leaving the version unset lets the operator pick the image's default.

export const INSTANCE_CLASSES = [
  { name: 'db.t3.micro', cpu: 1, memoryGiB: 1 },
  { name: 'db.t3.small', cpu: 1, memoryGiB: 2 },
  { name: 'db.t3.medium', cpu: 2, memoryGiB: 4 },
  { name: 'db.t3.large', cpu: 2, memoryGiB: 8 },
  { name: 'db.t3.xlarge', cpu: 4, memoryGiB: 16 },
  { name: 'db.m5.large', cpu: 2, memoryGiB: 8 },
  { name: 'db.m5.xlarge', cpu: 4, memoryGiB: 16 },
  { name: 'db.m5.2xlarge', cpu: 8, memoryGiB: 32 },
  { name: 'db.m5.4xlarge', cpu: 16, memoryGiB: 64 },
  { name: 'db.r5.large', cpu: 2, memoryGiB: 16 },
  { name: 'db.r5.xlarge', cpu: 4, memoryGiB: 32 },
  { name: 'db.r5.2xlarge', cpu: 8, memoryGiB: 64 },
];

export const ENGINE_VERSIONS = ['17', '16', '15'];

// Create-time defaults (the operator's sample uses db.t3.medium)
export const DEFAULT_INSTANCE_CLASS = 'db.t3.medium';
export const DEFAULT_ALLOCATED_STORAGE_GIB = 20;

// Shown as placeholders; the operator applies these when the field is left empty
export const OPERATOR_DEFAULTS = {
  masterUsername: 'dbadmin',
  port:           5432,
};

// spec.backup.automated defaults from the CRD schema
export const BACKUP_DEFAULTS = {
  enabled:            true,
  preferredWindowUTC: '02:00-03:00',
  retainCount:        7,
};
