<script>
import SortableTable from '@shell/components/SortableTable';
import DateFormatter from '@shell/components/formatter/Date';
import { timeUntil } from '../utils/time';
import Loading from '@shell/components/Loading';
import { Banner } from '@components/Banner';
import { RcButton } from '@components/RcButton';
import { AGE, NAME, STATE } from '@shell/config/table-headers';
import { DBAAS } from '../types';

// Backups of one DBInstance (view mode of its Backup tab): the schedule
// settings, a Take Snapshot button and the snapshots taken from it.
export default {
  name: 'DBInstanceBackups',

  components: {
    Banner, DateFormatter, Loading, RcButton, SortableTable
  },

  props: {
    value: {
      type:     Object,
      required: true,
    },
  },

  async fetch() {
    if (this.canListSnapshots) {
      await this.$store.dispatch(`${ this.inStore }/findAll`, { type: DBAAS.SNAPSHOT, opt: { namespaced: this.value.namespace } });
    }
  },

  data() {
    return {
      headers: [
        { ...STATE, formatter: 'DBInstanceState' },
        NAME,
        {
          name:     'origin',
          labelKey: 'dbaas.snapshot.tableHeaders.origin',
          value:    'origin',
          sort:     ['origin', 'nameSort'],
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
      ],
    };
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    canListSnapshots() {
      return !!this.$store.getters[`${ this.inStore }/canList`](DBAAS.SNAPSHOT);
    },

    automated() {
      return this.value.spec?.backup?.automated || {};
    },

    // Defaults from the CRD when the fields are omitted
    automatedEnabled() {
      return this.automated.enabled !== false;
    },

    nextScheduled() {
      return this.value.status?.backup?.nextScheduledSnapshotTime || '';
    },

    // e.g. "in about 20 hours"; empty if the time has already passed
    nextScheduledRelative() {
      const until = timeUntil(this.nextScheduled);

      return until ? this.t(until.key, until.args, true) : '';
    },
  },
};
</script>

<template>
  <div class="dbinstance-backups">
    <Banner
      v-if="!value.hasBackup"
      color="info"
      :label="t('dbaas.snapshot.backups.disabled')"
    />

    <template v-else>
      <dl class="settings">
        <dt>{{ t('dbaas.instance.form.automatedBackupEnabled') }}</dt>
        <dd>{{ automatedEnabled ? t('generic.yes') : t('generic.no') }}</dd>

        <template v-if="automatedEnabled">
          <dt>{{ t('dbaas.instance.form.backupWindow') }}</dt>
          <dd>{{ automated.preferredWindowUTC || '02:00-03:00' }}</dd>

          <dt>{{ t('dbaas.instance.form.retainCount') }}</dt>
          <dd>{{ automated.retainCount || 7 }}</dd>

          <dt>{{ t('dbaas.snapshot.backups.nextScheduled') }}</dt>
          <dd>
            <template v-if="nextScheduled">
              <DateFormatter :value="nextScheduled" />
              <span
                v-if="nextScheduledRelative"
                class="text-muted ml-5"
              >({{ nextScheduledRelative }})</span>
            </template>
            <span v-else>—</span>
          </dd>
        </template>
      </dl>

      <div class="take-snapshot mt-20 mb-20">
        <RcButton
          :disabled="!value.canTakeSnapshot"
          @click="value.takeSnapshot()"
        >
          {{ t('dbaas.snapshot.actions.take') }}
        </RcButton>
        <span
          v-if="!value.canTakeSnapshot"
          class="text-muted"
        >{{ value.takeSnapshotBlockedReason }}</span>
      </div>

      <h3>{{ t('dbaas.snapshot.backups.snapshots') }}</h3>
      <Banner
        v-if="!canListSnapshots"
        color="info"
        :label="t('dbaas.snapshot.backups.forbidden')"
      />
      <Loading v-else-if="$fetchState.pending" />
      <SortableTable
        v-else
        :rows="value.snapshots"
        :headers="headers"
        key-field="id"
        :search="false"
        :table-actions="false"
        :row-actions="true"
        :paging="true"
        :rows-per-page="10"
        default-sort-by="age"
        no-rows-key="dbaas.snapshot.backups.none"
      />
    </template>
  </div>
</template>

<style lang="scss" scoped>
.dbinstance-backups {
  .settings {
    display: grid;
    grid-template-columns: 220px 1fr;
    row-gap: 10px;
    column-gap: 20px;
    margin: 0;

    dt {
      color: var(--input-label);
    }

    dd {
      margin: 0;
    }
  }

  .take-snapshot {
    display: flex;
    align-items: center;
    gap: 12px;
  }
}
</style>
