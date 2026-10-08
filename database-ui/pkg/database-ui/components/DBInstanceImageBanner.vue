<script>
import { Banner } from '@components/Banner';
import { RcButton } from '@components/RcButton';
import { isAdminUser } from '@shell/store/type-map';
import { IMAGE_STATUS } from '../types';

// Top-of-page notice about the instance's OS image: an update to apply, a
// PostgreSQL version the new image no longer supports, a repave in progress or
// refused. Shown only when there is something to say.
export default {
  name: 'DBInstanceImageBanner',

  components: { Banner, RcButton },

  props: {
    value: {
      type:     Object,
      required: true,
    },
  },

  data() {
    return { IMAGE_STATUS };
  },

  computed: {
    status() {
      return this.value.imageStatus;
    },

    driftMessage() {
      return this.value.imageDriftSummary;
    },

    refusal() {
      return this.value.repaveRefusal;
    },

    // "Could not evaluate" is an operator configuration issue, not something a
    // tenant can act on
    isAdmin() {
      return isAdminUser(this.$store.getters);
    },
  },
};
</script>

<template>
  <div>
    <!-- A request was refused; offer a retry while the update is still pending -->
    <Banner
      v-if="refusal && status === IMAGE_STATUS.OS_UPDATE"
      color="warning"
    >
      <div class="image-banner">
        <span>{{ t('dbaas.instance.image.banner.refused', { message: refusal.message }, true) }}</span>
        <RcButton
          variant="secondary"
          size="small"
          :disabled="!value.canApplyOSUpdate"
          @click="value.applyOSUpdate()"
        >
          {{ t('dbaas.instance.image.retry') }}
        </RcButton>
      </div>
    </Banner>

    <Banner
      v-else-if="status === IMAGE_STATUS.OS_UPDATE"
      color="info"
    >
      <div class="image-banner">
        <div>
          <div>{{ t('dbaas.instance.image.banner.osUpdate') }}</div>
          <div
            v-if="driftMessage"
            class="text-muted mt-5"
          >
            {{ driftMessage }}
          </div>
          <div
            v-if="!value.canApplyOSUpdate"
            class="text-muted mt-5"
          >
            {{ value.applyOSUpdateBlockedReason }}
          </div>
        </div>
        <RcButton
          class="apply-button"
          :disabled="!value.canApplyOSUpdate"
          @click="value.applyOSUpdate()"
        >
          <i class="icon icon-upgrade-alt mr-5" />
          {{ t('dbaas.instance.image.apply') }}
        </RcButton>
      </div>
    </Banner>

    <Banner
      v-else-if="status === IMAGE_STATUS.EOL"
      color="warning"
    >
      <div>{{ t('dbaas.instance.image.banner.eol', { version: value.engineVersion || '?' }, true) }}</div>
      <div
        v-if="driftMessage"
        class="text-muted mt-5"
      >
        {{ driftMessage }}
      </div>
    </Banner>

    <Banner
      v-else-if="status === IMAGE_STATUS.UPDATING || status === IMAGE_STATUS.REQUESTED"
      color="info"
    >
      <div class="busy-line">
        <i class="icon icon-spinner icon-spin" />
        <span>{{ status === IMAGE_STATUS.REQUESTED ? t('dbaas.instance.image.banner.requested') : t('dbaas.instance.image.banner.updating', { message: (value.repaveCondition && value.repaveCondition.message) || '' }, true) }}</span>
      </div>
    </Banner>

    <Banner
      v-else-if="status === IMAGE_STATUS.UNKNOWN && isAdmin && driftMessage"
      color="info"
      :label="t('dbaas.instance.image.banner.unknown', { message: driftMessage }, true)"
    />
  </div>
</template>

<style lang="scss" scoped>
.image-banner {
  // Fill the banner (its content area is a flex row), so the button sits at
  // the right edge instead of right after the text
  flex: 1 1 auto;
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;

  .apply-button {
    flex: 0 0 auto;
    margin-right: 24px;
  }
}

// Banner content is a flex row that stretches its children; give the spinner
// a fixed square box so it rotates around its own centre
.busy-line {
  display: flex;
  align-items: center;
  gap: 8px;

  .icon-spin {
    flex: 0 0 auto;
    width: 1em;
    height: 1em;
    line-height: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
}
</style>
