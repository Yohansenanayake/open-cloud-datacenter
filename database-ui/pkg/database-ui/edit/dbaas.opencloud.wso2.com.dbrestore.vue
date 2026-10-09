<script>
import CreateEditView from '@shell/mixins/create-edit-view';
import FormValidation from '@shell/mixins/form-validation';
import CruResource from '@shell/components/CruResource';
import LabeledSelect from '@shell/components/form/LabeledSelect';
import UnitInput from '@shell/components/form/UnitInput';
import Loading from '@shell/components/Loading';
import { LabeledInput } from '@components/Form/LabeledInput';
import { Checkbox } from '@components/Form/Checkbox';
import { Banner } from '@components/Banner';
import { NETWORK_ATTACHMENT } from '@shell/config/types';
import DBInstanceState from '../formatters/DBInstanceState.vue';
import DBaaSRelatedLink from '../formatters/DBaaSRelatedLink.vue';
import { DBAAS, RESTORE_QUERY } from '../types';
import { BACKUP_DEFAULTS, INSTANCE_CLASSES } from '../config/catalog';
import { RESTORE_STEPS } from '../utils/dbrestore';
import { timeUntil } from '../utils/time';
import {
  validateBackupWindow, validateInstanceName, validateRequired, validateRetainCount, validateStorage
} from '../utils/dbinstance-validation';
import { databaseNetworkOptions } from '../utils/networks';

// Restore a snapshot into a new DBInstance (create), or follow a restore's
// progress (view). The DBRestore spec is immutable, so there is no edit mode.
export default {
  name: 'EditDBRestore',

  emits: ['input'],

  inheritAttrs: false,

  components: {
    Banner,
    Checkbox,
    CruResource,
    DBaaSRelatedLink,
    DBInstanceState,
    LabeledInput,
    LabeledSelect,
    Loading,
    UnitInput,
  },

  mixins: [CreateEditView, FormValidation],

  async fetch() {
    const canList = (type) => this.$store.getters[`${ this.inStore }/canList`](type);
    const findAll = (type) => (canList(type) ? this.$store.dispatch(`${ this.inStore }/findAll`, { type }) : []);

    const [snapshots, instances, networks] = await Promise.all([
      findAll(DBAAS.SNAPSHOT),
      findAll(DBAAS.INSTANCE),
      this.isCreate ? findAll(NETWORK_ATTACHMENT) : [],
      this.isCreate ? findAll(DBAAS.RESTORE) : [],
    ]);

    this.snapshots = snapshots;
    this.instances = instances;
    this.canListNetworks = canList(NETWORK_ATTACHMENT);
    this.networks = networks;

    if (this.isCreate) {
      this.prefillFromQuery();
    }
  },

  data() {
    this.value.spec = this.value.spec || {};
    this.value.spec.snapshotRef = this.value.spec.snapshotRef || { name: '' };

    return {
      BACKUP_DEFAULTS,
      snapshots:       [],
      instances:       [],
      networks:        [],
      canListNetworks: true,
      snapshotId:      '',
      // A retried restore already carries all settings; selecting its
      // snapshot must not overwrite them with the snapshot's hints
      prefilled:       false,
      fvFormRuleSets:  [
        { path: 'spec.snapshotRef.name', rules: ['required'] },
        { path: 'spec.targetInstanceName', rules: ['targetName'] },
        { path: 'spec.dbInstanceClass', rules: ['required'] },
        { path: 'spec.allocatedStorage', rules: ['storage'] },
        { path: 'spec.networkRef', rules: ['required'] },
        { path: 'spec.backup.automated.preferredWindowUTC', rules: ['backupWindow'] },
        { path: 'spec.backup.automated.retainCount', rules: ['retainCount'] },
      ],
    };
  },

  created() {
    this.registerBeforeHook(this.prepareForSave, 'prepareForSave');
    // After creating, follow the restore on its own page rather than the list
    this.registerAfterHook(() => {
      this.doneLocationOverride = this.value.detailLocation;
    }, 'showProgress');
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    // --- create ------------------------------------------------------------

    snapshotOptions() {
      return this.snapshots
        .filter((s) => s.isReady && !s.metadata?.deletionTimestamp)
        .map((s) => ({
          label: this.t('dbaas.restore.form.snapshotOption', {
            id: s.id, source: s.sourceName, date: s.completionTime ? new Date(s.completionTime).toLocaleString() : '—'
          }, true),
          value: s.id,
        }))
        .sort((a, b) => a.label.localeCompare(b.label));
    },

    selectedSnapshot() {
      return this.snapshots.find((s) => s.id === this.snapshotId) || null;
    },

    inherited() {
      return this.selectedSnapshot?.source || {};
    },

    minStorage() {
      return this.selectedSnapshot?.sizeGiB || 1;
    },

    classOptions() {
      const options = INSTANCE_CLASSES.map((c) => ({
        label: this.t('dbaas.instance.form.classOption', {
          name: c.name, cpu: c.cpu, memory: c.memoryGiB, connections: c.maxConnections
        }, true),
        value: c.name,
      }));
      const current = this.value.spec.dbInstanceClass;

      if (current && !INSTANCE_CLASSES.find((c) => c.name === current)) {
        options.unshift({ label: current, value: current });
      }

      return options;
    },

    networkOptions() {
      return databaseNetworkOptions(this.networks);
    },

    backupEnabled: {
      get() {
        return !!this.value.spec.backup;
      },
      set(enabled) {
        if (enabled) {
          this.value.spec.backup = { automated: { ...BACKUP_DEFAULTS } };
        } else {
          delete this.value.spec.backup;
        }
      },
    },

    automatedBackup() {
      return this.value.spec.backup?.automated;
    },

    fvExtraRules() {
      const wrap = (fn) => (val) => {
        const problem = fn(val);

        return problem ? this.t(problem.key, problem.args || {}) : undefined;
      };

      return {
        required:     wrap(validateRequired),
        storage:      wrap((val) => validateStorage(val, this.minStorage)),
        backupWindow: wrap(validateBackupWindow),
        retainCount:  wrap(validateRetainCount),
        targetName:   (val) => {
          if (!this.isCreate) {
            return undefined;
          }

          const problem = validateInstanceName(val);

          if (problem) {
            return this.t(problem.key, problem.args || {});
          }

          const namespace = this.value.metadata?.namespace;

          if (this.instances.find((i) => i.namespace === namespace && i.name === val)) {
            return this.t('dbaas.restore.validation.targetExists', { name: val });
          }

          return undefined;
        },
      };
    },

    // --- view --------------------------------------------------------------

    steps() {
      const current = this.value.stepIndex;
      const failed = this.value.isFailed;
      const succeeded = this.value.isSucceeded;

      return RESTORE_STEPS.map((stage, i) => {
        let state = 'pending';

        if (succeeded || i < current) {
          state = 'done';
        } else if (i === current) {
          state = failed ? 'failed' : 'current';
        }

        return {
          stage, state, label: this.t(`dbaas.restore.stage.${ stage }`)
        };
      });
    },

    deadlineRelative() {
      const until = timeUntil(this.value.deadline);

      return until ? this.t(until.key, until.args, true) : '';
    },

    resolved() {
      return this.value.status?.resolved || {};
    },
  },

  watch: {
    // Choosing a snapshot sets the restore's namespace and suggests the new
    // instance's settings from what the snapshot recorded about its source
    snapshotId() {
      const snapshot = this.selectedSnapshot;

      if (!snapshot) {
        return;
      }

      this.value.metadata.namespace = snapshot.namespace;
      this.value.spec.snapshotRef = { name: snapshot.name };

      if (this.prefilled) {
        this.prefilled = false;

        return;
      }

      const source = snapshot.source;
      const spec = this.value.spec;

      spec.allocatedStorage = Math.max(spec.allocatedStorage || 0, snapshot.sizeGiB || 0) || snapshot.sizeGiB;
      if (source.dbInstanceClass) {
        spec.dbInstanceClass = source.dbInstanceClass;
      }
      if (source.networkRef && this.networkOptions.find((o) => o.value === source.networkRef)) {
        spec.networkRef = source.networkRef;
      }
      if (source.backup) {
        spec.backup = JSON.parse(JSON.stringify(source.backup));
      } else if (!spec.backup) {
        spec.backup = { automated: { ...BACKUP_DEFAULTS } };
      }
    },
  },

  methods: {
    prefillFromQuery() {
      const query = this.$route.query || {};
      const retryId = query[RESTORE_QUERY.RETRY];

      if (retryId) {
        const failed = this.$store.getters[`${ this.inStore }/byId`](DBAAS.RESTORE, retryId);

        if (failed) {
          // Keep every setting of the failed attempt, including any the form
          // doesn't show (e.g. staticNetwork set through YAML)
          this.value.spec = JSON.parse(JSON.stringify(failed.spec));
          this.value.metadata.namespace = failed.namespace;
          this.prefilled = true;
          this.snapshotId = `${ failed.namespace }/${ failed.snapshotName }`;

          return;
        }
      }

      if (query[RESTORE_QUERY.SNAPSHOT]) {
        this.snapshotId = query[RESTORE_QUERY.SNAPSHOT];
      }
    },

    prepareForSave() {
      const meta = this.value.metadata;

      // The record's own name doesn't matter; derive it from the target
      delete meta.name;
      meta.generateName = `${ this.value.spec.targetInstanceName }-restore-`;

      const retain = this.value.spec.backup?.automated?.retainCount;

      if (typeof retain === 'string') {
        this.value.spec.backup.automated.retainCount = parseInt(retain, 10);
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
    <!-- ===== Create: restore a snapshot into a new instance ===== -->
    <template v-if="isCreate">
      <h3>{{ t('dbaas.restore.form.snapshotSection') }}</h3>
      <Banner
        v-if="!snapshotOptions.length"
        color="info"
        :label="t('dbaas.restore.form.noSnapshots')"
      />
      <div class="row mb-20">
        <div class="col span-6">
          <LabeledSelect
            v-model:value="snapshotId"
            :label="t('dbaas.restore.tableHeaders.snapshot')"
            :options="snapshotOptions"
            :searchable="true"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.snapshotRef.name')"
          />
        </div>
      </div>

      <template v-if="selectedSnapshot">
        <p class="text-muted mb-10">
          {{ t('dbaas.restore.form.inheritedHelp') }}
        </p>
        <dl class="details mb-30">
          <dt>{{ t('dbaas.snapshot.tableHeaders.source') }}</dt>
          <dd>
            <DBaaSRelatedLink
              :value="selectedSnapshot.sourceName"
              :row="selectedSnapshot"
              :col="{ formatterOpts: { link: 'sourceInstance' } }"
            />
          </dd>
          <dt>{{ t('dbaas.instance.tableHeaders.engine') }}</dt>
          <dd>{{ selectedSnapshot.engineDisplay || '—' }}</dd>
          <dt>{{ t('dbaas.instance.connection.dbName') }}</dt>
          <dd>{{ inherited.dbName || '—' }}</dd>
          <dt>{{ t('dbaas.instance.connection.username') }}</dt>
          <dd>{{ inherited.masterUsername || '—' }}</dd>
          <dt>{{ t('dbaas.instance.connection.port') }}</dt>
          <dd>{{ inherited.port || '—' }}</dd>
          <dt>{{ t('dbaas.instance.form.storageClass') }}</dt>
          <dd>{{ inherited.storageType || '—' }}</dd>
          <dt>{{ t('dbaas.snapshot.tableHeaders.size') }}</dt>
          <dd>{{ selectedSnapshot.sizeDisplay || '—' }}</dd>
        </dl>
      </template>

      <h3>{{ t('dbaas.restore.form.instanceSection') }}</h3>
      <div class="row mb-20">
        <div class="col span-6">
          <LabeledInput
            v-model:value="value.spec.targetInstanceName"
            :label="t('dbaas.restore.form.targetName')"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.targetInstanceName')"
          />
        </div>
        <div class="col span-6">
          <LabeledInput
            :value="value.metadata.namespace || '—'"
            :label="t('tableHeaders.namespace')"
            mode="view"
            :tooltip="t('dbaas.restore.form.namespaceTooltip')"
          />
        </div>
      </div>
      <div class="row mb-20">
        <div class="col span-6">
          <LabeledSelect
            v-model:value="value.spec.dbInstanceClass"
            :label="t('dbaas.instance.form.instanceClass')"
            :options="classOptions"
            :taggable="true"
            :searchable="true"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.dbInstanceClass')"
          />
        </div>
        <div class="col span-6">
          <UnitInput
            v-model:value="value.spec.allocatedStorage"
            :label="t('dbaas.instance.form.allocatedStorage')"
            suffix="GiB"
            :delay="0"
            :min="minStorage"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.allocatedStorage')"
            :tooltip="t('dbaas.restore.form.storageTooltip')"
          />
        </div>
      </div>
      <div class="row mb-10">
        <div class="col span-6">
          <LabeledSelect
            v-if="canListNetworks"
            v-model:value="value.spec.networkRef"
            :label="t('dbaas.instance.form.network')"
            :options="networkOptions"
            :searchable="true"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.networkRef')"
          />
          <LabeledInput
            v-else
            v-model:value="value.spec.networkRef"
            :label="t('dbaas.instance.form.network')"
            :placeholder="t('dbaas.instance.form.networkPlaceholder')"
            :required="true"
            :rules="fvGetAndReportPathRules('spec.networkRef')"
          />
        </div>
      </div>
      <p class="text-muted mb-20">
        {{ t('dbaas.instance.form.vlanOnly') }}
      </p>

      <div class="mb-20">
        <Checkbox
          v-model:value="backupEnabled"
          :label="t('dbaas.restore.form.backupEnabled')"
          :description="t('dbaas.instance.form.backupEnabledDescription')"
        />
      </div>
      <div
        v-if="automatedBackup"
        class="backup-automated"
      >
        <div class="mb-20">
          <Checkbox
            v-model:value="automatedBackup.enabled"
            :label="t('dbaas.instance.form.automatedBackupEnabled')"
            :description="t('dbaas.instance.form.automatedBackupDescription')"
          />
        </div>
        <div
          v-if="automatedBackup.enabled !== false"
          class="row mb-20"
        >
          <div class="col span-6">
            <LabeledInput
              v-model:value="automatedBackup.preferredWindowUTC"
              :label="t('dbaas.instance.form.backupWindow')"
              :placeholder="BACKUP_DEFAULTS.preferredWindowUTC"
              :rules="fvGetAndReportPathRules('spec.backup.automated.preferredWindowUTC')"
            />
          </div>
          <div class="col span-6">
            <LabeledInput
              v-model:value="automatedBackup.retainCount"
              type="number"
              :label="t('dbaas.instance.form.retainCount')"
              :rules="fvGetAndReportPathRules('spec.backup.automated.retainCount')"
            />
          </div>
        </div>
      </div>

      <Banner
        color="info"
        :label="t('dbaas.restore.form.credentialsInfo')"
      />
    </template>

    <!-- ===== View: restore progress ===== -->
    <template v-else>
      <ol class="restore-steps mb-20">
        <li
          v-for="step in steps"
          :key="step.stage"
          :class="step.state"
        >
          <i
            class="icon"
            :class="{
              'icon-checkmark': step.state === 'done',
              'icon-spinner icon-spin': step.state === 'current',
              'icon-error': step.state === 'failed',
              'icon-dot-open': step.state === 'pending',
            }"
          />
          {{ step.label }}
        </li>
      </ol>

      <Banner
        v-if="value.stateMessages.length"
        :color="value.isFailed ? 'error' : 'info'"
        :label="value.stateMessages[0].message"
      />
      <p
        v-if="deadlineRelative"
        class="text-muted mb-20"
      >
        {{ t('dbaas.restore.view.timesOut', { when: deadlineRelative }, true) }}
      </p>

      <dl class="details mb-30">
        <dt>{{ t('tableHeaders.state') }}</dt>
        <dd><DBInstanceState :row="value" /></dd>
        <dt>{{ t('dbaas.restore.tableHeaders.snapshot') }}</dt>
        <dd>
          <DBaaSRelatedLink
            :value="value.snapshotName"
            :row="value"
            :col="{ formatterOpts: { link: 'snapshot', note: 'snapshotMissingNote' } }"
          />
        </dd>
        <dt>{{ t('dbaas.snapshot.tableHeaders.source') }}</dt>
        <dd>
          <DBaaSRelatedLink
            :value="value.sourceInstanceName"
            :row="value"
            :col="{ formatterOpts: { link: 'sourceInstance', note: 'sourceMissingNote' } }"
          />
        </dd>
        <dt>{{ t('dbaas.restore.tableHeaders.target') }}</dt>
        <dd>
          <DBaaSRelatedLink
            :value="value.targetName"
            :row="value"
            :col="{ formatterOpts: { link: 'targetInstance', note: 'targetMissingNote' } }"
          />
        </dd>
        <dt>{{ t('dbaas.instance.form.instanceClass') }}</dt>
        <dd>{{ value.spec.dbInstanceClass || '—' }}</dd>
        <dt>{{ t('dbaas.instance.form.allocatedStorage') }}</dt>
        <dd>{{ value.spec.allocatedStorage ? `${ value.spec.allocatedStorage } GiB` : '—' }}</dd>
        <dt>{{ t('dbaas.instance.form.network') }}</dt>
        <dd>{{ value.spec.networkRef || '—' }}</dd>
        <dt>{{ t('dbaas.restore.form.backupEnabled') }}</dt>
        <dd>{{ value.spec.backup ? t('generic.yes') : t('generic.no') }}</dd>
      </dl>

      <h3>{{ t('dbaas.restore.view.restored') }}</h3>
      <dl class="details">
        <dt>{{ t('dbaas.instance.tableHeaders.engine') }}</dt>
        <dd>{{ resolved.engineVersion ? `PostgreSQL ${ resolved.engineVersion }` : '—' }}</dd>
        <dt>{{ t('dbaas.instance.connection.dbName') }}</dt>
        <dd>{{ resolved.dbName || '—' }}</dd>
        <dt>{{ t('dbaas.instance.connection.username') }}</dt>
        <dd>{{ resolved.masterUsername || '—' }}</dd>
        <dt>{{ t('dbaas.instance.connection.port') }}</dt>
        <dd>{{ resolved.port || '—' }}</dd>
        <dt>{{ t('dbaas.instance.form.storageClass') }}</dt>
        <dd>{{ resolved.storageType || '—' }}</dd>
      </dl>
    </template>
  </CruResource>
</template>

<style lang="scss" scoped>
.details {
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

.backup-automated {
  margin-left: 30px;
}

.restore-steps {
  display: flex;
  gap: 30px;
  list-style: none;
  padding: 0;
  margin: 0;

  li {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--muted);

    // Fixed square box, so the spinner rotates around its own centre
    .icon {
      width: 1em;
      height: 1em;
      line-height: 1;
      display: inline-flex;
      align-items: center;
      justify-content: center;
    }

    &.done {
      color: var(--success);
    }

    &.current {
      color: var(--info);
      font-weight: bold;
    }

    &.failed {
      color: var(--error);
      font-weight: bold;
    }
  }
}
</style>
