<script>
import AsyncButton from '@shell/components/AsyncButton';
import { Card } from '@components/Card';
import { Banner } from '@components/Banner';
import { mapGetters } from 'vuex';
import { resourceNames } from '@shell/utils/string';
import { exceptionToErrorsArray } from '@shell/utils/error';

// Confirm stopping one or more DBInstances (sets spec.running=false). The
// operator stops the VM; storage and data are kept, and Start brings it back.
export default {
  name: 'DBaaSConfirmStopDialog',

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
    // A store-bound t: resourceNames() calls it as a plain function
    ...mapGetters({ t: 'i18n/t' }),

    names() {
      return resourceNames(this.resources.map((r) => r.nameDisplay), null, this.t);
    },
  },

  methods: {
    close(data) {
      this.$emit('close', data);
    },

    async stop(buttonDone) {
      this.errors = [];

      try {
        await Promise.all(this.resources.map((r) => r.setRunning(false)));
        buttonDone(true);
        this.close({ performCallback: true, clearTableSelection: true });
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
        {{ t('dbaas.instance.stop.title', { count: resources.length }) }}
      </h4>
    </template>

    <template #body>
      <div class="pl-10 pr-10">
        <p class="mb-10">
          {{ t('dbaas.instance.stop.attempting') }} <span v-clean-html="names" />
        </p>
        <Banner
          color="warning"
          :label="t('dbaas.instance.stop.warning')"
        />
        <p class="text-muted">
          {{ t('dbaas.instance.stop.protip') }}
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
          @click="close()"
        >
          {{ t('generic.cancel') }}
        </button>
        <AsyncButton
          class="ml-20"
          :action-label="t('dbaas.instance.actions.stop')"
          :waiting-label="t('dbaas.instance.stop.stopping')"
          @click="stop"
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
