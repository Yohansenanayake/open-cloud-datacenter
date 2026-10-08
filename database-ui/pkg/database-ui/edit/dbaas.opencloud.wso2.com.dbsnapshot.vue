<script>
import CreateEditView from '@shell/mixins/create-edit-view';
import FormValidation from '@shell/mixins/form-validation';
import CruResource from '@shell/components/CruResource';
import LabeledSelect from '@shell/components/form/LabeledSelect';
import Loading from '@shell/components/Loading';
import LiveDate from '@shell/components/formatter/LiveDate';
import { LabeledInput } from '@components/Form/LabeledInput';
import { Banner } from '@components/Banner';
import DBInstanceState from '../formatters/DBInstanceState.vue';
import DBSnapshotSource from '../formatters/DBSnapshotSource.vue';
import DBSnapshotProgress from '../formatters/DBSnapshotProgress.vue';
import { DBAAS } from '../types';
import { defaultSnapshotName, validateSnapshotName } from '../utils/dbsnapshot';

// Take a manual snapshot (create) or show one (view). The spec is immutable,
// so there is no edit mode.
export default {
  name: 'EditDBSnapshot',

  emits: ['input'],

  inheritAttrs: false,

  components: {
    Banner, CruResource, DBInstanceState, LiveDate, DBSnapshotProgress, DBSnapshotSource, LabeledInput, LabeledSelect, Loading
  },

  mixins: [CreateEditView, FormValidation],

  async fetch() {
    if (this.$store.getters[`${ this.inStore }/canList`](DBAAS.INSTANCE)) {
      this.instances = await this.$store.dispatch(`${ this.inStore }/findAll`, { type: DBAAS.INSTANCE });
    }
  },

  data() {
    this.value.spec = this.value.spec || {};
    this.value.spec.sourceInstanceRef = this.value.spec.sourceInstanceRef || { name: '' };

    return {
      instances:      [],
      sourceId:       '',
      fvFormRuleSets: [
        { path: 'metadata.name', rules: ['snapshotName'] },
        { path: 'spec.sourceInstanceRef.name', rules: ['required'] },
      ],
    };
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    // Instances that can be snapshotted are selectable; others say why not
    sourceOptions() {
      return this.instances
        .filter((i) => !i.isDeleting)
        .map((i) => {
          const reason = i.takeSnapshotBlockedReason;

          return {
            label:    reason ? `${ i.id } (${ reason })` : i.id,
            value:    i.id,
            disabled: !!reason,
          };
        })
        .sort((a, b) => a.label.localeCompare(b.label));
    },

    hasEligibleSource() {
      return this.sourceOptions.some((o) => !o.disabled);
    },

    source() {
      return this.value.source || {};
    },

    fvExtraRules() {
      return {
        snapshotName: (val) => {
          const problem = this.isCreate ? validateSnapshotName(val) : undefined;

          return problem ? this.t(problem.key, problem.args || {}) : undefined;
        },
        required: (val) => (val ? undefined : this.t('dbaas.instance.validation.required')),
      };
    },
  },

  watch: {
    // Picking the source sets the snapshot's namespace and suggests a name
    sourceId(id) {
      const instance = this.instances.find((i) => i.id === id);

      if (!instance) {
        return;
      }

      const previousDefault = this.value.spec.sourceInstanceRef.name ? defaultSnapshotName(this.value.spec.sourceInstanceRef.name) : '';

      this.value.metadata.namespace = instance.namespace;
      this.value.spec.sourceInstanceRef.name = instance.name;
      if (!this.value.metadata.name || this.value.metadata.name === previousDefault) {
        this.value.metadata.name = defaultSnapshotName(instance.name);
      }
    },
  },
};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <CruResource
    v-else
    :done-route="doneRoute"
    :mode="mode"
    :resource="value"
    :validation-passed="fvFormIsValid"
    :errors="fvUnreportedValidationErrors"
    :apply-hooks="applyHooks"
    :can-yaml="isView"
    @error="e => errors = e"
    @finish="save"
    @cancel="done"
  >
    <!-- Create: choose the source instance, then the name -->
    <template v-if="isCreate">
      <Banner
        v-if="!hasEligibleSource"
        color="info"
        :label="t('dbaas.snapshot.create.noEligible')"
      />
      <div class="row mb-20">
        <div class="col span-6">
          <LabeledSelect
            v-model:value="sourceId"
            :label="t('dbaas.snapshot.create.source')"
            :options="sourceOptions"
            :selectable="(o) => !o.disabled"
            :searchable="true"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.sourceInstanceRef.name')"
          />
        </div>
        <div class="col span-6">
          <LabeledInput
            v-model:value="value.metadata.name"
            :label="t('dbaas.snapshot.dialog.name')"
            :required="true"
            :rules="fvGetAndReportPathRules('metadata.name')"
          />
        </div>
      </div>
      <Banner
        color="info"
        :label="t('dbaas.snapshot.create.info')"
      />
    </template>

    <!-- View: what was backed up, and how it went -->
    <template v-else>
      <dl class="snapshot-details">
        <dt>{{ t('tableHeaders.state') }}</dt>
        <dd><DBInstanceState :row="value" /></dd>

        <dt>{{ t('dbaas.snapshot.tableHeaders.source') }}</dt>
        <dd>
          <DBSnapshotSource
            :value="value.sourceName"
            :row="value"
          />
        </dd>

        <dt>{{ t('dbaas.snapshot.tableHeaders.origin') }}</dt>
        <dd>{{ value.origin }}</dd>

        <dt>{{ t('dbaas.snapshot.tableHeaders.progress') }}</dt>
        <dd><DBSnapshotProgress :row="value" /></dd>

        <dt>{{ t('dbaas.snapshot.detail.started') }}</dt>
        <dd>
          <LiveDate
            v-if="value.startTime"
            :value="value.startTime"
            :add-suffix="true"
          />
          <span v-else>—</span>
        </dd>

        <dt>{{ t('dbaas.snapshot.tableHeaders.completed') }}</dt>
        <dd>
          <LiveDate
            v-if="value.completionTime"
            :value="value.completionTime"
            :add-suffix="true"
          />
          <span v-else>—</span>
        </dd>
      </dl>

      <h3 class="mt-30">
        {{ t('dbaas.snapshot.detail.contents') }}
      </h3>
      <dl class="snapshot-details">
        <dt>{{ t('dbaas.instance.tableHeaders.engine') }}</dt>
        <dd>{{ value.engineDisplay || '—' }}</dd>

        <dt>{{ t('dbaas.instance.connection.dbName') }}</dt>
        <dd>{{ source.dbName || '—' }}</dd>

        <dt>{{ t('dbaas.instance.connection.username') }}</dt>
        <dd>{{ source.masterUsername || '—' }}</dd>

        <dt>{{ t('dbaas.instance.connection.port') }}</dt>
        <dd>{{ source.port || '—' }}</dd>

        <dt>{{ t('dbaas.snapshot.tableHeaders.size') }}</dt>
        <dd>{{ value.sizeDisplay || '—' }}</dd>

        <dt>{{ t('dbaas.instance.form.storageClass') }}</dt>
        <dd>{{ source.storageType || '—' }}</dd>

        <dt>{{ t('dbaas.instance.form.instanceClass') }}</dt>
        <dd>{{ source.dbInstanceClass || '—' }}</dd>

        <dt>{{ t('dbaas.instance.form.network') }}</dt>
        <dd>{{ source.networkRef || '—' }}</dd>
      </dl>
    </template>
  </CruResource>
</template>

<style lang="scss" scoped>
.snapshot-details {
  display: grid;
  grid-template-columns: 180px 1fr;
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
</style>
