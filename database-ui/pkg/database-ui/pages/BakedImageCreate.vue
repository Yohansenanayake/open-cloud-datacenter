<script>
import Loading from '@shell/components/Loading';
import LabeledSelect from '@shell/components/form/LabeledSelect';
import PercentageBar from '@shell/components/PercentageBar';
import AsyncButton from '@shell/components/AsyncButton';
import { LabeledInput } from '@components/Form/LabeledInput';
import { RadioGroup } from '@components/Form/Radio';
import { Banner } from '@components/Banner';
import { NAMESPACE, STORAGE_CLASS } from '@shell/config/types';
import { exceptionToErrorsArray } from '@shell/utils/error';
import { HARVESTER_IMAGE, IMAGE_LABEL, IMAGES_ROUTE } from '../types';
import { BAKED_IMAGE_OS_VERSIONS, BAKED_IMAGE_REVISIONS } from '../config/catalog';
import { isBakedImage, mostUsedNamespace } from '../utils/baked-images';
import { imageUploads, uploadImageFile } from '../utils/image-uploads';

const SOURCE = { URL: 'download', FILE: 'upload' };
const DEFAULT_SC_ANNOTATION = 'storageclass.kubernetes.io/is-default-class';

// Upload a DBaaS baked image: creates a Harvester VirtualMachineImage labelled
// as a DBaaS image, downloaded from a URL by Harvester or uploaded from this
// browser. The admin chooses the namespace (the operator's imageNamespace is
// the admin's own setting).
export default {
  name: 'DBaaSBakedImageCreate',

  components: {
    AsyncButton, Banner, LabeledInput, LabeledSelect, Loading, PercentageBar, RadioGroup
  },

  async fetch() {
    const canList = (type) => this.$store.getters[`${ this.inStore }/canList`](type);
    const findAll = (type) => (canList(type) ? this.$store.dispatch(`${ this.inStore }/findAll`, { type }) : []);
    const [images, namespaces, storageClasses] = await Promise.all([findAll(HARVESTER_IMAGE), findAll(NAMESPACE), findAll(STORAGE_CLASS)]);

    this.images = images;
    this.namespaces = namespaces;
    this.storageClasses = storageClasses;

    const baked = images.filter(isBakedImage);

    this.namespace = mostUsedNamespace(baked) || 'default';
    this.storageClass = baked[0]?.metadata?.annotations?.['harvesterhci.io/storageClassName'] ||
      storageClasses.find((sc) => sc.metadata?.annotations?.[DEFAULT_SC_ANNOTATION] === 'true')?.name || '';
  },

  data() {
    return {
      SOURCE,
      images:         [],
      namespaces:     [],
      storageClasses: [],
      displayName:    '',
      osVersion:      '',
      namespace:      '',
      storageClass:   '',
      source:         SOURCE.URL,
      url:            '',
      checksum:       '',
      file:           null,
      errors:         [],
      uploadKey:      '',
    };
  },

  beforeUnmount() {
    window.removeEventListener('beforeunload', this.warnBeforeLeaving);
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    revisionOptions() {
      return BAKED_IMAGE_REVISIONS.map((r) => ({ label: r.name, value: r.name }));
    },

    osVersionOptions() {
      return BAKED_IMAGE_OS_VERSIONS;
    },

    namespaceOptions() {
      return this.namespaces.map((n) => n.name).sort();
    },

    storageClassOptions() {
      return this.storageClasses.map((sc) => sc.name).sort();
    },

    sourceOptions() {
      return [SOURCE.URL, SOURCE.FILE];
    },

    sourceLabels() {
      return [this.t('dbaas.images.form.sourceUrl'), this.t('dbaas.images.form.sourceFile')];
    },

    // Another image with this display name in this namespace would make the
    // operator's lookup ambiguous (if this is its image namespace)
    nameTaken() {
      const name = this.displayName.trim();

      return !!name && this.images.some((i) => i.metadata?.namespace === this.namespace && i.spec?.displayName === name);
    },

    validUrl() {
      return /^https?:\/\/\S+$/.test(this.url.trim());
    },

    canSave() {
      const sourceReady = this.source === SOURCE.URL ? this.validUrl : !!this.file;

      return !!this.displayName.trim() && !!this.osVersion && !!this.namespace && !this.nameTaken && sourceReady && !this.uploadKey;
    },

    upload() {
      return this.uploadKey ? imageUploads[this.uploadKey] : null;
    },

    listLocation() {
      return { name: IMAGES_ROUTE, params: { cluster: this.$route.params.cluster } };
    },
  },

  watch: {
    // Picking a known revision fills in its OS version
    displayName(name) {
      const known = BAKED_IMAGE_REVISIONS.find((r) => r.name === name);

      if (known) {
        this.osVersion = known.osVersion;
      }
    },
  },

  methods: {
    onFile(event) {
      this.file = event.target.files?.[0] || null;
    },

    warnBeforeLeaving(event) {
      event.preventDefault();
      event.returnValue = '';
    },

    async save(buttonDone) {
      this.errors = [];

      try {
        const annotations = {};

        if (this.storageClass) {
          annotations['harvesterhci.io/storageClassName'] = this.storageClass;
        }

        const image = await this.$store.dispatch(`${ this.inStore }/create`, {
          type:     HARVESTER_IMAGE,
          metadata: {
            generateName: 'image-',
            namespace:    this.namespace,
            labels:       { [IMAGE_LABEL.BAKED]: 'true', [IMAGE_LABEL.OS_VERSION]: this.osVersion },
            annotations,
          },
          spec: {
            displayName: this.displayName.trim(),
            sourceType:  this.source,
            url:         this.source === SOURCE.URL ? this.url.trim() : '',
            ...(this.source === SOURCE.URL && this.checksum.trim() ? { checksum: this.checksum.trim() } : {}),
          },
        });
        const saved = (await image.save()) || image;

        if (this.source === SOURCE.FILE) {
          // Stay here while the browser sends the file; leaving the tab would abort it
          this.uploadKey = `${ saved.metadata.namespace }/${ saved.metadata.name }`;
          window.addEventListener('beforeunload', this.warnBeforeLeaving);
          await uploadImageFile(this.$store, this.$route.params.cluster, saved, this.file);
          window.removeEventListener('beforeunload', this.warnBeforeLeaving);
        }

        buttonDone(true);
        this.$router.push(this.listLocation);
      } catch (e) {
        window.removeEventListener('beforeunload', this.warnBeforeLeaving);
        this.errors = exceptionToErrorsArray(e);
        buttonDone(false);
      }
    },
  },
};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <div v-else>
    <header class="mb-20">
      <div class="title">
        <h1>{{ t('dbaas.images.form.title') }}</h1>
      </div>
    </header>

    <Banner
      color="info"
      :label="t('dbaas.images.form.info')"
    />

    <div class="row mb-20">
      <div class="col span-6">
        <LabeledSelect
          v-model:value="displayName"
          :label="t('dbaas.images.tableHeaders.displayName')"
          :options="revisionOptions"
          :taggable="true"
          :searchable="true"
          :required="true"
          :tooltip="t('dbaas.images.form.displayNameTooltip')"
        />
        <p
          v-if="nameTaken"
          class="text-error mt-5"
        >
          {{ t('dbaas.images.form.nameTaken', { name: displayName, namespace }, true) }}
        </p>
      </div>
      <div class="col span-6">
        <LabeledSelect
          v-model:value="osVersion"
          :label="t('dbaas.images.tableHeaders.osVersion')"
          :options="osVersionOptions"
          :taggable="true"
          :searchable="true"
          :required="true"
        />
      </div>
    </div>

    <div class="row mb-20">
      <div class="col span-6">
        <LabeledSelect
          v-model:value="namespace"
          :label="t('tableHeaders.namespace')"
          :options="namespaceOptions"
          :searchable="true"
          :required="true"
          :tooltip="t('dbaas.images.form.namespaceTooltip')"
        />
      </div>
      <div class="col span-6">
        <LabeledSelect
          v-model:value="storageClass"
          :label="t('dbaas.images.tableHeaders.storageClass')"
          :options="storageClassOptions"
          :searchable="true"
        />
      </div>
    </div>

    <div class="mb-20">
      <RadioGroup
        v-model:value="source"
        name="image-source"
        :label="t('dbaas.images.form.source')"
        :options="sourceOptions"
        :labels="sourceLabels"
        :row="true"
      />
    </div>

    <div
      v-if="source === SOURCE.URL"
      class="row mb-20"
    >
      <div class="col span-6">
        <LabeledInput
          v-model:value="url"
          :label="t('dbaas.images.form.url')"
          placeholder="https://example.com/ubuntu-2404-postgres-v20261101.qcow2"
          :required="true"
        />
      </div>
      <div class="col span-6">
        <LabeledInput
          v-model:value="checksum"
          :label="t('dbaas.images.form.checksum')"
          :tooltip="t('dbaas.images.form.checksumTooltip')"
        />
      </div>
    </div>

    <div
      v-else
      class="mb-20"
    >
      <input
        type="file"
        accept=".qcow2,.img,.raw,.iso"
        :disabled="!!uploadKey"
        @change="onFile"
      >
      <div
        v-if="upload"
        class="upload-progress mt-10"
      >
        <span>{{ t('dbaas.images.form.uploading') }}</span>
        <PercentageBar
          :model-value="upload.progress"
          :show-percentage="true"
          :color-stops="{ 0: '--primary' }"
        />
      </div>
    </div>

    <Banner
      v-for="(err, i) in errors"
      :key="i"
      color="error"
      :label="err"
    />

    <div class="form-actions">
      <router-link
        class="btn role-secondary"
        :to="listLocation"
      >
        {{ t('generic.cancel') }}
      </router-link>
      <AsyncButton
        :action-label="source === SOURCE.FILE ? t('dbaas.images.form.uploadAction') : t('dbaas.images.form.createAction')"
        :waiting-label="t('dbaas.images.form.working')"
        :disabled="!canSave"
        @click="save"
      />
    </div>
  </div>
</template>

<style lang="scss" scoped>
.upload-progress {
  max-width: 420px;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 30px;
}
</style>
