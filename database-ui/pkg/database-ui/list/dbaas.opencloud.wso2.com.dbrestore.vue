<script>
import ResourceTable from '@shell/components/ResourceTable';
import { AGE, NAME, NAMESPACE, STATE } from '@shell/config/table-headers';
import { DBAAS } from '../types';

export const DBRESTORE_HEADERS = [
  { ...STATE, formatter: 'DBInstanceState' },
  NAME,
  NAMESPACE,
  {
    name:          'snapshot',
    labelKey:      'dbaas.restore.tableHeaders.snapshot',
    value:         'snapshotName',
    sort:          ['snapshotName', 'nameSort'],
    formatter:     'DBaaSRelatedLink',
    formatterOpts: { link: 'snapshot', note: 'snapshotMissingNote' },
  },
  {
    name:          'source',
    labelKey:      'dbaas.snapshot.tableHeaders.source',
    value:         'sourceInstanceName',
    sort:          ['sourceInstanceName', 'nameSort'],
    formatter:     'DBaaSRelatedLink',
    formatterOpts: { link: 'sourceInstance', note: 'sourceMissingNote' },
  },
  {
    name:          'target',
    labelKey:      'dbaas.restore.tableHeaders.target',
    value:         'targetName',
    sort:          ['targetName', 'nameSort'],
    formatter:     'DBaaSRelatedLink',
    formatterOpts: { link: 'targetInstance', note: 'targetMissingNote' },
  },
  AGE,
];

// Restores list. Rows come from Shell's ResourceList; snapshots and instances
// are loaded too, for the related-object links.
export default {
  name:       'ListDBRestore',
  components: { ResourceTable },

  props: {
    schema: {
      type:     Object,
      required: true,
    },

    rows: {
      type:     Array,
      required: true,
    },

    loading: {
      type:    Boolean,
      default: false,
    },

    useQueryParamsForSimpleFiltering: {
      type:    Boolean,
      default: false
    }
  },

  data() {
    return { headers: DBRESTORE_HEADERS };
  },

  created() {
    const inStore = this.$store.getters['currentProduct']?.inStore || 'cluster';

    [DBAAS.SNAPSHOT, DBAAS.INSTANCE].forEach((type) => {
      if (this.$store.getters[`${ inStore }/canList`](type)) {
        this.$store.dispatch(`${ inStore }/findAll`, { type });
      }
    });
  },
};
</script>

<template>
  <ResourceTable
    :schema="schema"
    :rows="rows"
    :headers="headers"
    :loading="loading"
    :use-query-params-for-simple-filtering="useQueryParamsForSimpleFiltering"
    default-sort-by="age"
    no-rows-key="dbaas.restore.list.noRows"
  />
</template>
