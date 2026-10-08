<script>
import { IMAGE_STATUS } from '../types';

const STATUS_CLASS = {
  [IMAGE_STATUS.UP_TO_DATE]: 'text-muted',
  [IMAGE_STATUS.OS_UPDATE]:  'text-info',
  [IMAGE_STATUS.EOL]:        'text-warning',
  [IMAGE_STATUS.UNKNOWN]:    'text-muted',
  [IMAGE_STATUS.REQUESTED]:  'text-info',
  [IMAGE_STATUS.UPDATING]:   'text-info',
};

// Image column of the DBInstance list: is the VM's OS image current, is an
// update available, or is its PostgreSQL version no longer supported
export default {
  props: {
    row: {
      type:     Object,
      required: true
    },
  },

  computed: {
    status() {
      return this.row.imageStatus;
    },

    busy() {
      return this.status === IMAGE_STATUS.UPDATING || this.status === IMAGE_STATUS.REQUESTED;
    },

    tooltip() {
      return this.row.imageDriftSummary || null;
    },
  },

  methods: {
    statusClass(status) {
      return STATUS_CLASS[status] || 'text-muted';
    },
  },
};
</script>

<template>
  <span
    v-clean-tooltip="tooltip"
    class="image-status"
    :class="statusClass(status)"
  >
    <i
      v-if="busy"
      class="icon icon-spinner icon-spin"
    />
    {{ row.imageStatusLabel }}
  </span>
</template>

<style lang="scss" scoped>
.image-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;

  // A fixed square box, so the spinner rotates around its own centre
  .icon-spin {
    width: 1em;
    height: 1em;
    line-height: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
}
</style>
