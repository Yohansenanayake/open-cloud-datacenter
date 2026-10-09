import { Store } from 'vuex';
import { DSLReturnType, IPlugin } from '@shell/core/types';
import { MANAGEMENT } from '@shell/config/types';
import { STATE, NAME as NAME_COL, NAMESPACE as NAMESPACE_COL, AGE } from '@shell/config/table-headers';
import {
  BLANK_CLUSTER, MANAGER_CLUSTERS_PAGE, MANAGER_CLUSTERS_ROUTE, MANAGER_PRODUCT_NAME,
  OVERVIEW_PAGE, OVERVIEW_ROUTE, PRODUCT_NAME, REGISTRY
} from './types';

type ProductOptions = Parameters<DSLReturnType['product']>[0];

export function init($plugin: IPlugin, store: Store<unknown>) {
  initManager($plugin, store);
  initCluster($plugin, store);
}

// Registries entry in the left rail (like Virtualization Management). Its page lists
// the Harvester clusters that serve the Registry type for the current user.
function initManager($plugin: IPlugin, store: Store<unknown>) {
  const { product, virtualType, basicType } = $plugin.DSL(store, MANAGER_PRODUCT_NAME);
  const to = {
    name:   MANAGER_CLUSTERS_ROUTE,
    params: { product: MANAGER_PRODUCT_NAME, cluster: BLANK_CLUSTER },
  };

  product({
    inStore:             'management',
    ifHaveType:          MANAGEMENT.CLUSTER,
    icon:                'storage',
    weight:              2,
    removable:           false,
    showClusterSwitcher: false,
    to,
  });

  virtualType({
    labelKey:   'registryUi.manager.clusters.label',
    name:       MANAGER_CLUSTERS_PAGE,
    namespaced: false,
    route:      to,
  });

  basicType([MANAGER_CLUSTERS_PAGE]);
}

// Registry pages for one cluster. rootProduct gives these pages their own side nav
// instead of nesting them in Cluster Explorer's, which hides Harvester clusters.
// Rancher 2.15's DSL supports it; Shell's TypeMapProduct type does not declare it.
function initCluster($plugin: IPlugin, store: Store<unknown>) {
  const {
    product, basicType, virtualType, configureType, headers, weightType
  } = $plugin.DSL(store, PRODUCT_NAME);

  product({
    inStore:             'cluster',
    rootProduct:         PRODUCT_NAME,
    ifHaveType:          REGISTRY,
    icon:                'storage',
    showNamespaceFilter: true,
    to:                  { name: OVERVIEW_ROUTE, params: { product: PRODUCT_NAME } },
  } as ProductOptions);

  virtualType({
    labelKey:   'registryUi.overview.title',
    name:       OVERVIEW_PAGE,
    namespaced: false,
    route:      { name: OVERVIEW_ROUTE, params: { product: PRODUCT_NAME } },
  });

  configureType(REGISTRY, {
    isCreatable: true,
    isEditable:  true,
    canYaml:     true,
  });

  basicType([OVERVIEW_PAGE, REGISTRY]);
  weightType(OVERVIEW_PAGE, 100, true);
  weightType(REGISTRY, 90, true);

  headers(REGISTRY, [
    STATE,
    NAME_COL,
    NAMESPACE_COL,
    {
      name: 'plan', labelKey: 'registryUi.headers.plan', value: 'spec.plan', sort: ['spec.plan'],
    },
    {
      name: 'quota', labelKey: 'registryUi.headers.quota', value: 'quotaDisplay', sort: ['quotaGiB'],
    },
    {
      name: 'project', labelKey: 'registryUi.headers.project', value: 'status.harborProject', sort: ['status.harborProject'],
    },
    {
      name: 'url', labelKey: 'registryUi.headers.url', value: 'status.registryURL', sort: ['status.registryURL'],
    },
    AGE,
  ]);
}
