// Global product: the DBaaS entry in the left rail, listing clusters that serve DBaaS
export const MANAGER_PRODUCT_NAME = 'dbaasManager';

// Per-cluster product holding the DBaaS resource pages (/c/<cluster>/dbaas/...)
export const PRODUCT_NAME = 'dbaas';

// Rancher's placeholder cluster ID for routes that are not scoped to a cluster
export const BLANK_CLUSTER = '_';

export const MANAGER_CLUSTERS_PAGE = 'dbaas-clusters';
export const MANAGER_CLUSTERS_ROUTE = `${ MANAGER_PRODUCT_NAME }-c-cluster-clusters`;

// Steve type IDs for the DBaaS operator CRDs (dbaas.opencloud.wso2.com/v1alpha1)
export const DBAAS = {
  INSTANCE: 'dbaas.opencloud.wso2.com.dbinstance',
  SNAPSHOT: 'dbaas.opencloud.wso2.com.dbsnapshot',
  RESTORE:  'dbaas.opencloud.wso2.com.dbrestore',
};

// status.stage values of a DBRestore (api/v1alpha1/dbrestore_types.go)
export const RESTORE_STAGE = {
  PREPARING:         'Preparing',
  RESTORING_VOLUME:  'RestoringVolume',
  STARTING_DATABASE: 'StartingDatabase',
  SUCCEEDED:         'Succeeded',
  FAILED:            'Failed',
};

// Query parameters understood by the DBRestore create page
export const RESTORE_QUERY = {
  SNAPSHOT: 'snapshot', // <namespace>/<snapshot name>: preselect this snapshot
  RETRY:    'retry', //    <namespace>/<restore name>: prefill from this failed restore
};

// OS image state of a DBInstance (from its ImageDrift / RepaveInProgress conditions)
export const IMAGE_STATUS = {
  UP_TO_DATE: 'upToDate',
  OS_UPDATE:  'osUpdate',
  EOL:        'eol',
  UNKNOWN:    'unknown',
  REQUESTED:  'requested', // repave trigger set, the operator has not picked it up yet
  UPDATING:   'updating',
};

// Harvester VM images; DBaaS baked images are the ones carrying IMAGE_LABEL.BAKED
export const HARVESTER_IMAGE = 'harvesterhci.io.virtualmachineimage';

export const IMAGE_LABEL = {
  BAKED:      'dbaas.opencloud.wso2.com/baked-image',
  OS_VERSION: 'dbaas.opencloud.wso2.com/os-version',
};

// Baked-images pages (admin only) inside the per-cluster DBaaS product
export const IMAGES_PAGE = 'dbaas-baked-images';
export const IMAGES_ROUTE = `${ PRODUCT_NAME }-c-cluster-baked-images`;
export const IMAGES_CREATE_ROUTE = `${ PRODUCT_NAME }-c-cluster-baked-images-create`;
export const IMAGES_DETAIL_ROUTE = `${ PRODUCT_NAME }-c-cluster-baked-images-namespace-id`;

// status.phase values of a DBSnapshot (derived by the operator from its Ready condition)
export const SNAPSHOT_PHASE = {
  QUEUED:      'Queued',
  IN_PROGRESS: 'InProgress',
  READY:       'Ready',
  FAILED:      'Failed',
  DELETING:    'Deleting',
};

export const SNAPSHOT_ORIGIN = {
  MANUAL:    'Manual',
  AUTOMATED: 'Automated',
};

// status.phase values derived by the operator (api/v1alpha1/dbinstance_types.go)
export const DB_PHASE = {
  CREATING:                'creating',
  AVAILABLE:               'available',
  STOPPING:                'stopping',
  STOPPED:                 'stopped',
  STARTING:                'starting',
  MODIFYING:               'modifying',
  DELETING:                'deleting',
  FAILED:                  'failed',
  DEGRADED:                'degraded',
  INCOMPATIBLE_PARAMETERS: 'incompatible-parameters',
  CRASH_LOOP_HALTED:       'crash-loop-halted',
};
