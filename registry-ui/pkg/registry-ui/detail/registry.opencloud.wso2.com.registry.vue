<script>
import CreateEditView from '@shell/mixins/create-edit-view';
import ResourceTabs from '@shell/components/form/ResourceTabs';
import Tab from '@shell/components/Tabbed/Tab';

export default {
  name: 'RegistryDetail',

  components: { ResourceTabs, Tab },

  mixins: [CreateEditView],

  props: {
    mode: {
      type:    String,
      default: 'view',
    },
    value: {
      type:     Object,
      required: true,
    },
  },

  computed: {
    rows() {
      const s = this.value.status || {};

      return [
        {
          key: 'phase', label: this.t('registryUi.detail.phase'), value: this.value.phase
        },
        {
          key: 'message', label: this.t('registryUi.detail.message'), value: s.message || '—'
        },
        {
          key: 'project', label: this.t('registryUi.detail.project'), value: s.harborProject || '—'
        },
        {
          key: 'projectId', label: this.t('registryUi.detail.projectId'), value: s.harborProjectID || '—'
        },
        {
          key: 'url', label: this.t('registryUi.detail.url'), value: s.registryURL || '—'
        },
        {
          key: 'quota', label: this.t('registryUi.detail.quota'), value: `${ this.value.quotaDisplay } (${ this.value.plan })`
        },
        {
          key: 'pull', label: this.t('registryUi.detail.pullSecret'), value: s.pullSecretName || '—'
        },
        {
          key: 'push', label: this.t('registryUi.detail.pushSecret'), value: s.pushSecretName || '—'
        },
      ];
    },

    ns() {
      return this.value.metadata.namespace;
    },

    readCreds() {
      const secret = this.value.status?.pushSecretName || `${ this.value.metadata.name }-push`;

      return `kubectl -n ${ this.ns } get secret ${ secret } -o jsonpath='{.data.\\.dockerconfigjson}' | base64 -d`;
    },

    pushCmd() {
      const host = this.value.registryHost || '<harbor-host>';
      const project = this.value.status?.harborProject || '<project>';

      return [
        `docker login ${ host } -u '<username from the command above>'`,
        `docker tag myapp:1.0 ${ host }/${ project }/myapp:1.0`,
        `docker push ${ host }/${ project }/myapp:1.0`,
      ].join('\n');
    },

    copyCmd() {
      const secret = this.value.status?.pullSecretName || `${ this.value.metadata.name }-pull`;

      return [
        `kubectl -n ${ this.ns } get secret ${ secret } -o jsonpath='{.data.\\.dockerconfigjson}' | base64 -d > ${ secret }.json`,
        `kubectl --kubeconfig other-cluster.yaml -n my-app create secret docker-registry ${ secret } --from-file=.dockerconfigjson=${ secret }.json`,
      ].join('\n');
    },
  },
};
</script>

<template>
  <ResourceTabs
    :value="value"
    :mode="mode"
  >
    <Tab
      name="summary"
      :label="t('registryUi.detail.summary')"
      :weight="10"
    >
      <table class="registry-facts">
        <tr
          v-for="r in rows"
          :key="r.key"
        >
          <th>{{ r.label }}</th>
          <td :class="{ 'text-error': r.key === 'phase' && !value.isReady, 'text-success': r.key === 'phase' && value.isReady }">
            {{ r.value }}
          </td>
        </tr>
      </table>
    </Tab>
    <Tab
      name="connect"
      :label="t('registryUi.detail.connect')"
      :weight="9"
    >
      <h3>{{ t('registryUi.detail.readCreds') }}</h3>
      <pre class="registry-cmd">{{ readCreds }}</pre>
      <h3>{{ t('registryUi.detail.push') }}</h3>
      <pre class="registry-cmd">{{ pushCmd }}</pre>
      <h3>{{ t('registryUi.detail.copy') }}</h3>
      <pre class="registry-cmd">{{ copyCmd }}</pre>
    </Tab>
  </ResourceTabs>
</template>

<style lang="scss" scoped>
.registry-facts {
  border-collapse: collapse;
  th, td { padding: 6px 12px; text-align: left; border-bottom: 1px solid var(--border); }
  th { width: 240px; color: var(--input-label); font-weight: normal; }
}
.registry-cmd {
  padding: 10px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--box-bg);
  border: 1px solid var(--border);
  border-radius: var(--border-radius);
}
h3 { margin-top: 16px; }
</style>
