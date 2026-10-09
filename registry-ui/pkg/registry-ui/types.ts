// Global product: the Registries entry in the left rail, listing clusters that serve Registries
export const MANAGER_PRODUCT_NAME = 'registryManager';

// Per-cluster product holding the Registry pages (/c/<cluster>/registries/...)
export const PRODUCT_NAME = 'registries';

// Rancher's placeholder cluster ID for routes that are not scoped to a cluster
export const BLANK_CLUSTER = '_';

export const MANAGER_CLUSTERS_PAGE = 'registry-clusters';
export const MANAGER_CLUSTERS_ROUTE = `${ MANAGER_PRODUCT_NAME }-c-cluster-clusters`;

// Steve type ID of the operator's CRD (registry.opencloud.wso2.com/v1alpha1)
export const REGISTRY = 'registry.opencloud.wso2.com.registry';

// Per-cluster overview page, the first page of the Registries pages
export const OVERVIEW_PAGE = 'registries-overview';
export const OVERVIEW_ROUTE = `${ PRODUCT_NAME }-c-cluster-overview`;

// Where the operator runs, and the Harvester Addon that installs it
export const OPERATOR_NAMESPACE = 'registry-system';
export const OPERATOR_SELECTOR = 'app.kubernetes.io/name=registry-operator,control-plane=controller-manager';
export const ADDON = 'harvesterhci.io.addon';
export const ADDON_ID = 'registry-system/registry-operator';

// status.phase values set by the operator (api/v1alpha1/registry_types.go)
export const REGISTRY_PHASE = {
  PROVISIONING: 'Provisioning',
  READY:        'Ready',
  FAILED:       'Failed',
  TERMINATING:  'Terminating',
};

// Storage quota of each plan, in GiB. Mirrors the operator's plan table.
export const PLAN_QUOTA_GIB = {
  starter:      5,
  professional: 20,
  enterprise:   100,
};
