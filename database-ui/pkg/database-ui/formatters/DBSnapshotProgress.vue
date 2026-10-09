<script>
import PercentageBar from '@shell/components/PercentageBar';
import { SNAPSHOT_PHASE } from '../types';

// PercentageBar is built for usage (red above 80%); progress stays one colour
const PROGRESS_COLOR = { 0: '--primary' };

// Backup progress of a DBSnapshot (status.progress mirrors the Harvester
// VirtualMachineBackup's progress). A bar while running, plain text otherwise.
export default {
  components: { PercentageBar },

  data() {
    return { PROGRESS_COLOR };
  },

  props: {
    row: {
      type:     Object,
      required: true
    },
  },

  computed: {
    phase() {
      return this.row.phase;
    },

    showBar() {
      return this.phase === SNAPSHOT_PHASE.IN_PROGRESS && typeof this.row.progress === 'number';
    },
  },
};
</script>

<template>
  <PercentageBar
    v-if="showBar"
    class="snapshot-progress"
    :model-value="row.progress"
    :show-percentage="true"
    :color-stops="PROGRESS_COLOR"
  />
  <span v-else-if="row.isReady">{{ t('dbaas.snapshot.completed') }}</span>
  <span
    v-else
    class="text-muted"
  >&mdash;</span>
</template>

<style lang="scss" scoped>
// The bar fills its container; keep it compact on wide detail pages
.snapshot-progress {
  max-width: 320px;
}
</style>
