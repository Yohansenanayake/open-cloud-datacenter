import DBaaSClusters from '../pages/DBaaSClusters.vue';
import {
  BLANK_CLUSTER, MANAGER_CLUSTERS_ROUTE, MANAGER_PRODUCT_NAME
} from '../types';

// Per-cluster DBaaS pages use Shell's built-in c-cluster-product-resource routes,
// so only the rail entry's landing page needs a route of its own.
const routes = [
  {
    parent: 'default',
    route:  {
      name:      MANAGER_CLUSTERS_ROUTE,
      path:      `/${ MANAGER_PRODUCT_NAME }/c/:cluster/clusters`,
      component: DBaaSClusters,
      meta:      { product: MANAGER_PRODUCT_NAME, cluster: BLANK_CLUSTER },
    },
  },
];

export default routes;
