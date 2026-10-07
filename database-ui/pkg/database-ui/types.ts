// Global product: the DBaaS entry in the left rail, listing clusters that serve DBaaS
export const MANAGER_PRODUCT_NAME = 'dbaasManager';

// Per-cluster product holding the DBaaS resource pages (/c/<cluster>/dbaas/...)
export const PRODUCT_NAME = 'dbaas';

// Rancher's placeholder cluster ID for routes that are not scoped to a cluster
export const BLANK_CLUSTER = '_';

export const MANAGER_CLUSTERS_PAGE = 'dbaas-clusters';
export const MANAGER_CLUSTERS_ROUTE = `${ MANAGER_PRODUCT_NAME }-c-cluster-clusters`;

// Steve type IDs for the DBaaS operator CRDs (dbaas.opencloud.wso2.com/v1alpha1)
export const DBAAS = { INSTANCE: 'dbaas.opencloud.wso2.com.dbinstance' };

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
