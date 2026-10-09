import SteveModel from '@shell/plugins/steve/steve-class';
import { colorForState } from '@shell/plugins/dashboard-store/resource-class';
import { ucFirst } from '@shell/utils/string';
import { insertAt } from '@shell/utils/array';
import { DB_PHASE, DBAAS, IMAGE_STATUS } from '../types';
import { DEFAULT_ALLOCATED_STORAGE_GIB, DEFAULT_INSTANCE_CLASS } from '../config/catalog';
import { parseEngineEOL, parseOSUpdate, shortDriftMessage } from '../utils/image-drift';

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

// Repave (OS image update). The operator compares the VM's image revision with
// its catalog in the ImageDrift condition, and repaves when this annotation is
// set to a value it has not handled yet (internal/ensure/repave.go).
const REPAVE_TRIGGER = 'dbaas.opencloud.wso2.com/repave-trigger';
const REPAVE_REFUSED_REASONS = ['RepaveNotAvailable', 'RepaveBlockedEOL', 'RepaveInvalidStream'];


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

    // Power state (spec.running), like a Harvester VM's Start / Stop. Bulkable,
    // so they also show as buttons above the list for a selection.
    insertAt(out, 0, {
      action:     'startInstance',
      label:      this.t('dbaas.instance.actions.start'),
      icon:       'icon icon-play',
      enabled:    canUpdate && !this.isDeleting && !this.wantsRunning,
      bulkable:   true,
      bulkAction: 'startInstances',
    });
    insertAt(out, 1, {
      action:     'stopInstance',
      altAction:  'stopInstanceNow',
      label:      this.t('dbaas.instance.actions.stop'),
      icon:       'icon icon-close',
      enabled:    canUpdate && !this.isDeleting && this.wantsRunning,
      bulkable:   true,
      bulkAction: 'stopInstances',
    });

    insertAt(out, 2, {
      action:  'goToConnection',
      label:   this.t('dbaas.instance.actions.connection'),
      icon:    'icon icon-network',
      enabled: !!this.status?.endpoint?.address,
    });

    insertAt(out, 3, {
      action:  'takeSnapshot',
      label:   this.t('dbaas.snapshot.actions.take'),
      icon:    'icon icon-backup',
      enabled: this.canTakeSnapshot,
    });

    insertAt(out, 4, {
      action:  'applyOSUpdate',
      label:   this.t('dbaas.instance.image.apply'),
      icon:    'icon icon-upgrade-alt',
      enabled: this.canApplyOSUpdate,
    });

    insertAt(out, 5, {
      action:  protectedNow ? 'disableDeletionProtection' : 'enableDeletionProtection',
      label:   this.t(protectedNow ? 'dbaas.instance.actions.disableDeletionProtection' : 'dbaas.instance.actions.enableDeletionProtection'),
      icon:    protectedNow ? 'icon icon-unlock' : 'icon icon-lock',
      enabled: canUpdate,
    });

    return out;
  }

  // --- Snapshots -----------------------------------------------------------

  // Backups can only be turned on when the instance is created (spec.backup)
  get hasBackup() {
    return !!this.spec?.backup;
  }

  // Why "Take Snapshot" is unavailable; the operator would reject the snapshot
  // permanently (SourceBackupDisabled / SourceNotReady / SourceDeleting)
  get takeSnapshotBlockedReason() {
    const schema = this.$rootGetters['cluster/schemaFor'](DBAAS.SNAPSHOT);

    if (!schema?.collectionMethods?.find((m) => m.toLowerCase() === 'post')) {
      return this.t('dbaas.snapshot.blocked.noPermission');
    }
    if (!this.hasBackup) {
      return this.t('dbaas.snapshot.blocked.backupDisabled');
    }
    if (this.isDeleting) {
      return this.t('dbaas.snapshot.blocked.deleting');
    }
    if (this.phase !== DB_PHASE.AVAILABLE) {
      return this.t('dbaas.snapshot.blocked.notAvailable');
    }

    return '';
  }

  get canTakeSnapshot() {
    return !this.takeSnapshotBlockedReason;
  }

  takeSnapshot() {
    this.$dispatch('promptModal', {
      component:  'DBaaSTakeSnapshotDialog',
      resources:  [this],
      modalWidth: '520px',
    });
  }

  // Snapshots taken from this instance (by UID, so an older instance of the
  // same name doesn't contribute its snapshots)
  get snapshots() {
    const uid = this.metadata?.uid;

    return this.$getters['all'](DBAAS.SNAPSHOT).filter((s) => s.namespace === this.namespace &&
      s.sourceName === this.name &&
      (!s.source.instanceUID || s.source.instanceUID === uid));
  }

  // Detail page, opened on its Connection tab
  goToConnection() {
    return this.currentRouter().push({ ...this.detailLocation, hash: '#connection' });
  }

  // --- Power (spec.running) ---------------------------------------------------

  // Desired power state; the operator defaults an omitted spec.running to true
  get wantsRunning() {
    return this.spec?.running !== false;
  }

  setRunning(running) {
    return this.patch([{
      op: 'add', path: '/spec/running', value: running
    }], {}, false, true);
  }

  startInstance() {
    return this.setRunning(true);
  }

  startInstances(instances) {
    return Promise.all(instances.map((i) => i.startInstance()));
  }

  // Stopping makes the database unavailable, so it is confirmed first
  stopInstance() {
    return this.stopInstances([this]);
  }

  stopInstances(instances) {
    this.$dispatch('promptModal', {
      component:  'DBaaSConfirmStopDialog',
      resources:  instances,
      modalWidth: '480px',
    });
  }

  // Shift-click on Stop: skip the confirmation (as Harvester does for VMs)
  stopInstanceNow() {
    return this.setRunning(false);
  }

  // --- OS image (repave) ------------------------------------------------------

  get imageDriftCondition() {
    return this.conditionFor('ImageDrift');
  }

  get repaveCondition() {
    return this.conditionFor('RepaveInProgress');
  }

  get currentImageRevision() {
    return this.status?.currentImageRevision || '';
  }

  // The trigger was set to a value the operator has not processed yet
  get repaveRequested() {
    const trigger = this.metadata?.annotations?.[REPAVE_TRIGGER];

    return !!trigger && trigger !== this.status?.lastAppliedRepaveTrigger;
  }

  get isRepaving() {
    return this.repaveCondition?.status === 'True';
  }

  // The last request was refused (e.g. instance not Available); only worth
  // showing while there is still something to apply
  get repaveRefusal() {
    const c = this.repaveCondition;

    return c?.status === 'False' && REPAVE_REFUSED_REASONS.includes(c.reason) && !this.repaveRequested ? c : null;
  }

  get imageStatus() {
    if (this.isRepaving) {
      return IMAGE_STATUS.UPDATING;
    }
    if (this.repaveRequested) {
      return IMAGE_STATUS.REQUESTED;
    }

    const drift = this.imageDriftCondition;

    if (drift?.status === 'True' && drift.reason === 'EngineVersionEOL') {
      return IMAGE_STATUS.EOL;
    }
    if (drift?.status === 'True') {
      return IMAGE_STATUS.OS_UPDATE;
    }
    if (drift?.status === 'False') {
      return IMAGE_STATUS.UP_TO_DATE;
    }

    return IMAGE_STATUS.UNKNOWN;
  }

  // One short sentence about the drift for the UI, e.g. "Update from X to Y".
  // Falls back to the operator's own message (minus its kubectl hint) when the
  // message doesn't have the expected shape.
  get imageDriftSummary() {
    const drift = this.imageDriftCondition;

    if (!drift?.message) {
      return '';
    }

    if (drift.reason === 'OSUpdateAvailable') {
      const parsed = parseOSUpdate(drift.message);

      if (parsed) {
        return this.t('dbaas.instance.image.summary.osUpdate', { current: this.currentImageRevision || parsed.current, target: parsed.target });
      }
    } else if (drift.reason === 'EngineVersionEOL') {
      const parsed = parseEngineEOL(drift.message);

      if (parsed) {
        return this.t('dbaas.instance.image.summary.eol', {
          version: parsed.engineVersion, target: parsed.target, supported: parsed.supported.join(', ')
        });
      }
    } else if (drift.reason === 'ImageUpToDate') {
      return this.t('dbaas.instance.image.summary.upToDate', { current: this.currentImageRevision || '' });
    }

    return shortDriftMessage(drift.message);
  }

  // Sort/search value for the list column
  get imageStatusLabel() {
    return this.t(`dbaas.instance.image.status.${ this.imageStatus }`);
  }

  // Why "Apply OS Update" is unavailable, or '' when it can be applied
  get applyOSUpdateBlockedReason() {
    if (!this.canUpdate) {
      return this.t('dbaas.instance.image.blocked.noPermission');
    }
    if (this.imageStatus !== IMAGE_STATUS.OS_UPDATE) {
      return this.t('dbaas.instance.image.blocked.noUpdate');
    }
    if (this.isDeleting || this.phase !== DB_PHASE.AVAILABLE) {
      return this.t('dbaas.instance.image.blocked.notAvailable');
    }

    return '';
  }

  get canApplyOSUpdate() {
    return !this.applyOSUpdateBlockedReason;
  }

  applyOSUpdate() {
    this.$dispatch('promptModal', {
      component:  'DBaaSConfirmRepaveDialog',
      resources:  [this],
      modalWidth: '520px',
    });
  }

  // A fresh trigger value starts a repave (and is how a refused one is retried).
  // Merge patch, so it works whether or not the instance has annotations yet.
  requestRepave() {
    return this.patch({ metadata: { annotations: { [REPAVE_TRIGGER]: new Date().toISOString() } } },
      { headers: { 'content-type': 'application/merge-patch+json' } }, false, true);
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
