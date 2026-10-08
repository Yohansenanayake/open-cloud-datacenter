<script>
import { Banner } from '@components/Banner';
import { DBAAS } from '../types';

// "Restored from snapshot X of instance Y, by restore Z" for an instance a
// DBRestore created (spec.restoredFrom). Links work while those objects still
// exist and are the same ones (matched by UID).
export default {
  name: 'DBInstanceRestoredFrom',

  components: { Banner },

  props: {
    value: {
      type:     Object,
      required: true,
    },
  },

  created() {
    const inStore = this.$store.getters['currentProduct']?.inStore || 'cluster';

    [DBAAS.SNAPSHOT, DBAAS.RESTORE].forEach((type) => {
      if (this.$store.getters[`${ inStore }/canList`](type)) {
        this.$store.dispatch(`${ inStore }/findAll`, { type, opt: { namespaced: this.value.namespace } });
      }
    });
    this.inStore = inStore;
  },

  computed: {
    from() {
      return this.value.spec?.restoredFrom || {};
    },

    snapshot() {
      return this.find(DBAAS.SNAPSHOT, this.from.dbSnapshotName, this.from.dbSnapshotUID);
    },

    restore() {
      return this.find(DBAAS.RESTORE, this.from.dbRestoreName, this.from.dbRestoreUID);
    },

    source() {
      return this.find(DBAAS.INSTANCE, this.from.sourceInstanceName, this.from.sourceInstanceUID);
    },
  },

  methods: {
    find(type, name, uid) {
      if (!name) {
        return null;
      }

      const obj = this.$store.getters[`${ this.inStore || 'cluster' }/byId`](type, `${ this.value.namespace }/${ name }`);

      return obj && (!uid || obj.metadata?.uid === uid) ? obj : null;
    },
  },
};
</script>

<template>
  <Banner color="info">
    <span>{{ t('dbaas.restore.restoredFrom.prefix') }}</span>
    <router-link
      v-if="snapshot"
      :to="snapshot.detailLocation"
    >
      {{ from.dbSnapshotName }}
    </router-link>
    <span v-else>{{ from.dbSnapshotName }}</span>
    <template v-if="from.sourceInstanceName">
      <span>{{ t('dbaas.restore.restoredFrom.ofInstance') }}</span>
      <router-link
        v-if="source"
        :to="source.detailLocation"
      >
        {{ from.sourceInstanceName }}
      </router-link>
      <span v-else>{{ from.sourceInstanceName }}</span>
    </template>
    <template v-if="from.dbRestoreName">
      <span>{{ t('dbaas.restore.restoredFrom.byRestore') }}</span>
      <router-link
        v-if="restore"
        :to="restore.detailLocation"
      >
        {{ from.dbRestoreName }}
      </router-link>
      <span v-else>{{ from.dbRestoreName }}</span>
    </template>
  </Banner>
</template>

<style lang="scss" scoped>
span + a, a + span, span + span {
  margin-left: 4px;
}
</style>
