<script>
import { Banner } from '@components/Banner';
import { mapGetters } from 'vuex';
import { resourceNames } from '@shell/utils/string';

// Body of Shell's delete dialog for DBInstances. Shell's own dialog puts the
// warning / prevent-deletion message inside the confirm-name input when
// confirmRemove is set, where it is never rendered, so the messages are shown
// here instead. Deletion itself (and the disabled Delete button, driven by the
// model's preventDeletionMessage) stays with Shell's dialog.
export default {
  name: 'PromptRemoveDBInstance',

  components: { Banner },

  props: {
    value: {
      type:    Array,
      default: () => []
    },

    names: {
      type:    Array,
      default: () => []
    },

    type: {
      type:     String,
      required: true
    },

    // Closes Shell's delete dialog (passed in by PromptRemove)
    close: {
      type:    Function,
      default: () => {}
    },
  },

  computed: {
    // A store-bound t: resourceNames() calls it as a plain function, which the
    // global template t (it reads this.$store) does not survive
    ...mapGetters({ t: 'i18n/t' }),

    protectedInstances() {
      return this.value.filter((r) => r.preventDeletionMessage);
    },

    protectedMessage() {
      if (this.protectedInstances.length === 1) {
        return this.protectedInstances[0].preventDeletionMessage;
      }

      return this.t('dbaas.instance.delete.protectedMany', { names: this.protectedInstances.map((r) => r.nameDisplay).join(', ') });
    },

    warning() {
      return this.value.find((r) => r.spec?.backup)?.warnDeletionMessage || this.value[0]?.warnDeletionMessage;
    },

    // Suggest keeping a copy: manual snapshots survive the instance
    snapshotCandidates() {
      return this.value.filter((r) => r.canTakeSnapshot);
    },
  },

  methods: {
    resourceNames,

    takeSnapshot() {
      const instance = this.snapshotCandidates[0];

      this.close();
      instance.takeSnapshot();
    },
  },
};
</script>

<template>
  <div>
    {{ t('promptRemove.attemptingToRemove', { type }) }}
    <span v-clean-html="resourceNames(names, null, t)" />

    <Banner
      v-if="protectedInstances.length"
      color="error"
      class="mb-0"
      :label="protectedMessage"
    />
    <template v-else>
      <Banner
        v-if="warning"
        color="warning"
        class="mb-0"
        :label="warning"
      />
      <p
        v-if="snapshotCandidates.length"
        class="mt-10"
      >
        <template v-if="value.length === 1">
          {{ t('dbaas.instance.delete.snapshotTip') }}
          <a
            role="button"
            @click="takeSnapshot"
          >{{ t('dbaas.snapshot.actions.take') }}</a>
        </template>
        <template v-else>
          {{ t('dbaas.instance.delete.snapshotTipMany') }}
        </template>
      </p>
    </template>
  </div>
</template>
