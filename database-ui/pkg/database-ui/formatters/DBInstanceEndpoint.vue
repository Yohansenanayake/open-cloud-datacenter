<script>
import CopyToClipboardText from '@shell/components/CopyToClipboardText';

// Endpoint cell: host:port with copy, or a dash until the operator publishes one
export default {
  components: { CopyToClipboardText },

  props: {
    value: {
      type:    String,
      default: ''
    },
  },
};
</script>

<template>
  <CopyToClipboardText
    v-if="value"
    :text="value"
  />
  <span
    v-else
    class="text-muted"
  >&mdash;</span>
</template>
