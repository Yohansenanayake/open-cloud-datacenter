<script>
import Loading from '@shell/components/Loading';
import SortableTable from '@shell/components/SortableTable';
import { BadgeState } from '@components/BadgeState';
import { RcButton } from '@components/RcButton';
import { RcDropdownMenu } from '@components/RcDropdown';
import { MANAGEMENT } from '@shell/config/types';
import { OVERVIEW_ROUTE, PRODUCT_NAME } from '../types';
import { CLUSTER_MENU_ACTIONS, CLUSTER_STATUS, checkClusterForRegistry, fetchHarvesterVersion } from '../utils/registry-clusters';

const STATUS_COLOR = {
  [CLUSTER_STATUS.AVAILABLE]:   'bg-success',
  [CLUSTER_STATUS.UNAVAILABLE]: 'bg-darker',
  [CLUSTER_STATUS.ERROR]:       'bg-error',
};

// Landing page of the Registries rail entry: the Harvester clusters on which the
// current user can use Registries. All clusters are checked in parallel before the
// list is shown. Clusters without the operator (or not visible to the user) are
// hidden; clusters that could not be checked stay listed with their state.
export default {
  name: 'RegistryClusters',

  components: {
    BadgeState, Loading, RcButton, RcDropdownMenu, SortableTable
  },

  async fetch() {
    const clusters = await this.$store.dispatch('management/findAll', { type: MANAGEMENT.CLUSTER });

    this.candidates = clusters.filter((c) => c.isHarvester);
    await Promise.all(this.candidates.map((c) => this.check(c)));
  },

  data() {
    return {
      CLUSTER_STATUS,
      STATUS_COLOR,
      candidates: [],
      results:    {},
      retrying:   {},
      headers:    [
        {
          name:     'state',
          labelKey: 'tableHeaders.state',
          value:    'status',
          sort:     ['status', 'name'],
          width:    120,
        },
        {
          name:     'name',
          labelKey: 'tableHeaders.name',
          value:    'name',
          sort:     ['name'],
        },
        {
          name:        'harvesterVersion',
          labelKey:    'registryUi.manager.clusters.tableHeaders.harvesterVersion',
          value:       'harvesterVersion',
          sort:        ['harvesterVersion'],
          dashIfEmpty: true,
        },
        {
          name:        'kubernetesVersion',
          labelKey:    'registryUi.manager.clusters.tableHeaders.kubernetesVersion',
          value:       'kubernetesVersion',
          sort:        ['kubernetesVersion'],
          dashIfEmpty: true,
        },
        {
          name:        'apiVersion',
          labelKey:    'registryUi.manager.clusters.tableHeaders.apiVersion',
          value:       'apiVersion',
          sort:        ['apiVersion'],
          dashIfEmpty: true,
        },
        {
          name:  'action',
          label: '',
          value: 'id',
          width: 160,
          align: 'right',
        },
      ],
    };
  },

  computed: {
    rows() {
      return this.candidates
        .map((cluster) => ({
          id:                cluster.id,
          name:              cluster.nameDisplay,
          kubernetesVersion: cluster.kubernetesVersion || '',
          menuActions:       this.menuActions(cluster),
          cluster,
          ...this.results[cluster.id],
        }))
        .filter((row) => row.status && row.status !== CLUSTER_STATUS.ABSENT);
    },
  },

  methods: {
    async check(cluster) {
      if (!cluster.isReady) {
        this.results[cluster.id] = { status: CLUSTER_STATUS.UNAVAILABLE };

        return;
      }

      const [result, harvesterVersion] = await Promise.all([
        checkClusterForRegistry(this.$store, cluster.id),
        fetchHarvesterVersion(this.$store, cluster.id),
      ]);

      this.results[cluster.id] = { ...result, harvesterVersion };
    },

    // Rancher's own cluster actions (labels, icons, permission checks), limited
    // to shell and kubeconfig access
    menuActions(cluster) {
      return (cluster._availableActions || []).filter((a) => CLUSTER_MENU_ACTIONS.includes(a.action) && a.enabled);
    },

    runAction(row, action) {
      row.cluster[action.action]();
    },

    async retry(row) {
      const cluster = this.candidates.find((c) => c.id === row.id);

      if (cluster) {
        this.retrying[row.id] = true;
        try {
          await this.check(cluster);
        } finally {
          this.retrying[row.id] = false;
        }
      }
    },

    statusLabel(status) {
      return this.t(`registryUi.manager.clusters.status.${ status }`);
    },

    statusTooltip(row) {
      if (row.status === CLUSTER_STATUS.ERROR) {
        return this.t('registryUi.manager.clusters.error', { message: row.message || '' });
      }

      if (row.status === CLUSTER_STATUS.UNAVAILABLE) {
        return this.t('registryUi.manager.clusters.unavailable');
      }

      return null;
    },

    manageLocation(row) {
      return { name: OVERVIEW_ROUTE, params: { cluster: row.id, product: PRODUCT_NAME } };
    },
  },
};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <div v-else>
    <header>
      <div class="title">
        <h1>{{ t('registryUi.manager.clusters.title') }}</h1>
      </div>
    </header>
    <p class="mb-20 text-muted">
      {{ t('registryUi.manager.clusters.description') }}
    </p>

    <SortableTable
      :rows="rows"
      :headers="headers"
      :search="false"
      :table-actions="false"
      :row-actions="false"
      default-sort-by="name"
      key-field="id"
      no-rows-key="registryUi.manager.clusters.none"
    >
      <template #cell:state="{ row }">
        <BadgeState
          v-clean-tooltip="statusTooltip(row)"
          :label="statusLabel(row.status)"
          :color="STATUS_COLOR[row.status]"
        />
      </template>

      <template #cell:name="{ row }">
        <router-link
          v-if="row.status === CLUSTER_STATUS.AVAILABLE"
          :to="manageLocation(row)"
        >
          {{ row.name }}
        </router-link>
        <span v-else>{{ row.name }}</span>
      </template>

      <template #cell:action="{ row }">
        <div class="row-actions">
          <RcButton
            v-if="row.status === CLUSTER_STATUS.AVAILABLE"
            :to="manageLocation(row)"
          >
            {{ t('registryUi.manager.clusters.manage') }}
          </RcButton>
          <RcButton
            v-else-if="row.status === CLUSTER_STATUS.ERROR"
            variant="secondary"
            :disabled="retrying[row.id]"
            @click="retry(row)"
          >
            {{ t('registryUi.manager.clusters.retry') }}
          </RcButton>
          <RcDropdownMenu
            v-if="row.menuActions.length"
            :options="row.menuActions"
            button-variant="link"
            button-size="medium"
            :button-aria-label="t('registryUi.manager.clusters.actions', { name: row.name })"
            :dropdown-aria-label="t('registryUi.manager.clusters.actions', { name: row.name })"
            @select="(e, action) => runAction(row, action)"
          />
        </div>
      </template>
    </SortableTable>
  </div>
</template>

<style lang="scss" scoped>
.row-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}
</style>
