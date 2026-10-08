import { DBAAS, PRODUCT_NAME, RESTORE_STAGE } from '../types';

// The steps shown on the restore progress page, in order
export const RESTORE_STEPS = [
  RESTORE_STAGE.PREPARING,
  RESTORE_STAGE.RESTORING_VOLUME,
  RESTORE_STAGE.STARTING_DATABASE,
  RESTORE_STAGE.SUCCEEDED,
];

// Route to the DBRestore create page in the given cluster
export function restoreCreateLocation(cluster, query) {
  return {
    name:   'c-cluster-product-resource-create',
    params: {
      cluster, product: PRODUCT_NAME, resource: DBAAS.RESTORE
    },
    query,
  };
}
