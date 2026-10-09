import SteveModel from '@shell/plugins/steve/steve-class';
import { DBAAS, RESTORE_QUERY, SNAPSHOT_ORIGIN, SNAPSHOT_PHASE } from '../types';
import { restoreCreateLocation } from '../utils/dbrestore';

// Set only by the operator's scheduler on automated snapshots
const ORIGIN_LABEL = 'dbaas.opencloud.wso2.com/snapshot-origin';

// Ready=False reasons the operator never retries (isTerminalSnapshotReason)
const TERMINAL_REASONS = [
  'SourceNotFound',
  'SourceBackupDisabled',
  'SourceNotReady',
  'SourceDeleting',
  'BackupFailed',
  'BackupTimedOut',
];

const PHASE_COLOR = {
  [SNAPSHOT_PHASE.READY]:       'success',
  [SNAPSHOT_PHASE.QUEUED]:      'info',
  [SNAPSHOT_PHASE.IN_PROGRESS]: 'info',
  [SNAPSHOT_PHASE.DELETING]:    'info',
  [SNAPSHOT_PHASE.FAILED]:      'error',
};

const PHASE_LABEL = { [SNAPSHOT_PHASE.IN_PROGRESS]: 'In Progress' };

export default class DBSnapshot extends SteveModel {
  // No cards on the detail page: Shell's default Resources card lists the
  // operator-owned child objects, and with one card present Shell also adds a
  // generic "Extras" card (same as DBInstance)
  get cards() {
    return [];
  }

  get readyCondition() {
    return (this.status?.conditions || []).find((c) => c.type === 'Ready');
  }

  // status.phase from the operator; older snapshots (written before the field
  // existed) get the same derivation the operator uses (snapshotPhase)
  get phase() {
    if (this.metadata?.deletionTimestamp) {
      return SNAPSHOT_PHASE.DELETING;
    }
    if (this.status?.phase) {
      return this.status.phase;
    }

    const ready = this.readyCondition;

    if (ready?.status === 'True') {
      return SNAPSHOT_PHASE.READY;
    }
    if (TERMINAL_REASONS.includes(ready?.reason)) {
      return SNAPSHOT_PHASE.FAILED;
    }
    if (ready?.reason === 'BackupInProgress') {
      return SNAPSHOT_PHASE.IN_PROGRESS;
    }

    return SNAPSHOT_PHASE.QUEUED;
  }

  get isReady() {
    return this.phase === SNAPSHOT_PHASE.READY;
  }

  get state() {
    return this.phase.toLowerCase();
  }

  get stateDisplay() {
    return PHASE_LABEL[this.phase] || this.phase;
  }

  get stateColor() {
    return `text-${ PHASE_COLOR[this.phase] || 'warning' }`;
  }

  get hasProblem() {
    return this.phase === SNAPSHOT_PHASE.FAILED;
  }

  // Shape shared with DBInstance, so the DBInstanceState cell can render both
  get hasPendingChanges() {
    return false;
  }

  get stateMessages() {
    const ready = this.readyCondition;

    if (!ready || ready.status === 'True' || !(ready.message || ready.reason)) {
      return [];
    }

    return [{
      type: 'Ready', reason: ready.reason, message: ready.message || ready.reason
    }];
  }

  get origin() {
    if (this.status?.origin) {
      return this.status.origin;
    }

    return this.metadata?.labels?.[ORIGIN_LABEL] === SNAPSHOT_ORIGIN.AUTOMATED ? SNAPSHOT_ORIGIN.AUTOMATED : SNAPSHOT_ORIGIN.MANUAL;
  }

  get isAutomated() {
    return this.origin === SNAPSHOT_ORIGIN.AUTOMATED;
  }

  get source() {
    return this.status?.source || {};
  }

  get sourceName() {
    return this.spec?.sourceInstanceRef?.name || '';
  }

  // The live source instance, if it still exists and is the one this snapshot
  // was taken from (an instance re-created with the same name is not)
  get sourceInstance() {
    const instance = this.$getters['byId'](DBAAS.INSTANCE, `${ this.namespace }/${ this.sourceName }`);

    if (!instance) {
      return null;
    }
    if (this.source.instanceUID && instance.metadata?.uid !== this.source.instanceUID) {
      return null;
    }

    return instance;
  }

  get engineDisplay() {
    return this.source.engineVersion ? `PostgreSQL ${ this.source.engineVersion }` : '';
  }

  get sizeGiB() {
    return this.source.allocatedStorage || 0;
  }

  get sizeDisplay() {
    return this.sizeGiB ? `${ this.sizeGiB } GiB` : '';
  }

  get progress() {
    if (this.isReady) {
      return 100;
    }

    return typeof this.status?.progress === 'number' ? this.status.progress : null;
  }

  get startTime() {
    return this.status?.startTime || '';
  }

  get completionTime() {
    if (this.status?.completionTime) {
      return this.status.completionTime;
    }

    // Older snapshots: when Ready last changed is when they finished
    return this.isReady ? this.readyCondition?.lastTransitionTime || '' : '';
  }

  // The spec is immutable, so there is nothing to edit or clone
  get canCustomEdit() {
    return false;
  }

  get canRestore() {
    const schema = this.$rootGetters['cluster/schemaFor'](DBAAS.RESTORE);

    return this.isReady && !!schema?.collectionMethods?.find((m) => m.toLowerCase() === 'post');
  }

  get _availableActions() {
    const hidden = ['goToEdit', 'goToClone', 'cloneYaml'];
    const out = super._availableActions.filter((a) => !hidden.includes(a.action));

    out.unshift({
      action:  'restoreToNewInstance',
      label:   this.t('dbaas.restore.actions.restoreNew'),
      icon:    'icon icon-backup-restore',
      enabled: this.canRestore,
    });

    return out;
  }

  restoreToNewInstance() {
    this.currentRouter().push(restoreCreateLocation(this.$rootGetters['clusterId'], { [RESTORE_QUERY.SNAPSHOT]: this.id }));
  }

  get warnDeletionMessage() {
    return this.isAutomated ? this.t('dbaas.snapshot.delete.warningAutomated') : this.t('dbaas.snapshot.delete.warning');
  }
}
