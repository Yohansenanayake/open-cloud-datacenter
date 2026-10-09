<script>
import SortableTable from '@shell/components/SortableTable';
import Loading from '@shell/components/Loading';
import { Banner } from '@components/Banner';
import { EVENT } from '@shell/config/types';
import { dbInstanceEventSource } from '../utils/dbinstance-events';

// Events for a DBInstance and the VM, pod and disks the operator runs it on,
// like the Events tab of a Harvester VM. Only the instance's namespace is
// loaded (and watched), not every Event in the cluster.
export default {
  name: 'DBInstanceEvents',

  components: {
    Banner, Loading, SortableTable
  },

  props: {
    value: {
      type:     Object,
      required: true,
    },
  },

  async fetch() {
    if (!this.canListEvents) {
      return;
    }

    await this.$store.dispatch(`${ this.inStore }/findAll`, { type: EVENT, opt: { namespaced: this.value.namespace } });
  },

  // Stop watching the namespace's Events once the tab goes away, as Shell's own
  // Events tab does
  beforeUnmount() {
    this.$store.dispatch(`${ this.inStore }/forgetType`, EVENT);
  },

  data() {
    return {
      headers: [
        {
          name:     'type',
          labelKey: 'tableHeaders.type',
          value:    'eventType',
          sort:     ['eventType'],
          width:    100,
        },
        {
          name:     'reason',
          labelKey: 'tableHeaders.reason',
          value:    'reason',
          sort:     ['reason'],
          width:    200,
        },
        {
          name:     'resource',
          labelKey: 'dbaas.instance.events.resource',
          value:    'resource',
          sort:     ['resource'],
        },
        {
          name:          'date',
          labelKey:      'dbaas.instance.events.date',
          value:         'timestamp',
          sort:          'timestamp:desc',
          formatter:     'LiveDate',
          formatterOpts: { addSuffix: true },
          width:         140,
          align:         'right',
        },
      ],
    };
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    canListEvents() {
      return !!this.$store.getters[`${ this.inStore }/canList`](EVENT);
    },

    rows() {
      return this.$store.getters[`${ this.inStore }/all`](EVENT)
        .map((event) => ({ event, source: dbInstanceEventSource(event, this.value) }))
        .filter(({ source }) => !!source)
        .map(({ event, source }) => ({
          id:        event.id,
          eventType: event._type || event.eventType,
          reason:    event.reason,
          resource:  `${ this.t(`dbaas.instance.events.source.${ source }`) } ${ event.involvedObject.name }`,
          message:   event.displayMessage,
          count:     event.count,
          timestamp: event.lastTimestamp || event.eventTime || event.metadata?.creationTimestamp,
        }));
    },
  },
};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <Banner
    v-else-if="!canListEvents"
    color="info"
    :label="t('dbaas.instance.events.forbidden')"
  />
  <SortableTable
    v-else
    :rows="rows"
    :headers="headers"
    key-field="id"
    :search="false"
    :table-actions="false"
    :row-actions="false"
    :paging="true"
    :rows-per-page="10"
    default-sort-by="date"
    no-rows-key="dbaas.instance.events.none"
  >
    <template #cell:type="{ row }">
      <span :class="{ 'text-warning': row.eventType === 'Warning' }">{{ row.eventType }}</span>
    </template>
    <template #cell:resource="{ row }">
      <div class="text-info">
        {{ row.resource }}
        <span
          v-if="row.count > 1"
          class="text-muted"
        >&times;{{ row.count }}</span>
      </div>
      <div
        v-if="row.message"
        class="message"
      >
        {{ row.message }}
      </div>
    </template>
  </SortableTable>
</template>

<style lang="scss" scoped>
.message {
  word-break: break-word;
}
</style>
