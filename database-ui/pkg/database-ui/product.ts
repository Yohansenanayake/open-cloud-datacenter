import { Store } from 'vuex';
import { DSLReturnType, IPlugin } from '@shell/core/types';
import { MANAGEMENT } from '@shell/config/types';
import { IF_HAVE } from '@shell/store/type-map';
import {
  BLANK_CLUSTER, DBAAS, HARVESTER_IMAGE, IMAGES_PAGE, IMAGES_ROUTE, MANAGER_CLUSTERS_PAGE, MANAGER_CLUSTERS_ROUTE, MANAGER_PRODUCT_NAME, PRODUCT_NAME
} from './types';

type ProductOptions = Parameters<DSLReturnType['product']>[0];

export function init($plugin: IPlugin, store: Store<unknown>) {
  initManager($plugin, store);
  initCluster($plugin, store);
}

// DBaaS entry in the left rail (like Virtualization Management). Its page lists the
// Harvester clusters that serve the DBInstance type for the current user.
function initManager($plugin: IPlugin, store: Store<unknown>) {
  const { product, virtualType, basicType } = $plugin.DSL(store, MANAGER_PRODUCT_NAME);
  const to = {
    name:   MANAGER_CLUSTERS_ROUTE,
    params: { product: MANAGER_PRODUCT_NAME, cluster: BLANK_CLUSTER },
  };

  product({
    inStore:             'management',
    ifHaveType:          MANAGEMENT.CLUSTER,
    icon:                'datastore',
    // Continuous Delivery uses the default weight (1); sort is ascending
    weight:              2,
    removable:           false,
    showClusterSwitcher: false,
    to,
  });

  virtualType({
    labelKey:   'dbaas.manager.clusters.label',
    name:       MANAGER_CLUSTERS_PAGE,
    namespaced: false,
    route:      to,
  });

  basicType([MANAGER_CLUSTERS_PAGE]);
}

// DBaaS resource pages for one cluster. rootProduct gives these pages their own side
// nav instead of nesting them in Cluster Explorer's. Rancher 2.15's DSL supports it,
// but Shell's TypeMapProduct type does not declare it, hence the type assertion.
function initCluster($plugin: IPlugin, store: Store<unknown>) {
  const {
    product, basicType, virtualType, weightType
  } = $plugin.DSL(store, PRODUCT_NAME);

  product({
    inStore:             'cluster',
    rootProduct:         PRODUCT_NAME,
    ifHaveType:          DBAAS.INSTANCE,
    icon:                'datastore',
    showNamespaceFilter: true,
    to:                  {
      name:   'c-cluster-product-resource',
      params: { product: PRODUCT_NAME, resource: DBAAS.INSTANCE },
    },
  } as ProductOptions);

  // Baked VM images for the operator: Rancher admins only
  virtualType({
    labelKey:   'dbaas.images.title',
    name:       IMAGES_PAGE,
    namespaced: false,
    ifHave:     IF_HAVE.ADMIN,
    ifHaveType: HARVESTER_IMAGE,
    route:      { name: IMAGES_ROUTE, params: { product: PRODUCT_NAME } },
  });

  basicType([DBAAS.INSTANCE, DBAAS.SNAPSHOT, DBAAS.RESTORE, IMAGES_PAGE]);
  weightType(DBAAS.INSTANCE, 100, true);
  weightType(DBAAS.SNAPSHOT, 90, true);
  weightType(DBAAS.RESTORE, 80, true);
  weightType(IMAGES_PAGE, 70, true);
}
