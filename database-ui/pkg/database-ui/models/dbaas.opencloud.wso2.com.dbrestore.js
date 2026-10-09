import SteveModel from '@shell/plugins/steve/steve-class';
import { DBAAS, RESTORE_QUERY, RESTORE_STAGE } from '../types';
import { RESTORE_STEPS, restoreCreateLocation } from '../utils/dbrestore';

const CANCELLING = 'Cancelling';

const STAGE_COLOR = {
  [RESTORE_STAGE.PREPARING]:         'info',
  [RESTORE_STAGE.RESTORING_VOLUME]:  'info',
  [RESTORE_STAGE.STARTING_DATABASE]: 'info',
  [RESTORE_STAGE.SUCCEEDED]:         'success',
  [RESTORE_STAGE.FAILED]:            'error',
  [CANCELLING]:                      'info',
};

export default class DBRestore extends SteveModel {
  // No cards on the detail page: Shell's default Resources card lists the
  // operator-owned child objects, and with one card present Shell also adds a
  // generic "Extras" card (same as DBInstance)
  get cards() {
    return [];
  }

  // The operator sets no stage until its first pass; Preparing is what follows
  get stage() {
    if (this.metadata?.deletionTimestamp && !this.isFinished) {
      return CANCELLING;
    }

    return this.status?.stage || RESTORE_STAGE.PREPARING;
  }

  get isFinished() {
    const stage = this.status?.stage;

    return stage === RESTORE_STAGE.SUCCEEDED || stage === RESTORE_STAGE.FAILED;
  }

  get isFailed() {
    return this.status?.stage === RESTORE_STAGE.FAILED;
  }

  get isSucceeded() {
    return this.status?.stage === RESTORE_STAGE.SUCCEEDED;
  }

  get state() {
    return this.stage.toLowerCase();
  }

  get stateDisplay() {
    return this.t(`dbaas.restore.stage.${ this.stage }`);
  }

  get stateColor() {
    return `text-${ STAGE_COLOR[this.stage] || 'warning' }`;
  }

  get hasProblem() {
    return this.isFailed;
  }

  // Shape shared with DBInstance, so the DBInstanceState cell can render both
  get hasPendingChanges() {
    return false;
  }

  get stateMessages() {
    const { reason, message } = this.status || {};

    if (this.isSucceeded || !(reason || message)) {
      return [];
    }

    return [{
      type: this.stage, reason, message: message || reason
    }];
  }

  // Index in RESTORE_STEPS of the step in progress (or reached)
  get stepIndex() {
    const i = RESTORE_STEPS.indexOf(this.status?.stage);

    return i < 0 ? 0 : i;
  }

  get snapshotName() {
    return this.spec?.snapshotRef?.name || '';
  }

  // The snapshot still exists and is the one this restore used
  get snapshot() {
    const snapshot = this.$getters['byId'](DBAAS.SNAPSHOT, `${ this.namespace }/${ this.snapshotName }`);

    if (!snapshot) {
      return null;
    }
    if (this.status?.snapshotUID && snapshot.metadata?.uid !== this.status.snapshotUID) {
      return null;
    }

    return snapshot;
  }

  get sourceInstanceName() {
    return this.status?.sourceInstanceName || '';
  }

  // The source instance, if it still exists (matched by UID)
  get sourceInstance() {
    const instance = this.$getters['byId'](DBAAS.INSTANCE, `${ this.namespace }/${ this.sourceInstanceName }`);

    return instance && instance.metadata?.uid === this.status?.sourceInstanceUID ? instance : null;
  }

  // Shown next to a name that has no live object to link to
  get snapshotMissingNote() {
    return this.snapshotName && !this.snapshot ? this.t('dbaas.snapshot.sourceDeleted') : '';
  }

  get sourceMissingNote() {
    return this.sourceInstanceName && !this.sourceInstance ? this.t('dbaas.snapshot.sourceDeleted') : '';
  }

  // Before the restore creates its instance there is nothing to link to yet
  get targetMissingNote() {
    if (this.targetInstance) {
      return '';
    }

    return this.isSucceeded ? this.t('dbaas.snapshot.sourceDeleted') : this.t('dbaas.restore.notCreated');
  }

  get targetName() {
    return this.spec?.targetInstanceName || '';
  }

  // The instance this restore created (an unrelated instance of the same
  // name, e.g. after a TargetNameConflict, is not it)
  get targetInstance() {
    const instance = this.$getters['byId'](DBAAS.INSTANCE, `${ this.namespace }/${ this.targetName }`);

    return instance?.spec?.restoredFrom?.dbRestoreUID === this.metadata?.uid ? instance : null;
  }

  get deadline() {
    return this.isFinished ? '' : this.status?.deadline || '';
  }

  get canCreateRestore() {
    const schema = this.$rootGetters['cluster/schemaFor'](DBAAS.RESTORE);

    return !!schema?.collectionMethods?.find((m) => m.toLowerCase() === 'post');
  }

  // The spec is immutable, so there is nothing to edit or clone
  get canCustomEdit() {
    return false;
  }

  get _availableActions() {
    const hidden = ['goToEdit', 'goToClone', 'cloneYaml'];
    const out = super._availableActions.filter((a) => !hidden.includes(a.action));

    out.unshift(
      {
        action:  'goToTarget',
        label:   this.t('dbaas.restore.actions.goToInstance'),
        icon:    'icon icon-external-link',
        enabled: !!this.targetInstance,
      },
      {
        action:  'retryRestore',
        label:   this.t('dbaas.restore.actions.retry'),
        icon:    'icon icon-backup-restore',
        enabled: this.isFailed && this.canCreateRestore,
      },
    );

    return out;
  }

  goToTarget() {
    if (this.targetInstance) {
      this.currentRouter().push(this.targetInstance.detailLocation);
    }
  }

  // A failed restore is final; retrying creates a new DBRestore with the same
  // settings (the operator cleans up the failed attempt)
  retryRestore() {
    this.currentRouter().push(restoreCreateLocation(this.$rootGetters['clusterId'], { [RESTORE_QUERY.RETRY]: this.id }));
  }

  // --- Deletion (read by the delete dialog) --------------------------------

  // Deleting a running restore cancels it and deletes its unfinished instance
  get confirmRemove() {
    return !this.isFinished;
  }

  get warnDeletionMessage() {
    if (!this.isFinished) {
      return this.t('dbaas.restore.delete.cancelWarning', { target: this.targetName });
    }

    return this.t('dbaas.restore.delete.recordOnly', { target: this.targetName });
  }
}
