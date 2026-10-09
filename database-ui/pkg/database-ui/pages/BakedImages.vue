<script>
import SortableTable from '@shell/components/SortableTable';
import Loading from '@shell/components/Loading';
import PercentageBar from '@shell/components/PercentageBar';
import { BadgeState } from '@components/BadgeState';
import { Banner } from '@components/Banner';
import { RcButton } from '@components/RcButton';
import { HARVESTER_IMAGE, IMAGE_LABEL, IMAGES_CREATE_ROUTE, IMAGES_DETAIL_ROUTE } from '../types';
import {
  IMAGE_STATE, duplicateDisplayNames, formatBytes, imageState, isBakedImage
} from '../utils/baked-images';
import { imageUploads } from '../utils/image-uploads';

const STATE_COLOR = {
  [IMAGE_STATE.IMPORTING]: 'bg-info',
  [IMAGE_STATE.READY]:     'bg-success',
  [IMAGE_STATE.FAILED]:    'bg-error',
};

// One colour for progress (PercentageBar turns red above 80% by default)
const PROGRESS_COLOR = { 0: '--primary' };

// DBaaS baked images (admin only): the Harvester VirtualMachineImages labelled
// as DBaaS images, in any namespace. The operator uses the ones in its
// configured image namespace whose display name matches its catalog; which
// namespace that is, is the cluster admin's operator setting.
export default {
  name: 'DBaaSBakedImages',

  components: {
    BadgeState, Banner, Loading, PercentageBar, RcButton, SortableTable
  },

  async fetch() {
    if (this.canList) {
      await this.$store.dispatch(`${ this.inStore }/findAll`, { type: HARVESTER_IMAGE });
    }
  },

  data() {
    return {
      PROGRESS_COLOR,
      headers: [
        {
          name: 'state', labelKey: 'tableHeaders.state', value: 'stateLabel', sort: ['stateLabel', 'displayName'], width: 150
        },
        {
          name: 'displayName', labelKey: 'dbaas.images.tableHeaders.displayName', value: 'displayName', sort: ['displayName']
        },
        {
          name: 'namespace', labelKey: 'tableHeaders.namespace', value: 'namespace', sort: ['namespace', 'displayName']
        },
        {
          name: 'osVersion', labelKey: 'dbaas.images.tableHeaders.osVersion', value: 'osVersion', sort: ['osVersion', 'displayName'], dashIfEmpty: true
        },
        {
          name: 'size', labelKey: 'dbaas.images.tableHeaders.size', value: 'size', sort: ['sizeBytes'], dashIfEmpty: true
        },
        {
          name: 'storageClass', labelKey: 'dbaas.images.tableHeaders.storageClass', value: 'storageClass', sort: ['storageClass'], dashIfEmpty: true
        },
        {
          name: 'age', labelKey: 'tableHeaders.age', value: 'created', sort: 'created:desc', formatter: 'LiveDate', width: 110
        },
        {
          name: 'actions', label: '', value: 'id', width: 60, align: 'right'
        },
      ],
    };
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    schema() {
      return this.$store.getters[`${ this.inStore }/schemaFor`](HARVESTER_IMAGE);
    },

    canList() {
      return !!this.$store.getters[`${ this.inStore }/canList`](HARVESTER_IMAGE);
    },

    canCreate() {
      return !!this.schema?.collectionMethods?.find((m) => m.toLowerCase() === 'post');
    },

    images() {
      return this.canList ? this.$store.getters[`${ this.inStore }/all`](HARVESTER_IMAGE).filter(isBakedImage) : [];
    },

    duplicates() {
      return duplicateDisplayNames(this.images);
    },

    rows() {
      return this.images.map((image) => {
        const id = `${ image.metadata.namespace }/${ image.metadata.name }`;
        const upload = imageUploads[id];
        const { state, progress, message } = imageState(image);
        // While this tab is still sending the file, its progress is the upload's
        const uploading = state === IMAGE_STATE.IMPORTING && upload;

        return {
          id,
          image,
          state,
          stateLabel:   this.t(`dbaas.images.state.${ uploading ? 'uploading' : state }`),
          progress:     uploading ? upload.progress : progress,
          message:      upload?.error || message,
          displayName:  image.spec?.displayName || image.metadata.name,
          namespace:    image.metadata.namespace,
          osVersion:    image.metadata.labels?.[IMAGE_LABEL.OS_VERSION] || '',
          sizeBytes:    image.status?.size || 0,
          size:         formatBytes(image.status?.size),
          storageClass: image.metadata.annotations?.['harvesterhci.io/storageClassName'] || image.status?.storageClassName || '',
          created:      image.metadata.creationTimestamp,
          detail:       {
            name:   IMAGES_DETAIL_ROUTE,
            params: {
              cluster: this.$route.params.cluster, namespace: image.metadata.namespace, id: image.metadata.name
            },
          },
        };
      });
    },

    createLocation() {
      return { name: IMAGES_CREATE_ROUTE, params: { cluster: this.$route.params.cluster } };
    },
  },

  methods: {
    stateColor(state) {
      return STATE_COLOR[state] || 'bg-info';
    },

    remove(row) {
      this.$store.dispatch(`${ this.inStore }/promptRemove`, [row.image]);
    },
  },
};
</script>

<template>
  <Loading v-if="$fetchState.pending" />
  <div v-else>
    <header class="images-header">
      <div class="title">
        <h1>{{ t('dbaas.images.title') }}</h1>
      </div>
      <RcButton
        v-if="canCreate"
        :to="createLocation"
      >
        {{ t('dbaas.images.upload') }}
      </RcButton>
    </header>

    <Banner
      color="info"
      :label="t('dbaas.images.info')"
    />
    <Banner
      v-if="!canList"
      color="warning"
      :label="t('dbaas.images.forbidden')"
    />
    <Banner
      v-for="dup in duplicates"
      :key="dup"
      color="warning"
      :label="t('dbaas.images.duplicate', { key: dup }, true)"
    />

    <SortableTable
      v-if="canList"
      :rows="rows"
      :headers="headers"
      key-field="id"
      :search="true"
      :table-actions="false"
      :row-actions="false"
      default-sort-by="displayName"
      no-rows-key="dbaas.images.none"
    >
      <template #cell:state="{ row }">
        <div class="state-cell">
          <BadgeState
            v-clean-tooltip="row.message || null"
            :label="row.stateLabel"
            :color="stateColor(row.state)"
          />
          <PercentageBar
            v-if="row.state === 'importing'"
            class="mt-5"
            :model-value="row.progress"
            :show-percentage="true"
            :color-stops="PROGRESS_COLOR"
          />
        </div>
      </template>

      <template #cell:displayName="{ row }">
        <router-link :to="row.detail">
          {{ row.displayName }}
        </router-link>
      </template>

      <template #cell:actions="{ row }">
        <button
          type="button"
          class="btn btn-sm role-link"
          :aria-label="t('dbaas.images.delete', { name: row.displayName }, true)"
          @click="remove(row)"
        >
          <i class="icon icon-trash" />
        </button>
      </template>
    </SortableTable>
  </div>
</template>

<style lang="scss" scoped>
.images-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.state-cell {
  min-width: 120px;
}
</style>
