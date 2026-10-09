<script>
import CreateEditView from '@shell/mixins/create-edit-view';
import CruResource from '@shell/components/CruResource';
import NameNsDescription from '@shell/components/form/NameNsDescription';
import LabeledSelect from '@shell/components/form/LabeledSelect';
import { PLAN_QUOTA_GIB } from '../types';

export default {
  name: 'RegistryEdit',

  emits: ['input'],

  components: {
    CruResource, NameNsDescription, LabeledSelect
  },

  mixins: [CreateEditView],

  props: {
    mode: {
      type:    String,
      default: 'create',
    },
    value: {
      type:     Object,
      required: true,
    },
  },

  data() {
    if (!this.value.spec) {
      this.value.spec = { plan: 'starter' };
    } else if (!this.value.spec.plan) {
      this.value.spec.plan = 'starter';
    }

    return {};
  },

  computed: {
    planOptions() {
      return Object.entries(PLAN_QUOTA_GIB).map(([plan, gib]) => ({ label: `${ plan } — ${ gib } GiB`, value: plan }));
    },

    validationPassed() {
      return !!this.value.metadata?.name && !!this.value.metadata?.namespace && !!this.value.spec?.plan;
    },
  },
};
</script>

<template>
  <CruResource
    :done-route="doneRoute"
    :mode="mode"
    :resource="value"
    :subtypes="[]"
    :validation-passed="validationPassed"
    :errors="errors"
    @error="e=>errors = e"
    @finish="save"
    @cancel="done"
  >
    <NameNsDescription
      :value="value"
      :mode="mode"
      :description-hidden="true"
    />
    <div class="row mt-20">
      <div class="col span-6">
        <LabeledSelect
          v-model:value="value.spec.plan"
          :mode="mode"
          :options="planOptions"
          :label="t('registryUi.edit.plan')"
          :tooltip="t('registryUi.edit.planHelp')"
          :required="true"
        />
      </div>
    </div>
  </CruResource>
</template>
