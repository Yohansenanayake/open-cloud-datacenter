import { flushPromises, mount } from '@vue/test-utils';
import StopDialog from '../../dialog/DBaaSConfirmStopDialog.vue';
import SnapshotDialog from '../../dialog/DBaaSTakeSnapshotDialog.vue';

// Shell components are stubbed: these tests cover the dialogs' own behaviour
jest.mock('@shell/components/AsyncButton', () => ({
  name: 'AsyncButton', props: ['actionLabel', 'disabled'], emits: ['click'], template: '<button class="go" :disabled="disabled" @click="$emit(\'click\', () => {})">{{ actionLabel }}</button>'
}));
jest.mock('@components/Card', () => ({ Card: { name: 'Card', template: '<div><slot name="title" /><slot name="body" /><slot name="actions" /></div>' } }));
jest.mock('@components/Banner', () => ({
  Banner: {
    name: 'Banner', props: ['label', 'color'], template: '<div class="banner">{{ label }}</div>'
  }
}));
jest.mock('@components/Form/LabeledInput', () => ({
  LabeledInput: {
    name: 'LabeledInput', props: ['value', 'label', 'rules'], template: '<input :value="value" />'
  }
}));
jest.mock('@shell/utils/error', () => ({ exceptionToErrorsArray: (e) => [String(e)] }));

const t = (k, a) => (a ? `${ k }:${ JSON.stringify(a) }` : k);
const global = {
  config:     { globalProperties: { t } },
  directives: {
    cleanHtml: (el, b) => {
      el.innerHTML = b.value;
    }
  },
};

describe('Stop dialog', () => {
  it('stops every selected instance and clears the table selection', async() => {
    const mk = (n) => ({ nameDisplay: n, setRunning: jest.fn(() => Promise.resolve()) });
    const a = mk('db-a');
    const b = mk('db-b');
    const w = mount(StopDialog, {
      props:  { resources: [a, b] },
      global: { ...global, mocks: { $store: { getters: { 'i18n/t': t } } } },
    });

    await w.find('button.go').trigger('click');
    await flushPromises();
    expect(a.setRunning).toHaveBeenCalledWith(false);
    expect(b.setRunning).toHaveBeenCalledWith(false);
    expect(w.emitted('close')[0][0]).toEqual({ performCallback: true, clearTableSelection: true });
  });
});

describe('Take Snapshot dialog', () => {
  it('suggests a manual snapshot name and creates the snapshot', async() => {
    const save = jest.fn(() => Promise.resolve());
    const dispatch = jest.fn((action) => (action === 'cluster/create' ? Promise.resolve({ save }) : Promise.resolve()));
    const instance = {
      name: 'orders-db', namespace: 'ns', takeSnapshotBlockedReason: ''
    };
    const w = mount(SnapshotDialog, {
      props:  { resources: [instance] },
      global: { ...global, mocks: { $store: { getters: { currentProduct: { inStore: 'cluster' } }, dispatch } } },
    });

    expect(w.find('input').element.value).toMatch(/^orders-db-manual-\d{8}-\d{4}$/);
    await w.find('button.go').trigger('click');
    await flushPromises();

    const [, created] = dispatch.mock.calls.find(([action]) => action === 'cluster/create');

    expect(created.spec).toEqual({ sourceInstanceRef: { name: 'orders-db' } });
    expect(created.metadata.namespace).toBe('ns');
    expect(save).toHaveBeenCalled();
  });
});
