import SteveModel from '@shell/plugins/steve/steve-class';
import { PLAN_QUOTA_GIB } from '../types';

export default class Registry extends SteveModel {
  applyDefaults() {
    if (!this.spec) {
      this.spec = { plan: 'starter' };
    }
  }

  get plan() {
    return this.spec?.plan || 'starter';
  }

  get quotaGiB() {
    return PLAN_QUOTA_GIB[this.plan] || 0;
  }

  get quotaDisplay() {
    return `${ this.quotaGiB } GiB`;
  }

  get phase() {
    return this.status?.phase || 'Pending';
  }

  get isReady() {
    return this.phase === 'Ready';
  }

  // The image host clients use: the registry URL without its scheme.
  get registryHost() {
    return (this.status?.registryURL || '').replace(/^https?:\/\//, '').replace(/\/$/, '');
  }
}
