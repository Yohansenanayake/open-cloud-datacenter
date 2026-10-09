<script>
import CreateEditView from '@shell/mixins/create-edit-view';
import FormValidation from '@shell/mixins/form-validation';
import CruResource from '@shell/components/CruResource';
import NameNsDescription from '@shell/components/form/NameNsDescription';
import Tabbed from '@shell/components/Tabbed';
import Tab from '@shell/components/Tabbed/Tab';
import LabeledSelect from '@shell/components/form/LabeledSelect';
import UnitInput from '@shell/components/form/UnitInput';
import Labels from '@shell/components/form/Labels';
import Loading from '@shell/components/Loading';
import DBInstanceConnection from '../components/DBInstanceConnection.vue';
import DBInstanceEvents from '../components/DBInstanceEvents.vue';
import DBInstanceBackups from '../components/DBInstanceBackups.vue';
import DBInstanceRestoredFrom from '../components/DBInstanceRestoredFrom.vue';
import DBInstanceImageBanner from '../components/DBInstanceImageBanner.vue';
import DBInstanceImageInfo from '../components/DBInstanceImageInfo.vue';
import { LabeledInput } from '@components/Form/LabeledInput';
import { Checkbox } from '@components/Form/Checkbox';
import { Banner } from '@components/Banner';
import { _CREATE, _VIEW } from '@shell/config/query-params';
import { NETWORK_ATTACHMENT, STORAGE_CLASS } from '@shell/config/types';
import { DBAAS } from '../types';
import { BACKUP_DEFAULTS, ENGINE_VERSIONS, INSTANCE_CLASSES, OPERATOR_DEFAULTS } from '../config/catalog';
import {
  defaultDBName, validateBackupWindow, validateDBName, validateInstanceName,
  validateMasterUsername, validatePort, validateRequired, validateRetainCount, validateStorage
} from '../utils/dbinstance-validation';
import { databaseNetworkOptions } from '../utils/networks';

// Optional spec fields the operator defaults when absent. Empty values are
// removed before saving so the operator default applies.
const OPTIONAL_SPEC_FIELDS = ['engineVersion', 'dbName', 'masterUsername', 'port', 'storageType'];

// Create/edit/view form for a DBInstance, laid out like Harvester's VM form:
// name and namespace on top, settings in side tabs. Inert API fields
// (yohan-docs/inert-api-fields.md) and dev-only fields are not offered.
export default {
  name: 'EditDBInstance',

  emits: ['input'],

  inheritAttrs: false,

  components: {
    Banner,
    Checkbox,
    CruResource,
    DBInstanceBackups,
    DBInstanceConnection,
    DBInstanceRestoredFrom,
    DBInstanceImageBanner,
    DBInstanceImageInfo,
    DBInstanceEvents,
    LabeledInput,
    LabeledSelect,
    Labels,
    Loading,
    NameNsDescription,
    Tab,
    Tabbed,
    UnitInput,
  },

  mixins: [CreateEditView, FormValidation],

  async fetch() {
    const inStore = this.$store.getters['currentProduct']?.inStore || 'cluster';
    const canList = (type) => this.$store.getters[`${ inStore }/canList`](type);

    const [networks, storageClasses] = await Promise.all([
      canList(NETWORK_ATTACHMENT) ? this.$store.dispatch(`${ inStore }/findAll`, { type: NETWORK_ATTACHMENT }) : [],
      canList(STORAGE_CLASS) ? this.$store.dispatch(`${ inStore }/findAll`, { type: STORAGE_CLASS }) : [],
      this.loadSchemaFields(inStore),
    ]);

    this.canListNetworks = canList(NETWORK_ATTACHMENT);
    this.networks = networks;
    this.storageClasses = storageClasses;
  },

  data() {
    this.value.spec = this.value.spec || {};

    return {
      OPERATOR_DEFAULTS,
      BACKUP_DEFAULTS,
      networks:        [],
      storageClasses:  [],
      canListNetworks: true,
      hasBackupField:  !!this.value.spec.backup,
      activeTab:       null,
      // Size when the form opened; storage can only grow from here
      originalStorage: this.mode === _CREATE ? 1 : (this.value.spec.allocatedStorage || 1),
      fvFormRuleSets:  [
        { path: 'metadata.name', rules: ['instanceName'] },
        { path: 'spec.dbInstanceClass', rules: ['required'] },
        { path: 'spec.allocatedStorage', rules: ['storage'] },
        { path: 'spec.networkRef', rules: ['required'] },
        { path: 'spec.dbName', rules: ['dbName'] },
        { path: 'spec.masterUsername', rules: ['masterUsername'] },
        { path: 'spec.port', rules: ['port'] },
        { path: 'spec.backup.automated.preferredWindowUTC', rules: ['backupWindow'] },
        { path: 'spec.backup.automated.retainCount', rules: ['retainCount'] },
      ],
    };
  },

  created() {
    this.registerBeforeHook(this.cleanOptionalFields, 'cleanOptionalFields');
  },

  computed: {
    // Fields fixed once the operator has reconciled the instance
    createOnlyMode() {
      return this.isCreate ? this.mode : _VIEW;
    },

    classOptions() {
      const options = INSTANCE_CLASSES.map((c) => ({
        label: this.t('dbaas.instance.form.classOption', {
          name: c.name, cpu: c.cpu, memory: c.memoryGiB, connections: c.maxConnections
        }),
        value: c.name,
      }));
      const current = this.value.spec.dbInstanceClass;

      // Keep a class the catalog doesn't know (custom operator config, or typed by hand)
      if (current && !INSTANCE_CLASSES.find((c) => c.name === current)) {
        options.unshift({ label: current, value: current });
      }

      return options;
    },

    engineOptions() {
      const versions = [...ENGINE_VERSIONS];
      const current = this.value.spec.engineVersion;

      if (current && !versions.includes(current)) {
        versions.unshift(current);
      }

      return [
        { label: this.t('dbaas.instance.form.engineDefault'), value: '' },
        ...versions.map((v) => ({ label: this.t('dbaas.instance.form.engineOption', { version: v }), value: v })),
      ];
    },

    storageClassOptions() {
      return [
        { label: this.t('dbaas.instance.form.storageClassDefault'), value: '' },
        ...this.storageClasses.map((sc) => ({ label: sc.name, value: sc.name })).sort((a, b) => a.label.localeCompare(b.label)),
      ];
    },

    networkOptions() {
      const options = databaseNetworkOptions(this.networks);
      const current = this.value.spec.networkRef;

      // Keep the network of an existing instance, even one no longer offered
      if (current && !options.find((o) => o.value === current)) {
        options.unshift({ label: current, value: current });
      }

      return options;
    },

    dbNamePlaceholder() {
      const name = this.value.metadata?.name;

      return name ? this.t('dbaas.instance.form.dbNamePlaceholder', { name: defaultDBName(name) }) : '';
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

    deletionBlockedMessage() {
      return this.value.stateMessages.map((m) => m.message).join(' ');
    },

    isResizable() {
      return this.isEdit && !!this.value.status?.appliedSpec;
    },

    fvExtraRules() {
      const wrap = (fn) => (val) => {
        const problem = fn(val);

        return problem ? this.t(problem.key, problem.args || {}) : undefined;
      };

      return {
        // The name is fixed after creation, so only check it when creating
        instanceName:   wrap((val) => (this.isCreate ? validateInstanceName(val) : undefined)),
        required:       wrap(validateRequired),
        storage:        wrap((val) => validateStorage(val, this.originalStorage)),
        dbName:         wrap(validateDBName),
        masterUsername: wrap(validateMasterUsername),
        port:           wrap(validatePort),
        backupWindow:   wrap(validateBackupWindow),
        retainCount:    wrap(validateRetainCount),
      };
    },
  },

  methods: {
    // spec.backup is only offered where the deployed CRD has it
    async loadSchemaFields(inStore) {
      try {
        const schema = this.$store.getters[`${ inStore }/schemaFor`](DBAAS.INSTANCE);

        await schema?.fetchResourceFields?.();
        this.hasBackupField = this.hasBackupField || this.$store.getters[`${ inStore }/pathExistsInSchema`](DBAAS.INSTANCE, 'spec.backup');
      } catch (e) {
        console.warn('Unable to read DBInstance schema fields', e); // eslint-disable-line no-console
      }
    },

    cleanOptionalFields() {
      const spec = this.value.spec;

      OPTIONAL_SPEC_FIELDS.forEach((field) => {
        if (spec[field] === '' || spec[field] === null || spec[field] === undefined) {
          delete spec[field];
        }
      });

      // Number inputs can hand back strings; the CRD expects integers
      if (typeof spec.port === 'string') {
        spec.port = parseInt(spec.port, 10);
      }
      if (typeof spec.backup?.automated?.retainCount === 'string') {
        spec.backup.automated.retainCount = parseInt(spec.backup.automated.retainCount, 10);
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
    @error="e => errors = e"
    @finish="save"
    @cancel="done"
  >
    <NameNsDescription
      :value="value"
      :mode="mode"
      :namespaced="true"
      :rules="{ name: fvGetAndReportPathRules('metadata.name'), namespace: [], description: [] }"
      @update:value="$emit('input', $event)"
    />

    <DBInstanceRestoredFrom
      v-if="isView && value.spec.restoredFrom"
      :value="value"
    />

    <DBInstanceImageBanner
      v-if="isView && !value.isDeleting"
      :value="value"
    />

    <Banner
      v-if="value.deletionProblem"
      color="error"
      :label="t('dbaas.instance.delete.blockedBanner', { message: deletionBlockedMessage }, true)"
    />
    <Banner
      v-else-if="value.isDeleting"
      color="info"
      :label="t('dbaas.instance.delete.deletingBanner')"
    />

    <Banner
      v-if="isResizable && !value.isDeleting"
      color="warning"
    >
      {{ t('dbaas.instance.form.resizeWarning') }}
    </Banner>

    <Tabbed
      :side-tabs="true"
      class="mt-15"
      @changed="({ selectedName }) => activeTab = selectedName"
    >
      <Tab
        v-if="isView"
        name="connection"
        :label="t('dbaas.instance.form.tabs.connection')"
        :weight="6"
      >
        <DBInstanceConnection :value="value" />
      </Tab>

      <Tab
        name="basics"
        :label="t('dbaas.instance.form.tabs.basics')"
        :weight="5"
      >
        <div class="row mb-20">
          <div class="col span-6">
            <LabeledSelect
              v-model:value="value.spec.dbInstanceClass"
              :label="t('dbaas.instance.form.instanceClass')"
              :options="classOptions"
              :mode="mode"
              :taggable="true"
              :searchable="true"
              :required="true"
              :rules="fvGetAndReportPathRules('spec.dbInstanceClass')"
              :tooltip="t('dbaas.instance.form.instanceClassTooltip')"
            />
          </div>
          <div class="col span-6">
            <LabeledSelect
              v-model:value="value.spec.engineVersion"
              :label="t('dbaas.instance.form.engineVersion')"
              :options="engineOptions"
              :mode="createOnlyMode"
              :tooltip="t('dbaas.instance.form.engineVersionTooltip')"
            />
          </div>
        </div>
        <div class="row mb-20">
          <div class="col span-6">
            <UnitInput
              v-model:value="value.spec.allocatedStorage"
              :label="t('dbaas.instance.form.allocatedStorage')"
              suffix="GiB"
              :delay="0"
              :min="originalStorage"
              :mode="mode"
              :required="true"
              :rules="fvGetAndReportPathRules('spec.allocatedStorage')"
              :tooltip="t('dbaas.instance.form.allocatedStorageTooltip')"
            />
          </div>
          <div
            v-if="storageClasses.length || value.spec.storageType"
            class="col span-6"
          >
            <LabeledSelect
              v-model:value="value.spec.storageType"
              :label="t('dbaas.instance.form.storageClass')"
              :options="storageClassOptions"
              :mode="createOnlyMode"
              :tooltip="t('dbaas.instance.form.storageClassTooltip')"
            />
          </div>
        </div>
        <DBInstanceImageInfo
          v-if="isView"
          class="mt-30"
          :value="value"
        />
      </Tab>

      <Tab
        name="database"
        :label="t('dbaas.instance.form.tabs.database')"
        :weight="4"
      >
        <div class="row mb-20">
          <div class="col span-6">
            <LabeledInput
              v-model:value="value.spec.dbName"
              :label="t('dbaas.instance.form.dbName')"
              :placeholder="dbNamePlaceholder"
              :mode="createOnlyMode"
              :rules="fvGetAndReportPathRules('spec.dbName')"
              :tooltip="t('dbaas.instance.form.dbNameTooltip')"
            />
          </div>
          <div class="col span-6">
            <LabeledInput
              v-model:value="value.spec.masterUsername"
              :label="t('dbaas.instance.form.masterUsername')"
              :placeholder="t('dbaas.instance.form.defaultPlaceholder', { value: OPERATOR_DEFAULTS.masterUsername })"
              :mode="createOnlyMode"
              :rules="fvGetAndReportPathRules('spec.masterUsername')"
              :tooltip="t('dbaas.instance.form.masterUsernameTooltip')"
            />
          </div>
        </div>
        <div class="row mb-20">
          <div class="col span-6">
            <LabeledInput
              v-model:value="value.spec.port"
              type="number"
              :label="t('dbaas.instance.form.port')"
              :placeholder="t('dbaas.instance.form.defaultPlaceholder', { value: OPERATOR_DEFAULTS.port })"
              :mode="createOnlyMode"
              :rules="fvGetAndReportPathRules('spec.port')"
            />
          </div>
        </div>
        <Banner
          color="info"
          :label="t('dbaas.instance.form.credentialsInfo')"
        />
      </Tab>

      <Tab
        name="network"
        :label="t('dbaas.instance.form.tabs.network')"
        :weight="3"
      >
        <div class="row mb-20">
          <div class="col span-6">
            <LabeledSelect
              v-if="canListNetworks"
              v-model:value="value.spec.networkRef"
              :label="t('dbaas.instance.form.network')"
              :options="networkOptions"
              :mode="createOnlyMode"
              :searchable="true"
              :required="true"
              :rules="fvGetAndReportPathRules('spec.networkRef')"
              :tooltip="t('dbaas.instance.form.networkTooltip')"
            />
            <LabeledInput
              v-else
              v-model:value="value.spec.networkRef"
              :label="t('dbaas.instance.form.network')"
              :placeholder="t('dbaas.instance.form.networkPlaceholder')"
              :mode="createOnlyMode"
              :required="true"
              :rules="fvGetAndReportPathRules('spec.networkRef')"
              :tooltip="t('dbaas.instance.form.networkTooltip')"
            />
          </div>
        </div>
        <p
          v-if="isCreate"
          class="text-muted mb-20"
        >
          {{ t('dbaas.instance.form.vlanOnly') }}
        </p>
        <Banner
          v-if="isCreate"
          color="info"
          :label="t('dbaas.instance.form.networkInfo')"
        />
      </Tab>

      <Tab
        v-if="hasBackupField"
        name="backup"
        :label="t('dbaas.instance.form.tabs.backup')"
        :weight="2"
      >
        <DBInstanceBackups
          v-if="isView"
          :value="value"
        />
        <template v-else>
          <!-- Two settings: whether the instance supports backups at all (fixed
               at creation), and the daily schedule (can change any time) -->
          <div class="mb-20">
            <Checkbox
              v-model:value="backupEnabled"
              :label="t('dbaas.instance.form.backupEnabled')"
              :description="t('dbaas.instance.form.backupEnabledDescription')"
              :mode="createOnlyMode"
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
                :mode="mode"
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
                  :mode="mode"
                  :rules="fvGetAndReportPathRules('spec.backup.automated.preferredWindowUTC')"
                  :tooltip="t('dbaas.instance.form.backupWindowTooltip')"
                />
              </div>
              <div class="col span-6">
                <LabeledInput
                  v-model:value="automatedBackup.retainCount"
                  type="number"
                  :label="t('dbaas.instance.form.retainCount')"
                  :mode="mode"
                  :rules="fvGetAndReportPathRules('spec.backup.automated.retainCount')"
                  :tooltip="t('dbaas.instance.form.retainCountTooltip')"
                />
              </div>
            </div>
          </div>
        </template>
      </Tab>

      <Tab
        name="advanced"
        :label="t('dbaas.instance.form.tabs.advanced')"
        :weight="1"
      >
        <Checkbox
          v-model:value="value.spec.deletionProtection"
          :label="t('dbaas.instance.form.deletionProtection')"
          :description="t('dbaas.instance.form.deletionProtectionDescription')"
          :mode="mode"
        />
      </Tab>

      <Tab
        v-if="isView"
        name="events"
        :label="t('dbaas.instance.form.tabs.events')"
        :weight="0"
      >
        <DBInstanceEvents
          v-if="activeTab === 'events'"
          :value="value"
        />
      </Tab>

      <Tab
        name="labels"
        label-key="generic.labelsAndAnnotations"
        :weight="-1"
      >
        <!-- Mounted only while visible: the annotation value boxes size
             themselves on mount, and measure 0 inside a hidden tab -->
        <Labels
          v-if="activeTab === 'labels'"
          :value="value"
          :mode="mode"
          :display-side-by-side="false"
        />
      </Tab>
    </Tabbed>
  </CruResource>
</template>

<style lang="scss" scoped>
// The automated schedule belongs to (and only exists with) backups
.backup-automated {
  margin-left: 30px;
}
</style>
