<script>
import AsyncButton from '@shell/components/AsyncButton';
import { Card } from '@components/Card';
import { Banner } from '@components/Banner';
import { LabeledInput } from '@components/Form/LabeledInput';
import { exceptionToErrorsArray } from '@shell/utils/error';
import { DBAAS } from '../types';
import { defaultSnapshotName, validateSnapshotName } from '../utils/dbsnapshot';

// Take a manual snapshot of one DBInstance (opened from the instance's
// "Take Snapshot" action). Creates a DBSnapshot; the operator does the rest.
export default {
  name: 'DBaaSTakeSnapshotDialog',

  emits: ['close'],

  components: {
    AsyncButton, Banner, Card, LabeledInput
  },

  props: {
    resources: {
      type:    Array,
      default: () => []
    },

    registerBackgroundClosing: {
      type:    Function,
      default: () => {}
    },
  },

  data() {
    const instance = this.resources[0];

    return {
      instance,
      name:   instance ? defaultSnapshotName(instance.name) : '',
      errors: [],
    };
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    nameError() {
      const problem = validateSnapshotName(this.name);

      return problem ? this.t(problem.key, problem.args || {}) : '';
    },

    blockedReason() {
      return this.instance?.takeSnapshotBlockedReason || '';
    },
  },

  methods: {
    close() {
      this.$emit('close');
    },

    async take(buttonDone) {
      this.errors = [];

      try {
        const snapshot = await this.$store.dispatch(`${ this.inStore }/create`, {
          type:     DBAAS.SNAPSHOT,
          metadata: { name: this.name, namespace: this.instance.namespace },
          spec:     { sourceInstanceRef: { name: this.instance.name } },
        });

        await snapshot.save();
        buttonDone(true);
        this.$store.dispatch('growl/success', {
          title:   this.t('dbaas.snapshot.dialog.successTitle'),
          message: this.t('dbaas.snapshot.dialog.successMessage', { name: this.name }, true),
          timeout: 5000,
        }, { root: true });
        this.close();
      } catch (e) {
        this.errors = exceptionToErrorsArray(e);
        buttonDone(false);
      }
    },
  },
};
</script>

<template>
  <Card :show-highlight-border="false">
    <template #title>
      <h4 class="text-default-text">
        {{ t('dbaas.snapshot.dialog.title') }}
      </h4>
    </template>

    <template #body>
      <div class="pl-10 pr-10">
        <p class="mb-20">
          {{ t('dbaas.snapshot.dialog.description', { instance: instance && instance.name }, true) }}
        </p>
        <LabeledInput
          v-model:value="name"
          :label="t('dbaas.snapshot.dialog.name')"
          :required="true"
          :rules="[() => nameError]"
        />
        <Banner
          v-if="blockedReason"
          color="warning"
          :label="blockedReason"
        />
        <Banner
          v-for="(err, i) in errors"
          :key="i"
          color="error"
          :label="err"
        />
      </div>
    </template>

    <template #actions>
      <div class="dialog-actions">
        <button
          class="btn role-secondary"
          @click="close"
        >
          {{ t('generic.cancel') }}
        </button>
        <AsyncButton
          class="ml-20"
          :action-label="t('dbaas.snapshot.dialog.take')"
          :waiting-label="t('dbaas.snapshot.dialog.taking')"
          :disabled="!!nameError || !!blockedReason"
          @click="take"
        />
      </div>
    </template>
  </Card>
</template>

<style lang="scss" scoped>
.dialog-actions {
  display: flex;
  justify-content: flex-end;
  width: 100%;
}
</style>
