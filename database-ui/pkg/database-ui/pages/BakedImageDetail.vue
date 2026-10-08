<script>
import Loading from '@shell/components/Loading';
import LabelValue from '@shell/components/LabelValue';
import LiveDate from '@shell/components/formatter/LiveDate';
import PercentageBar from '@shell/components/PercentageBar';
import CopyToClipboardText from '@shell/components/CopyToClipboardText';
import Tabbed from '@shell/components/Tabbed';
import Tab from '@shell/components/Tabbed/Tab';
import { BadgeState } from '@components/BadgeState';
import { Banner } from '@components/Banner';
import { RcButton } from '@components/RcButton';
import { HARVESTER_IMAGE, IMAGE_LABEL, IMAGES_ROUTE } from '../types';
import { IMAGE_STATE, formatBytes, imageState } from '../utils/baked-images';
import { imageUploads } from '../utils/image-uploads';

const STATE_COLOR = {
  [IMAGE_STATE.IMPORTING]: 'bg-info',
  [IMAGE_STATE.READY]:     'bg-success',
  [IMAGE_STATE.FAILED]:    'bg-error',
};

const DESCRIPTION = 'field.cattle.io/description';
const UPLOADED_FILE = 'harvesterhci.io/image-name';
const STORAGE_CLASS = 'harvesterhci.io/storageClassName';

// Details of one database image (a Harvester VirtualMachineImage), laid out
// like Harvester's own image page: Basics, Storage, Labels & Annotations.
export default {
  name: 'DBaaSBakedImageDetail',

  components: {
    Banner, BadgeState, CopyToClipboardText, LabelValue, LiveDate, Loading, PercentageBar, RcButton, Tab, Tabbed
  },

  async fetch() {
    try {
      await this.$store.dispatch(`${ this.inStore }/find`, { type: HARVESTER_IMAGE, id: this.id });
    } catch (e) {
      this.notFound = true;
    }
  },

  data() {
    return {
      notFound:       false,
      PROGRESS_COLOR: { 0: '--primary' },
      IMAGE_LABEL,
      DESCRIPTION,
      UPLOADED_FILE,
      STORAGE_CLASS,
    };
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    id() {
      return `${ this.$route.params.namespace }/${ this.$route.params.id }`;
    },

    // From the store, so it updates live (and disappears once deleted)
    image() {
      return this.$store.getters[`${ this.inStore }/byId`](HARVESTER_IMAGE, this.id);
    },

    meta() {
      return this.image?.metadata || {};
    },

    spec() {
      return this.image?.spec || {};
    },

    status() {
      return this.image?.status || {};
    },

    state() {
      const upload = imageUploads[this.id];
      const s = imageState(this.image);

      if (s.state === IMAGE_STATE.IMPORTING && upload) {
        return {
          ...s, progress: upload.progress, label: this.t('dbaas.images.state.uploading'), message: upload.error || s.message
        };
      }

      return { ...s, label: this.t(`dbaas.images.state.${ s.state }`) };
    },

    isUpload() {
      return this.spec.sourceType === 'upload';
    },

    sourceLabel() {
      return this.isUpload ? this.t('dbaas.images.form.sourceFile') : this.t('dbaas.images.form.sourceUrl');
    },

    storageParams() {
      return this.spec.storageClassParameters || {};
    },

    labels() {
      return Object.entries(this.meta.labels || {}).sort(([a], [b]) => a.localeCompare(b));
    },

    annotations() {
      return Object.entries(this.meta.annotations || {})
        .filter(([k]) => k !== 'kubectl.kubernetes.io/last-applied-configuration')
        .sort(([a], [b]) => a.localeCompare(b));
    },

    listLocation() {
      return { name: IMAGES_ROUTE, params: { cluster: this.$route.params.cluster } };
    },

    canDelete() {
      return !!this.image?.links?.remove || !!this.image?.hasLink?.('remove');
    },
  },

  methods: {
    formatBytes,

    stateColor(state) {
      return STATE_COLOR[state] || 'bg-info';
    },

    dash(value) {
      return value === undefined || value === null || value === '' ? '—' : `${ value }`;
    },

    remove() {
      this.$store.dispatch(`${ this.inStore }/promptRemove`, [this.image]);
    },
  },

  watch: {
    // Leave the page once the image has been deleted
    image(neu, old) {
      if (old && !neu) {
        this.$router.replace(this.listLocation);
      }
    },
  },

};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <div v-else>
    <div class="mb-10">
      <router-link :to="listLocation">
        <i class="icon icon-chevron-left" /> {{ t('dbaas.images.title') }}
      </router-link>
    </div>

    <Banner
      v-if="notFound || !image"
      color="warning"
      :label="t('dbaas.images.detail.notFound', { id }, true)"
    />

    <template v-else>
      <header class="image-header mb-20">
        <div class="title">
          <h1>
            {{ t('dbaas.images.detail.title') }}: {{ spec.displayName || meta.name }}
            <BadgeState
              class="ml-10"
              :label="state.label"
              :color="stateColor(state.state)"
            />
          </h1>
        </div>
        <RcButton
          v-if="canDelete"
          variant="secondary"
          @click="remove"
        >
          <i class="icon icon-trash mr-5" /> {{ t('generic.delete') }}
        </RcButton>
      </header>

      <div class="summary mb-20">
        <LabelValue
          :name="t('tableHeaders.namespace')"
          :value="meta.namespace"
        />
        <LabelValue
          :name="t('dbaas.images.detail.resourceName')"
          :value="meta.name"
        />
        <div>
          <div class="text-label">
            {{ t('tableHeaders.age') }}
          </div>
          <LiveDate :value="meta.creationTimestamp" />
        </div>
      </div>

      <Banner
        v-if="state.state === 'failed' && state.message"
        color="error"
        :label="state.message"
      />
      <div
        v-if="state.state === 'importing'"
        class="progress mb-20"
      >
        <PercentageBar
          :model-value="state.progress"
          :show-percentage="true"
          :color-stops="PROGRESS_COLOR"
        />
      </div>

      <Tabbed :side-tabs="true">
        <Tab
          name="basics"
          :label="t('dbaas.instance.form.tabs.basics')"
          :weight="3"
        >
          <div class="details">
            <LabelValue
              :name="t('dbaas.images.tableHeaders.displayName')"
              :value="dash(spec.displayName)"
            />
            <LabelValue
              :name="t('dbaas.images.tableHeaders.osVersion')"
              :value="dash(meta.labels && meta.labels[IMAGE_LABEL.OS_VERSION])"
            />
            <LabelValue
              :name="t('dbaas.images.form.source')"
              :value="sourceLabel"
            />
            <LabelValue
              v-if="isUpload"
              :name="t('dbaas.images.detail.fileName')"
              :value="dash(meta.annotations && meta.annotations[UPLOADED_FILE])"
            />
            <div v-else>
              <div class="text-label">
                {{ t('dbaas.images.form.url') }}
              </div>
              <CopyToClipboardText
                v-if="spec.url"
                :text="spec.url"
              />
              <span v-else>—</span>
            </div>
            <LabelValue
              v-if="!isUpload"
              :name="t('dbaas.images.form.checksum')"
              :value="dash(spec.checksum)"
            />
            <LabelValue
              :name="t('dbaas.images.tableHeaders.size')"
              :value="dash(formatBytes(status.size))"
            />
            <LabelValue
              :name="t('dbaas.images.detail.virtualSize')"
              :value="dash(formatBytes(status.virtualSize))"
            />
            <LabelValue
              :name="t('dbaas.images.detail.description')"
              :value="dash(meta.annotations && meta.annotations[DESCRIPTION])"
            />
          </div>
        </Tab>

        <Tab
          name="storage"
          :label="t('dbaas.images.detail.storage')"
          :weight="2"
        >
          <div class="details">
            <LabelValue
              :name="t('dbaas.images.tableHeaders.storageClass')"
              :value="dash((meta.annotations && meta.annotations[STORAGE_CLASS]) || status.storageClassName)"
            />
            <LabelValue
              :name="t('dbaas.images.detail.replicas')"
              :value="dash(storageParams.numberOfReplicas)"
            />
            <LabelValue
              :name="t('dbaas.images.detail.staleReplicaTimeout')"
              :value="dash(storageParams.staleReplicaTimeout)"
            />
            <LabelValue
              :name="t('dbaas.images.detail.migratable')"
              :value="dash(storageParams.migratable)"
            />
            <LabelValue
              :name="t('dbaas.images.detail.nodeSelector')"
              :value="dash(storageParams.nodeSelector)"
            />
            <LabelValue
              :name="t('dbaas.images.detail.diskSelector')"
              :value="dash(storageParams.diskSelector)"
            />
            <LabelValue
              :name="t('dbaas.images.detail.backend')"
              :value="dash(spec.backend)"
            />
          </div>
        </Tab>

        <Tab
          name="labels"
          label-key="generic.labelsAndAnnotations"
          :weight="1"
        >
          <h3>{{ t('dbaas.images.detail.labels') }}</h3>
          <dl class="kv mb-20">
            <template
              v-for="[k, v] in labels"
              :key="k"
            >
              <dt>{{ k }}</dt>
              <dd>{{ v }}</dd>
            </template>
          </dl>
          <h3>{{ t('dbaas.images.detail.annotations') }}</h3>
          <dl class="kv">
            <template
              v-for="[k, v] in annotations"
              :key="k"
            >
              <dt>{{ k }}</dt>
              <dd>{{ v }}</dd>
            </template>
          </dl>
        </Tab>
      </Tabbed>
    </template>
  </div>
</template>

<style lang="scss" scoped>
.image-header {
  display: flex;
  justify-content: space-between;
  align-items: center;

  h1 {
    display: flex;
    align-items: center;
  }
}

.summary {
  display: flex;
  gap: 60px;
}

.progress {
  max-width: 420px;
}

.details {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 20px 40px;
}

.kv {
  display: grid;
  grid-template-columns: minmax(200px, 360px) 1fr;
  row-gap: 8px;
  column-gap: 20px;
  margin: 0;

  dt {
    color: var(--input-label);
    word-break: break-all;
  }

  dd {
    margin: 0;
    word-break: break-all;
  }
}
</style>
