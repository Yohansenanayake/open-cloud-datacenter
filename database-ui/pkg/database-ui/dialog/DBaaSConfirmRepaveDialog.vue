<script>
import { nextTick } from 'vue';
import AsyncButton from '@shell/components/AsyncButton';
import { Card } from '@components/Card';
import { Banner } from '@components/Banner';
import { exceptionToErrorsArray } from '@shell/utils/error';

// Confirm applying an OS update (repave) to a DBInstance. Sets the repave
// trigger annotation; the operator stops the VM, replaces its OS disk with the
// new image revision (the data disk is kept) and starts it again.
export default {
  name: 'DBaaSConfirmRepaveDialog',

  emits: ['close'],

  components: {
    AsyncButton, Banner, Card
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
    return { errors: [] };
  },

  computed: {
    instance() {
      return this.resources[0];
    },

    driftMessage() {
      return this.instance?.imageDriftSummary || '';
    },
  },

  methods: {
    close() {
      this.$emit('close');
    },

    // Shell's PromptModal only notices a change of its open flag, so closing
    // this dialog and opening another in the same tick leaves it out of sync
    // (the next dialog then never opens). Let the close land first.
    async takeSnapshot() {
      const instance = this.instance;

      this.close();
      await nextTick();
      instance.takeSnapshot();
    },

    async apply(buttonDone) {
      this.errors = [];

      try {
        await this.instance.requestRepave();
        buttonDone(true);
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
        {{ t('dbaas.instance.image.dialog.title', { name: instance && instance.nameDisplay }, true) }}
      </h4>
    </template>

    <template #body>
      <div class="pl-10 pr-10">
        <p
          v-if="driftMessage"
          class="text-muted mb-10"
        >
          {{ driftMessage }}
        </p>
        <Banner
          color="warning"
          :label="t('dbaas.instance.image.dialog.downtime')"
        />
        <p class="mb-10">
          {{ t('dbaas.instance.image.dialog.snapshotTip') }}
          <a
            v-if="instance && instance.canTakeSnapshot"
            role="button"
            @click="takeSnapshot"
          >{{ t('dbaas.snapshot.actions.take') }}</a>
        </p>
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
          :action-label="t('dbaas.instance.image.apply')"
          :waiting-label="t('dbaas.instance.image.dialog.applying')"
          :disabled="!instance || !instance.canApplyOSUpdate"
          @click="apply"
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
