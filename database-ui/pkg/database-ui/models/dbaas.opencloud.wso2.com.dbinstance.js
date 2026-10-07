import SteveModel from '@shell/plugins/steve/steve-class';
import { colorForState } from '@shell/plugins/dashboard-store/resource-class';
import { ucFirst } from '@shell/utils/string';
import { insertAt } from '@shell/utils/array';
import { DB_PHASE } from '../types';
import { DEFAULT_ALLOCATED_STORAGE_GIB, DEFAULT_INSTANCE_CLASS } from '../config/catalog';

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
  'StorageChangeRejected',
];

// DeletionBlocked is True both while teardown waits for a running snapshot
// (normal) and when teardown cannot proceed; only the latter is a problem.
const DELETION_BLOCKED = 'DeletionBlocked';
const DELETION_PROTECTED = 'DeletionProtected';
const DELETION_PROBLEM_REASONS = [DELETION_PROTECTED, 'TeardownFailed', 'OperatorSecretCleanupFailed'];

const DEFAULT_ENGINE = 'PostgreSQL';

export default class DBInstance extends SteveModel {
  // Create-form defaults. Optional fields stay unset so the operator's own
  // defaults apply.
  applyDefaults() {
    this.spec = this.spec || {};
    this.spec.dbInstanceClass = this.spec.dbInstanceClass || DEFAULT_INSTANCE_CLASS;
    this.spec.allocatedStorage = this.spec.allocatedStorage || DEFAULT_ALLOCATED_STORAGE_GIB;
  }

  // No cards on the detail page: Shell's default Resources card lists the
  // operator-owned child objects (VM, Secrets, ...), and with one card present
  // Shell also adds a generic "Extras" card.
  get cards() {
    return [];
  }

  // Deletion has been requested. Shown as Deleting straight away, before the
  // operator's first status update (it derives the same phase from this field).
  get isDeleting() {
    return !!this.metadata?.deletionTimestamp;
  }

  get phase() {
    if (this.isDeleting) {
      return DB_PHASE.DELETING;
    }

    return (this.status?.phase || PENDING).toLowerCase();
  }

  get state() {
    return this.phase;
  }

  get stateDisplay() {
    return this.phase.split('-').map(ucFirst).join(' ');
  }

  get stateColor() {
    if (this.isDeleting && this.deletionProblem) {
      return 'text-error';
    }

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
      if (c.type === DELETION_BLOCKED) {
        return this.isDeleting && c.status === 'True' && DELETION_PROBLEM_REASONS.includes(c.reason);
      }

      return PROBLEM_CONDITIONS.includes(c.type) && c.status === 'True';
    });
  }

  // Why deletion cannot finish, if it is stuck
  get deletionProblem() {
    return this.problemConditions.find((c) => c.type === DELETION_BLOCKED);
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

    if (this.isDeleting) {
      if (this.deletionProblem?.reason === DELETION_PROTECTED) {
        add({ type: DELETION_BLOCKED, message: this.t('dbaas.instance.delete.protectedHint') });
      }

      // Teardown progress, e.g. waiting for a snapshot or for the VM to go away
      const deletion = this.conditionFor(DELETION_BLOCKED);

      if (deletion) {
        add(deletion);
      }

      return out;
    }

    const ready = this.conditionFor('Ready');

    if (ready && ready.status !== 'True' && this.phase !== DB_PHASE.STOPPED) {
      add(ready);
    }

    if (!out.length && this.status?.message && this.phase !== DB_PHASE.AVAILABLE) {
      add({ type: 'Status', message: this.status.message });
    }

    return out;
  }

  // --- Deletion (read by Shell's delete dialog) ---------------------------

  // Disables the dialog's Delete button: the operator would refuse teardown,
  // and a Kubernetes delete cannot be cancelled, leaving the instance stuck.
  get preventDeletionMessage() {
    if (this.spec?.deletionProtection && !this.isDeleting) {
      return this.t('dbaas.instance.delete.protected', { name: this.nameDisplay });
    }

    return null;
  }

  get warnDeletionMessage() {
    return this.spec?.backup ? this.t('dbaas.instance.delete.warningWithBackups') : this.t('dbaas.instance.delete.warning');
  }

  // Deleting destroys the data volume, so ask for the name to be typed
  get confirmRemove() {
    return true;
  }

  get _availableActions() {
    const out = super._availableActions;
    const canUpdate = this.canUpdate;
    const protectedNow = !!this.spec?.deletionProtection;

    insertAt(out, 0, {
      action:  'goToConnection',
      label:   this.t('dbaas.instance.actions.connection'),
      icon:    'icon icon-network',
      enabled: !!this.status?.endpoint?.address,
    });

    insertAt(out, 1, {
      action:  protectedNow ? 'disableDeletionProtection' : 'enableDeletionProtection',
      label:   this.t(protectedNow ? 'dbaas.instance.actions.disableDeletionProtection' : 'dbaas.instance.actions.enableDeletionProtection'),
      icon:    protectedNow ? 'icon icon-unlock' : 'icon icon-lock',
      enabled: canUpdate,
    });

    return out;
  }

  // Detail page, opened on its Connection tab
  goToConnection() {
    return this.currentRouter().push({ ...this.detailLocation, hash: '#connection' });
  }

  enableDeletionProtection() {
    return this.setDeletionProtection(true);
  }

  disableDeletionProtection() {
    return this.setDeletionProtection(false);
  }

  setDeletionProtection(enabled) {
    return this.patch([{
      op: 'add', path: '/spec/deletionProtection', value: enabled
    }], {}, false, true);
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
