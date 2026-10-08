<script>
// A name that links to a related DBaaS object while it exists. The column's
// formatterOpts name the row getters to use:
//   { link: 'snapshot', note: 'snapshotMissingNote' }
// `link` returns the live object (or null); `note` returns text shown in
// brackets when there is nothing to link to (e.g. "deleted").
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

    col: {
      type:    Object,
      default: () => ({})
    },
  },

  computed: {
    opts() {
      return this.col?.formatterOpts || {};
    },

    target() {
      return this.opts.link ? this.row[this.opts.link] : null;
    },

    note() {
      return this.opts.note ? this.row[this.opts.note] : '';
    },
  },
};
</script>

<template>
  <router-link
    v-if="target"
    :to="target.detailLocation"
  >
    {{ value }}
  </router-link>
  <span v-else-if="value">
    {{ value }}
    <span
      v-if="note"
      class="text-muted"
    >({{ note }})</span>
  </span>
  <span
    v-else
    class="text-muted"
  >&mdash;</span>
</template>
