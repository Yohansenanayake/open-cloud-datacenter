/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

// DBSnapshotFinalizerName gates removal until the backend Harvester backup
// is confirmed deleted. Distinct from DBInstance's FinalizerName even though
// both currently share the same literal cleanup purpose — kept separate so
// each kind's finalizer is unambiguous when read off a live object.
const DBSnapshotFinalizerName = "dbaas.opencloud.wso2.com/snapshot-cleanup"

// ConditionSnapshotReady is the one condition type this design's single-writer
// rule needs for DBSnapshot: only DBSnapshotReconciler ever writes it.
const ConditionSnapshotReady = "Ready"

const (
	ReasonSnapshotSourceNotFound       ConditionReason = "SourceNotFound"
	ReasonSnapshotSourceBackupDisabled ConditionReason = "SourceBackupDisabled"
	ReasonSnapshotSourceNotReady       ConditionReason = "SourceNotReady"
	ReasonSnapshotSourceDeleting       ConditionReason = "SourceDeleting"
	ReasonSnapshotHoldWaiting          ConditionReason = "SnapshotHoldWaiting"
	// ReasonSnapshotBackupQueued: admitted, waiting for a backup slot (the
	// cluster-wide cap on backups in flight).
	ReasonSnapshotBackupQueued ConditionReason = "BackupQueued"
	// ReasonSnapshotBackupTimedOut: the backup didn't finish within
	// backup.timeout of its creation; it was deleted and its slot freed.
	ReasonSnapshotBackupTimedOut     ConditionReason = "BackupTimedOut"
	ReasonSnapshotBackupInProgress   ConditionReason = "BackupInProgress"
	ReasonSnapshotBackupReady        ConditionReason = "BackupReady"
	ReasonSnapshotBackupFailed       ConditionReason = "BackupFailed"
	ReasonSnapshotFinalizerAdded     ConditionReason = "FinalizerAdded"
	ReasonSnapshotDeletionInProgress ConditionReason = "DeletionInProgress"
	// ReasonSnapshotDeletionWaitingForRestore: Ready=False while deletion
	// waits for a restore reading the source's snapshots to end.
	ReasonSnapshotDeletionWaitingForRestore ConditionReason = "DeletionWaitingForRestore"
)
