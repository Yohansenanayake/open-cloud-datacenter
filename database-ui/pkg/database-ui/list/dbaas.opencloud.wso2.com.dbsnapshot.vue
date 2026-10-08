<script>
import ResourceTable from '@shell/components/ResourceTable';
import ShellSelect from '@shell/components/form/Select';
import { AGE, NAME, NAMESPACE, STATE } from '@shell/config/table-headers';
import { DBAAS, SNAPSHOT_ORIGIN } from '../types';

const ALL = 'all';

export const DBSNAPSHOT_HEADERS = [
  { ...STATE, formatter: 'DBInstanceState' },
  NAME,
  NAMESPACE,
  {
    name:      'source',
    labelKey:  'dbaas.snapshot.tableHeaders.source',
    value:     'sourceName',
    sort:      ['sourceName', 'nameSort'],
    formatter: 'DBSnapshotSource',
  },
  {
    name:     'origin',
    labelKey: 'dbaas.snapshot.tableHeaders.origin',
    value:    'origin',
    sort:     ['origin', 'nameSort'],
  },
  {
    name:        'engine',
    labelKey:    'dbaas.instance.tableHeaders.engine',
    value:       'engineDisplay',
    sort:        ['engineDisplay'],
    dashIfEmpty: true,
  },
  {
    name:        'size',
    labelKey:    'dbaas.snapshot.tableHeaders.size',
    value:       'sizeDisplay',
    sort:        ['sizeGiB'],
    dashIfEmpty: true,
  },
  {
    name:      'progress',
    labelKey:  'dbaas.snapshot.tableHeaders.progress',
    value:     'progress',
    sort:      ['progress'],
    formatter: 'DBSnapshotProgress',
    width:     140,
  },
  {
    name:          'completed',
    labelKey:      'dbaas.snapshot.tableHeaders.completed',
    value:         'completionTime',
    sort:          ['completionTime'],
    formatter:     'LiveDate',
    formatterOpts: { addSuffix: true },
    dashIfEmpty:   true,
  },
  AGE,
];

// Snapshots list: manual and automated snapshots together, with an Origin
// filter. Rows come from Shell's ResourceList.
export default {
  name:       'ListDBSnapshot',
  components: { ResourceTable, ShellSelect },

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
    return {
      headers: DBSNAPSHOT_HEADERS,
      origin:  ALL,
    };
  },

  created() {
    // Source instances, for the Source column links (missing ones show as deleted)
    const inStore = this.$store.getters['currentProduct']?.inStore || 'cluster';

    if (this.$store.getters[`${ inStore }/canList`](DBAAS.INSTANCE)) {
      this.$store.dispatch(`${ inStore }/findAll`, { type: DBAAS.INSTANCE });
    }
  },

  computed: {
    originOptions() {
      return [
        { label: this.t('dbaas.snapshot.origin.all'), value: ALL },
        { label: this.t('dbaas.snapshot.origin.manual'), value: SNAPSHOT_ORIGIN.MANUAL },
        { label: this.t('dbaas.snapshot.origin.automated'), value: SNAPSHOT_ORIGIN.AUTOMATED },
      ];
    },

    filteredRows() {
      return this.origin === ALL ? this.rows : this.rows.filter((r) => r.origin === this.origin);
    },
  },
};
</script>

<template>
  <ResourceTable
    :schema="schema"
    :rows="filteredRows"
    :headers="headers"
    :loading="loading"
    :use-query-params-for-simple-filtering="useQueryParamsForSimpleFiltering"
    default-sort-by="age"
    no-rows-key="dbaas.snapshot.list.noRows"
  >
    <template #more-header-middle>
      <ShellSelect
        v-model:value="origin"
        class="origin-filter"
        :options="originOptions"
        :searchable="false"
        :aria-label="t('dbaas.snapshot.origin.label')"
      />
    </template>
  </ResourceTable>
</template>

<style lang="scss" scoped>
// Shell renders this slot before the list/group-by buttons in the header;
// lay them out on one row with the buttons first, then the Origin filter.
:deep(.middle) {
  display: flex;
  align-items: center;
  gap: 10px;
}

.origin-filter {
  order: 1;
  min-width: 180px;
}
</style>
