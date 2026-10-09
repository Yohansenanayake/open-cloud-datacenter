<script>
import BadgeStateFormatter from '@shell/components/formatter/BadgeStateFormatter';

// State cell for DBInstance lists: the phase badge plus an icon that reveals
// condition detail (problems, provisioning progress, unapplied spec changes).
export default {
  components: { BadgeStateFormatter },

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
    messages() {
      const out = this.row.stateMessages.map((m) => (m.reason ? `${ m.type } (${ m.reason }): ${ m.message }` : `${ m.type }: ${ m.message }`));

      if (this.row.hasPendingChanges) {
        out.push(this.t('dbaas.instance.state.pendingChanges'));
      }

      return out;
    },

    iconClass() {
      if (this.row.hasProblem) {
        return this.row.stateColor === 'text-error' ? 'icon-error text-error' : 'icon-warning text-warning';
      }

      return 'icon-info text-info';
    },
  },
};
</script>

<template>
  <div class="dbinstance-state">
    <BadgeStateFormatter :row="row" />
    <v-dropdown
      v-if="messages.length"
      :triggers="['hover', 'focus']"
      :popper-triggers="['hover']"
      placement="bottom-start"
    >
      <span
        class="detail-icon"
        tabindex="0"
        role="button"
        :aria-label="t('dbaas.instance.state.detail')"
      >
        <i
          class="icon icon-lg"
          :class="iconClass"
        />
      </span>

      <template #popper>
        <ul class="dbinstance-state-messages">
          <li
            v-for="(message, i) in messages"
            :key="i"
          >
            {{ message }}
          </li>
        </ul>
      </template>
    </v-dropdown>
  </div>
</template>

<style lang="scss" scoped>
.dbinstance-state {
  display: flex;
  align-items: center;
  gap: 6px;

  .detail-icon {
    display: inline-flex;
    cursor: help;
  }
}

.dbinstance-state-messages {
  margin: 0;
  padding: 0 0 0 16px;
  max-width: 480px;

  li + li {
    margin-top: 6px;
  }
}
</style>
