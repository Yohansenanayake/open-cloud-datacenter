<script>
import ResourceTable from '@shell/components/ResourceTable';
import { AGE, NAME, NAMESPACE, STATE } from '@shell/config/table-headers';

export const DBINSTANCE_HEADERS = [
  { ...STATE, formatter: 'DBInstanceState' },
  NAME,
  NAMESPACE,
  {
    name:     'engine',
    labelKey: 'dbaas.instance.tableHeaders.engine',
    value:    'engineDisplay',
    sort:     ['engineSort', 'nameSort'],
  },
  {
    name:        'instanceClass',
    labelKey:    'dbaas.instance.tableHeaders.class',
    value:       'instanceClass',
    sort:        ['instanceClass', 'nameSort'],
    dashIfEmpty: true,
  },
  {
    name:        'storage',
    labelKey:    'dbaas.instance.tableHeaders.storage',
    value:       'storageDisplay',
    sort:        ['allocatedStorage', 'nameSort'],
    search:      ['storageDisplay'],
    dashIfEmpty: true,
  },
  {
    name:      'image',
    labelKey:  'dbaas.instance.tableHeaders.image',
    value:     'imageStatusLabel',
    sort:      ['imageStatusLabel', 'nameSort'],
    formatter: 'DBInstanceImage',
  },
  {
    name:      'endpoint',
    labelKey:  'dbaas.instance.tableHeaders.endpoint',
    value:     'endpointDisplay',
    sort:      ['endpointDisplay'],
    formatter: 'DBInstanceEndpoint',
  },
  AGE,
];

// Rows are fetched by Shell's ResourceList; this component only shapes the table.
export default {
  name:       'ListDBInstance',
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
    return { headers: DBINSTANCE_HEADERS };
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
    no-rows-key="dbaas.instance.list.noRows"
  />
</template>
