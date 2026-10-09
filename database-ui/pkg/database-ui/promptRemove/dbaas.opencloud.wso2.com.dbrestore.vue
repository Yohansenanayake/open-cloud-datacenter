<script>
import { Banner } from '@components/Banner';
import { mapGetters } from 'vuex';
import { resourceNames } from '@shell/utils/string';

// Body of the delete dialog for DBRestores. Deleting a running restore cancels
// it and deletes its unfinished instance (so Shell asks for the name to be
// typed, which also hides its own warning); deleting a finished one only
// removes the record. The model's warnDeletionMessage says which.
export default {
  name: 'PromptRemoveDBRestore',

  components: { Banner },

  props: {
    value: {
      type:    Array,
      default: () => []
    },

    names: {
      type:    Array,
      default: () => []
    },

    type: {
      type:     String,
      required: true
    },
  },

  computed: {
    // A store-bound t: resourceNames() calls it as a plain function
    ...mapGetters({ t: 'i18n/t' }),

    running() {
      return this.value.filter((r) => !r.isFinished);
    },

    finished() {
      return this.value.filter((r) => r.isFinished);
    },
  },

  methods: { resourceNames },
};
</script>

<template>
  <div>
    {{ t('promptRemove.attemptingToRemove', { type }) }}
    <span v-clean-html="resourceNames(names, null, t)" />

    <Banner
      v-for="r in running"
      :key="r.id"
      color="error"
      class="mb-0"
      :label="r.warnDeletionMessage"
    />
    <Banner
      v-if="finished.length"
      color="info"
      class="mb-0"
      :label="finished.length === 1 ? finished[0].warnDeletionMessage : t('dbaas.restore.delete.recordOnlyMany')"
    />
  </div>
</template>
