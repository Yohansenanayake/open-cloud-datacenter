import RegistryClusters from '../pages/RegistryClusters.vue';
import Overview from '../pages/overview.vue';
import {
  BLANK_CLUSTER, MANAGER_CLUSTERS_ROUTE, MANAGER_PRODUCT_NAME, OVERVIEW_ROUTE, PRODUCT_NAME
} from '../types';

// Registry list, detail and edit pages use Shell's built-in c-cluster-product-resource
// routes. Only the rail entry's landing page and the per-cluster overview need routes.
const routes = [
  {
    parent: 'default',
    route:  {
      name:      MANAGER_CLUSTERS_ROUTE,
      path:      `/${ MANAGER_PRODUCT_NAME }/c/:cluster/clusters`,
      component: RegistryClusters,
      meta:      { product: MANAGER_PRODUCT_NAME, cluster: BLANK_CLUSTER },
    },
  },
  {
    parent: 'default',
    route:  {
      name:      OVERVIEW_ROUTE,
      path:      `/c/:cluster/${ PRODUCT_NAME }/overview`,
      component: Overview,
      meta:      { product: PRODUCT_NAME },
    },
  },
];

export default routes;
