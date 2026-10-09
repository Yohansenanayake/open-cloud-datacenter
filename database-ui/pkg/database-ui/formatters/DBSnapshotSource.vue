<script>
// Source instance of a DBSnapshot: a link while the instance exists, otherwise
// its name marked as deleted (manual snapshots outlive their instance).
export default {
  props: {
    value: {
      type:    String,
      default: ''
    },

    row: {
      type:     Object,
      required: true
    },
  },

  computed: {
    instance() {
      return this.row.sourceInstance;
    },
  },
};
</script>

<template>
  <router-link
    v-if="instance"
    :to="instance.detailLocation"
  >
    {{ value }}
  </router-link>
  <span v-else-if="value">
    {{ value }}
    <span class="text-muted">({{ t('dbaas.snapshot.sourceDeleted') }})</span>
  </span>
  <span
    v-else
    class="text-muted"
  >&mdash;</span>
</template>
