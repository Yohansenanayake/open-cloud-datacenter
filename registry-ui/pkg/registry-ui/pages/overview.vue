<script>
import Loading from '@shell/components/Loading';
import { POD, WORKLOAD_TYPES } from '@shell/config/types';
import {
  PRODUCT_NAME, REGISTRY, ADDON, ADDON_ID, OPERATOR_NAMESPACE, OPERATOR_SELECTOR, PLAN_QUOTA_GIB
} from '../types';

export default {
  name: 'RegistriesOverview',

  components: { Loading },

  async fetch() {
    const store = this.$store;

    this.registries = await store.dispatch('cluster/findAll', { type: REGISTRY });

    // Operator health. Each part is optional: a reader may lack access, and
    // the Addon type exists only on Harvester.
    try {
      this.pods = await store.dispatch('cluster/findMatching', {
        type: POD, selector: OPERATOR_SELECTOR, namespace: OPERATOR_NAMESPACE
      });
    } catch (e) {
      this.pods = [];
    }
    try {
      const deployments = await store.dispatch('cluster/findMatching', {
        type: WORKLOAD_TYPES.DEPLOYMENT, selector: OPERATOR_SELECTOR, namespace: OPERATOR_NAMESPACE
      });
      const env = deployments[0]?.spec?.template?.spec?.containers?.[0]?.env || [];

      this.harborURL = env.find((e) => e.name === 'HARBOR_URL')?.value || '';
    } catch (e) {
      this.harborURL = '';
    }
    if (store.getters['cluster/schemaFor'](ADDON)) {
      try {
        this.addon = await store.dispatch('cluster/find', { type: ADDON, id: ADDON_ID });
      } catch (e) {
        this.addon = null;
      }
    }
  },

  data() {
    return {
      registries: [], pods: [], addon: null, harborURL: ''
    };
  },

  computed: {
    clusterId() {
      return this.$route.params.cluster;
    },

    listLocation() {
      return {
        name:   'c-cluster-product-resource',
        params: {
          cluster: this.clusterId, product: PRODUCT_NAME, resource: REGISTRY
        }
      };
    },

    readyCount() {
      return this.registries.filter((r) => r.isReady).length;
    },

    notReady() {
      return this.registries.filter((r) => !r.isReady);
    },

    committedGiB() {
      return this.registries.reduce((sum, r) => sum + r.quotaGiB, 0);
    },

    byPlan() {
      return Object.keys(PLAN_QUOTA_GIB).map((plan) => ({
        plan,
        count: this.registries.filter((r) => r.plan === plan).length,
        gib:   PLAN_QUOTA_GIB[plan],
      }));
    },

    // Pods that exist now: not finished, not being deleted.
    livePods() {
      return this.pods.filter((p) => !p.metadata?.deletionTimestamp && !['Succeeded', 'Failed'].includes(p.status?.phase));
    },

    operatorReady() {
      return this.livePods.some((p) => p.status?.phase === 'Running' && (p.status?.containerStatuses || []).every((c) => c.ready));
    },

    podSummary() {
      if (!this.livePods.length) {
        return this.t('registryUi.overview.notInstalled');
      }
      const ready = this.livePods.filter((p) => (p.status?.containerStatuses || []).every((c) => c.ready)).length;

      return `${ ready }/${ this.livePods.length } ${ this.t('registryUi.overview.ready') }`;
    },

    addonSummary() {
      if (!this.addon) {
        return this.t('registryUi.overview.notInstalled');
      }

      return `${ this.addon.spec?.enabled ? 'Enabled' : 'Disabled' } · ${ this.addon.status?.status || '—' } · ${ this.addon.spec?.version || '' }`;
    },
  },
};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <div
    v-else
    class="registries-overview"
  >
    <header class="mb-20">
      <h1>{{ t('product.registries') }}</h1>
      <p class="text-muted">
        {{ t('registryUi.overview.subtitle') }}
      </p>
    </header>

    <section class="tiles">
      <router-link
        :to="listLocation"
        class="tile"
      >
        <span class="num">{{ registries.length }}</span>
        <span class="lbl">{{ t('registryUi.overview.total') }}</span>
      </router-link>
      <div class="tile">
        <span class="num text-success">{{ readyCount }}</span>
        <span class="lbl">{{ t('registryUi.overview.readyCount') }}</span>
      </div>
      <div class="tile">
        <span
          class="num"
          :class="{ 'text-error': notReady.length }"
        >{{ notReady.length }}</span>
        <span class="lbl">{{ t('registryUi.overview.attention') }}</span>
      </div>
      <div class="tile">
        <span class="num">{{ committedGiB }} GiB</span>
        <span class="lbl">{{ t('registryUi.overview.committed') }}</span>
      </div>
    </section>

    <div class="cols">
      <section class="panel">
        <h2>{{ t('registryUi.overview.operator') }}</h2>
        <table class="facts">
          <tr>
            <th>{{ t('registryUi.overview.pod') }}</th>
            <td :class="operatorReady ? 'text-success' : 'text-error'">
              {{ podSummary }}
            </td>
          </tr>
          <tr>
            <th>{{ t('registryUi.overview.harbor') }}</th>
            <td>{{ harborURL || '—' }}</td>
          </tr>
          <tr>
            <th>{{ t('registryUi.overview.addon') }}</th>
            <td>{{ addonSummary }}</td>
          </tr>
        </table>
      </section>

      <section class="panel">
        <h2>{{ t('registryUi.overview.byPlan') }}</h2>
        <table class="facts">
          <tr
            v-for="p in byPlan"
            :key="p.plan"
          >
            <th>{{ p.plan }} ({{ p.gib }} GiB)</th>
            <td>{{ p.count }}</td>
          </tr>
        </table>
      </section>
    </div>

    <section class="panel">
      <h2>{{ t('registryUi.overview.needsAttention') }}</h2>
      <p v-if="!registries.length">
        {{ t('registryUi.overview.none') }}
      </p>
      <p
        v-else-if="!notReady.length"
        class="text-success"
      >
        {{ t('registryUi.overview.allReady') }}
      </p>
      <table
        v-else
        class="facts"
      >
        <tr
          v-for="r in notReady"
          :key="r.id"
        >
          <th>
            <router-link :to="r.detailLocation">
              {{ r.metadata.namespace }}/{{ r.metadata.name }}
            </router-link>
          </th>
          <td class="text-error">
            {{ r.phase }} — {{ r.status && r.status.message }}
          </td>
        </tr>
      </table>
    </section>
  </div>
</template>

<style lang="scss" scoped>
.registries-overview { padding: 20px; }
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px;
  margin-bottom: 20px;
}
.tile {
  display: flex;
  flex-direction: column;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--border-radius);
  background: var(--box-bg);
  color: var(--body-text);
  text-decoration: none;
  .num { font-size: 28px; font-weight: 600; }
  .lbl { color: var(--input-label); }
}
.cols {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 16px;
}
.panel {
  margin-bottom: 20px;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--border-radius);
  h2 { font-size: 18px; margin-bottom: 10px; }
}
.facts {
  width: 100%;
  border-collapse: collapse;
  th, td { padding: 6px 8px; text-align: left; border-bottom: 1px solid var(--border); }
  th { font-weight: normal; color: var(--input-label); width: 45%; }
}
</style>
