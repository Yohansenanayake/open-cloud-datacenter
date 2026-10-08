<script>
import LiveDate from '@shell/components/formatter/LiveDate';

// Read-only "OS Image" block for the instance page: which baked image revision
// the VM runs and what the operator last reported about it.
export default {
  name: 'DBInstanceImageInfo',

  components: { LiveDate },

  props: {
    value: {
      type:     Object,
      required: true,
    },
  },

  computed: {
    drift() {
      return this.value.imageDriftCondition;
    },
  },
};
</script>

<template>
  <div>
    <h3>{{ t('dbaas.instance.image.title') }}</h3>
    <dl class="image-info">
      <dt>{{ t('dbaas.instance.image.revision') }}</dt>
      <dd>{{ value.currentImageRevision || '—' }}</dd>

      <dt>{{ t('dbaas.instance.image.statusLabel') }}</dt>
      <dd>{{ value.imageStatusLabel }}</dd>

      <template v-if="value.imageDriftSummary">
        <dt>{{ t('dbaas.instance.image.details') }}</dt>
        <dd>{{ value.imageDriftSummary }}</dd>
      </template>

      <template v-if="drift && drift.lastTransitionTime">
        <dt>{{ t('dbaas.instance.image.since') }}</dt>
        <dd>
          <LiveDate
            :value="drift.lastTransitionTime"
            :add-suffix="true"
          />
        </dd>
      </template>
    </dl>
  </div>
</template>

<style lang="scss" scoped>
.image-info {
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
    word-break: break-word;
  }
}
</style>
