import SteveModel from '@shell/plugins/steve/steve-class';
import { colorForState } from '@shell/plugins/dashboard-store/resource-class';
import { ucFirst } from '@shell/utils/string';
import { DB_PHASE } from '../types';

// Shown before the operator has reported a phase (e.g. a newly created instance)
const PENDING = 'pending';

const PHASE_COLOR = {
  [DB_PHASE.AVAILABLE]:               'success',
  [DB_PHASE.CREATING]:                'info',
  [DB_PHASE.STARTING]:                'info',
  [DB_PHASE.STOPPING]:                'info',
  [DB_PHASE.MODIFYING]:               'info',
  [DB_PHASE.DELETING]:                'info',
  [DB_PHASE.STOPPED]:                 'darker',
  [DB_PHASE.DEGRADED]:                'warning',
  [DB_PHASE.FAILED]:                  'error',
  [DB_PHASE.INCOMPATIBLE_PARAMETERS]: 'error',
  [DB_PHASE.CRASH_LOOP_HALTED]:       'error',
  [PENDING]:                          'info',
};

// Conditions that report a problem when their status is True. Accepted is the
// one positive condition whose False status blocks the instance outright.
const PROBLEM_CONDITIONS = [
  'InterventionRequired',
  'CrashLoopHalted',
  'Degraded',
  'DeletionBlocked',
  'StorageChangeRejected',
];

const DEFAULT_ENGINE = 'PostgreSQL';

export default class DBInstance extends SteveModel {
  get phase() {
    return (this.status?.phase || PENDING).toLowerCase();
  }

  get state() {
    return this.phase;
  }

  get stateDisplay() {
    return this.phase.split('-').map(ucFirst).join(' ');
  }

  get stateColor() {
    const color = PHASE_COLOR[this.phase];

    return color ? `text-${ color }` : colorForState(this.phase);
  }

  // The spec was changed but the operator has not reconciled that generation yet
  get hasPendingChanges() {
    const generation = this.metadata?.generation;
    const observed = this.status?.observedGeneration;

    return !!generation && observed !== undefined && observed !== null && generation > observed;
  }

  conditionFor(type) {
    return (this.status?.conditions || []).find((c) => c.type === type);
  }

  // Conditions currently reporting a problem, in the order the operator lists them
  get problemConditions() {
    return (this.status?.conditions || []).filter((c) => {
      if (c.type === 'Accepted') {
        return c.status === 'False';
      }

      return PROBLEM_CONDITIONS.includes(c.type) && c.status === 'True';
    });
  }

  get hasProblem() {
    return ['error', 'warning'].includes(PHASE_COLOR[this.phase]) || this.problemConditions.length > 0;
  }

  // Detail for the state cell popover: problem conditions first, then why the
  // instance is not Ready (e.g. provisioning progress), then status.message.
  // The operator always sets status.message, so a healthy instance shows nothing.
  get stateMessages() {
    const out = [];
    const seen = new Set();
    const add = (condition) => {
      const text = condition.message || condition.reason;

      if (text && !seen.has(text)) {
        seen.add(text);
        out.push({
          type: condition.type, reason: condition.reason, message: text
        });
      }
    };

    this.problemConditions.forEach(add);

    const ready = this.conditionFor('Ready');

    if (ready && ready.status !== 'True' && this.phase !== DB_PHASE.STOPPED) {
      add(ready);
    }

    if (!out.length && this.status?.message && this.phase !== DB_PHASE.AVAILABLE) {
      add({ type: 'Status', message: this.status.message });
    }

    return out;
  }

  get engineVersion() {
    return this.spec?.engineVersion || this.status?.appliedSpec?.engineVersion || '';
  }

  get engineDisplay() {
    return this.engineVersion ? `${ DEFAULT_ENGINE } ${ this.engineVersion }` : DEFAULT_ENGINE;
  }

  get engineSort() {
    return parseInt(this.engineVersion, 10) || 0;
  }

  get instanceClass() {
    return this.spec?.dbInstanceClass || '';
  }

  get allocatedStorage() {
    return this.spec?.allocatedStorage || 0;
  }

  get storageDisplay() {
    return this.allocatedStorage ? `${ this.allocatedStorage } GiB` : '';
  }

  get endpointDisplay() {
    const endpoint = this.status?.endpoint;

    if (!endpoint?.address) {
      return '';
    }

    return endpoint.port ? `${ endpoint.address }:${ endpoint.port }` : endpoint.address;
  }
}
