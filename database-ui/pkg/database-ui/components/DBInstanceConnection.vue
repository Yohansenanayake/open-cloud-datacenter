<script>
import CopyToClipboardText from '@shell/components/CopyToClipboardText';
import { Banner } from '@components/Banner';
import { RcButton } from '@components/RcButton';
import { SECRET } from '@shell/config/types';
import { base64Decode } from '@shell/utils/crypto';
import { copyTextToClipboard } from '@shell/utils/clipboard';
import { downloadFile } from '@shell/utils/download';

const LOAD = {
  LOADING:   'loading',
  READY:     'ready',
  NOT_READY: 'notReady',
  FORBIDDEN: 'forbidden',
  ERROR:     'error',
};

// Connection details for a DBInstance: endpoint, admin credentials and CA.
//
// Both Secrets are read by the names the operator publishes in
// status.resources (never by listing Secrets), with a direct request so their
// contents stay in this component instead of the shared store. The admin
// password is fetched only when the user asks to see or copy it, and is
// dropped again when hidden or when the page closes.
export default {
  name: 'DBInstanceConnection',

  components: {
    Banner, CopyToClipboardText, RcButton
  },

  props: {
    value: {
      type:     Object,
      required: true,
    },
  },

  data() {
    return {
      LOAD,
      connection:      null,
      connectionState: LOAD.LOADING,
      credentials:     null,
      passwordState:   null,
      passwordVisible: false,
      // Which value was just copied ('password' or 'ca'), for button feedback
      copied:          null,
    };
  },

  created() {
    this.loadConnection();
  },

  beforeUnmount() {
    this.credentials = null;
  },

  computed: {
    inStore() {
      return this.$store.getters['currentProduct']?.inStore || 'cluster';
    },

    resources() {
      return this.value.status?.resources || {};
    },

    endpoint() {
      return this.value.status?.endpoint || {};
    },

    appliedSpec() {
      return this.value.status?.appliedSpec || {};
    },

    host() {
      return this.connection?.host || this.endpoint.address || '';
    },

    port() {
      return this.connection?.port || (this.endpoint.port ? `${ this.endpoint.port }` : '');
    },

    dbName() {
      return this.connection?.dbname || this.appliedSpec.dbName || this.value.spec?.dbName || '';
    },

    sslMode() {
      return this.connection?.sslmode || '';
    },

    jdbcUrl() {
      return this.connection?.jdbcUrl || this.endpoint.jdbcUrl || '';
    },

    caCert() {
      return this.connection?.['ca.crt'] || '';
    },

    username() {
      return this.credentials?.admin_user || this.appliedSpec.masterUsername || this.value.spec?.masterUsername || '';
    },

    psqlCommand() {
      if (!this.host) {
        return '';
      }

      const parts = [`host=${ this.host }`, `port=${ this.port }`, `dbname=${ this.dbName }`, `user=${ this.username }`];

      if (this.sslMode) {
        parts.push(`sslmode=${ this.sslMode }`);
      }
      if (this.caCert) {
        parts.push('sslrootcert=ca.crt');
      }

      return `psql "${ parts.join(' ') }"`;
    },

    hasEndpoint() {
      return !!this.host;
    },

    password() {
      return this.credentials?.admin_password || '';
    },
  },

  watch: {
    // The operator creates the Secret once the database has an endpoint
    'resources.connectionSecretName'(neu, old) {
      if (neu && neu !== old) {
        this.loadConnection();
      }
    },
  },

  methods: {
    async readSecret(name) {
      const collection = this.$store.getters[`${ this.inStore }/schemaFor`](SECRET)?.links?.collection;

      if (!collection) {
        throw { _status: 403 }; // eslint-disable-line no-throw-literal
      }

      const secret = await this.$store.dispatch(`${ this.inStore }/request`, {
        url:                  `${ collection }/${ encodeURIComponent(this.value.namespace) }/${ encodeURIComponent(name) }`,
        method:               'get',
        redirectUnauthorized: false,
      });

      return Object.fromEntries(Object.entries(secret?.data || {}).map(([k, v]) => [k, base64Decode(v)]));
    },

    loadStateFor(err) {
      if (err?._status === 403 || err?._status === 401) {
        return LOAD.FORBIDDEN;
      }
      if (err?._status === 404) {
        return LOAD.NOT_READY;
      }

      return LOAD.ERROR;
    },

    async loadConnection() {
      const name = this.resources.connectionSecretName;

      if (!name) {
        this.connectionState = LOAD.NOT_READY;

        return;
      }

      this.connectionState = LOAD.LOADING;
      try {
        this.connection = await this.readSecret(name);
        this.connectionState = LOAD.READY;
      } catch (err) {
        this.connection = null;
        this.connectionState = this.loadStateFor(err);
      }
    },

    async ensurePassword() {
      if (this.credentials) {
        return true;
      }

      const name = this.resources.adminCredentialsSecretName;

      if (!name) {
        this.passwordState = LOAD.NOT_READY;

        return false;
      }

      this.passwordState = LOAD.LOADING;
      try {
        this.credentials = await this.readSecret(name);
        this.passwordState = LOAD.READY;

        return true;
      } catch (err) {
        this.passwordState = this.loadStateFor(err);

        return false;
      }
    },

    async togglePassword() {
      if (this.passwordVisible) {
        this.passwordVisible = false;
        this.credentials = null;
        this.passwordState = null;

        return;
      }

      this.passwordVisible = await this.ensurePassword();
    },

    async copy(key, text) {
      await copyTextToClipboard(text);
      this.copied = key;
      setTimeout(() => {
        if (this.copied === key) {
          this.copied = null;
        }
      }, 2000);
    },

    async copyPassword() {
      if (await this.ensurePassword()) {
        await this.copy('password', this.password);
      }
    },

    downloadCa() {
      downloadFile(`${ this.value.nameDisplay }-ca.crt`, this.caCert, 'application/x-pem-file');
    },
  },
};
</script>

<template>
  <div class="dbinstance-connection">
    <Banner
      v-if="!hasEndpoint"
      color="info"
      :label="t('dbaas.instance.connection.notReady')"
    />

    <template v-else>
      <Banner
        v-if="connectionState === LOAD.FORBIDDEN"
        color="warning"
        :label="t('dbaas.instance.connection.connectionForbidden')"
      />
      <Banner
        v-else-if="connectionState === LOAD.NOT_READY"
        color="info"
        :label="t('dbaas.instance.connection.secretNotReady')"
      />
      <Banner
        v-else-if="connectionState === LOAD.ERROR"
        color="error"
      >
        {{ t('dbaas.instance.connection.loadError') }}
        <a
          role="button"
          class="ml-5"
          @click="loadConnection"
        >{{ t('dbaas.instance.connection.retry') }}</a>
      </Banner>

      <h3>{{ t('dbaas.instance.connection.endpoint') }}</h3>
      <dl class="details">
        <dt>{{ t('dbaas.instance.connection.host') }}</dt>
        <dd><CopyToClipboardText :text="host" /></dd>

        <dt>{{ t('dbaas.instance.connection.port') }}</dt>
        <dd><CopyToClipboardText :text="port" /></dd>

        <dt>{{ t('dbaas.instance.connection.dbName') }}</dt>
        <dd>
          <CopyToClipboardText
            v-if="dbName"
            :text="dbName"
          />
          <span
            v-else
            class="text-muted"
          >&mdash;</span>
        </dd>

        <dt>{{ t('dbaas.instance.connection.sslMode') }}</dt>
        <dd>
          <span v-if="sslMode">{{ sslMode }}</span>
          <span
            v-else
            class="text-muted"
          >&mdash;</span>
        </dd>

        <template v-if="jdbcUrl">
          <dt>{{ t('dbaas.instance.connection.jdbcUrl') }}</dt>
          <dd class="wrap">
            <CopyToClipboardText :text="jdbcUrl" />
          </dd>
        </template>

        <template v-if="psqlCommand">
          <dt>{{ t('dbaas.instance.connection.psql') }}</dt>
          <dd class="wrap">
            <CopyToClipboardText :text="psqlCommand" />
          </dd>
        </template>
      </dl>

      <h3 class="mt-30">
        {{ t('dbaas.instance.connection.credentials') }}
      </h3>
      <dl class="details">
        <dt>{{ t('dbaas.instance.connection.username') }}</dt>
        <dd>
          <CopyToClipboardText
            v-if="username"
            :text="username"
          />
          <span
            v-else
            class="text-muted"
          >&mdash;</span>
        </dd>

        <dt>{{ t('dbaas.instance.connection.password') }}</dt>
        <dd>
          <div class="password">
            <code
              v-if="passwordVisible && password"
              class="password-value"
            >{{ password }}</code>
            <span
              v-else
              class="password-mask"
              aria-hidden="true"
            >&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;</span>
            <RcButton
              variant="secondary"
              size="small"
              :disabled="passwordState === LOAD.LOADING"
              @click="togglePassword"
            >
              {{ passwordVisible ? t('dbaas.instance.connection.hide') : t('dbaas.instance.connection.show') }}
            </RcButton>
            <RcButton
              variant="secondary"
              size="small"
              :disabled="passwordState === LOAD.LOADING"
              @click="copyPassword"
            >
              {{ copied === 'password' ? t('dbaas.instance.connection.copied') : t('dbaas.instance.connection.copy') }}
            </RcButton>
          </div>
          <div
            v-if="passwordState === LOAD.FORBIDDEN"
            class="text-warning mt-5"
          >
            {{ t('dbaas.instance.connection.passwordForbidden') }}
          </div>
          <div
            v-else-if="passwordState === LOAD.NOT_READY"
            class="text-muted mt-5"
          >
            {{ t('dbaas.instance.connection.secretNotReady') }}
          </div>
          <div
            v-else-if="passwordState === LOAD.ERROR"
            class="text-error mt-5"
          >
            {{ t('dbaas.instance.connection.loadError') }}
          </div>
        </dd>
      </dl>

      <template v-if="caCert">
        <h3 class="mt-30">
          {{ t('dbaas.instance.connection.caCert') }}
        </h3>
        <p class="text-muted mb-10">
          {{ t('dbaas.instance.connection.caCertDescription') }}
        </p>
        <div class="ca-actions">
          <RcButton
            variant="secondary"
            size="small"
            @click="downloadCa"
          >
            {{ t('dbaas.instance.connection.downloadCa') }}
          </RcButton>
          <RcButton
            variant="secondary"
            size="small"
            @click="copy('ca', caCert)"
          >
            {{ copied === 'ca' ? t('dbaas.instance.connection.copied') : t('dbaas.instance.connection.copy') }}
          </RcButton>
        </div>
      </template>
    </template>
  </div>
</template>

<style lang="scss" scoped>
.dbinstance-connection {
  h3 {
    margin-bottom: 10px;
  }

  .details {
    display: grid;
    grid-template-columns: 160px 1fr;
    row-gap: 10px;
    column-gap: 20px;
    margin: 0;

    dt {
      color: var(--input-label);
    }

    dd {
      margin: 0;
      min-width: 0;
    }

    .wrap {
      word-break: break-all;
    }
  }

  .password {
    display: flex;
    align-items: center;
    gap: 10px;

    .password-value {
      word-break: break-all;
    }
  }

  .ca-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }
}
</style>
