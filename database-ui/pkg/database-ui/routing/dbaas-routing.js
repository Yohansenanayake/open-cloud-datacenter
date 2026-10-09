import DBaaSClusters from '../pages/DBaaSClusters.vue';
import BakedImages from '../pages/BakedImages.vue';
import BakedImageCreate from '../pages/BakedImageCreate.vue';
import BakedImageDetail from '../pages/BakedImageDetail.vue';
import {
  BLANK_CLUSTER, IMAGES_CREATE_ROUTE, IMAGES_DETAIL_ROUTE, IMAGES_ROUTE, MANAGER_CLUSTERS_ROUTE, MANAGER_PRODUCT_NAME, PRODUCT_NAME
} from '../types';

// DBaaS resource pages use Shell's built-in c-cluster-product-resource routes.
// Only the rail entry's landing page and the database-images pages (which show
// Harvester images, not a DBaaS resource) need routes of their own.
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
  // Database images (admin only), inside a cluster's DBaaS pages
  {
    parent: 'default',
    route:  {
      name:      IMAGES_ROUTE,
      path:      `/c/:cluster/${ PRODUCT_NAME }/database-images`,
      component: BakedImages,
      meta:      { product: PRODUCT_NAME },
    },
  },
  {
    parent: 'default',
    route:  {
      name:      IMAGES_CREATE_ROUTE,
      path:      `/c/:cluster/${ PRODUCT_NAME }/database-images/create`,
      component: BakedImageCreate,
      meta:      { product: PRODUCT_NAME },
    },
  },
  {
    parent: 'default',
    route:  {
      name:      IMAGES_DETAIL_ROUTE,
      path:      `/c/:cluster/${ PRODUCT_NAME }/database-images/:namespace/:id`,
      component: BakedImageDetail,
      meta:      { product: PRODUCT_NAME },
    },
  },
];

export default routes;
