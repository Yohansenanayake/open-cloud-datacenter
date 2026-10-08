// Minimal stand-in for Shell's SteveModel, so the DBaaS models can be tested
// without a Vuex store. Use with:
//   jest.mock('@shell/plugins/steve/steve-class', () => require('../helpers/steve-class-mock'));
// Constructor: new Model(data, { getters, rootGetters, canUpdate })
class SteveModelMock {
  constructor(data, ctx = {}) {
    Object.assign(this, data);
    this.$getters = ctx.getters || {};
    this.$rootGetters = ctx.rootGetters || { 'cluster/schemaFor': () => null, clusterId: 'c-test' };
    this._canUpdate = ctx.canUpdate !== false;
  }

  get name() {
    return this.metadata?.name;
  }

  get namespace() {
    return this.metadata?.namespace;
  }

  get id() {
    return `${ this.metadata?.namespace }/${ this.metadata?.name }`;
  }

  get nameDisplay() {
    return this.metadata?.name;
  }

  get canUpdate() {
    return this._canUpdate;
  }

  get stateBackground() {
    return this.stateColor.replace('text-', 'bg-');
  }

  get _availableActions() {
    return [{ action: 'goToEdit' }, { action: 'goToClone' }, { action: 'cloneYaml' }, { action: 'promptRemove' }];
  }

  // Translations come back as "key" or "key:{args}" so tests can assert them
  t(key, args) {
    return args ? `${ key }:${ JSON.stringify(args) }` : key;
  }
}

module.exports = SteveModelMock;
module.exports.default = SteveModelMock;
