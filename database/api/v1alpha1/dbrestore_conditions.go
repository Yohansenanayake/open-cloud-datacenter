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

// DBRestoreFinalizerName gates removal until any target DBInstance this
// restore created is confirmed torn down and the restore hold is released.
const DBRestoreFinalizerName = "dbaas.opencloud.wso2.com/restore-cleanup"

// Reason values for DBRestoreStatus.Reason. DBRestore has no Conditions list
// (status.stage/reason on an object the controller fully owns is already the
// durable fact) but reuses the shared ConditionReason type so
// TestProductionConditionReasonsUseConstants' raw-string check applies here
// too. Reasons are output only — never read back to steer reconciliation.
const (
	// Preparing.
	ReasonRestoreSnapshotNotFound         ConditionReason = "SnapshotNotFound"
	ReasonRestoreSnapshotNotReady         ConditionReason = "SnapshotNotReady"
	ReasonRestoreSnapshotFailed           ConditionReason = "SnapshotFailed"
	ReasonRestoreSnapshotReplaced         ConditionReason = "SnapshotReplaced"
	ReasonRestoreSnapshotDeleting         ConditionReason = "SnapshotDeleting"
	ReasonRestoreVolumeSnapshotMissing    ConditionReason = "VolumeSnapshotMissing"
	ReasonRestoreVolumeSnapshotFailed     ConditionReason = "VolumeSnapshotFailed"
	ReasonRestoreInvalidSnapshotState     ConditionReason = "InvalidSnapshotState"
	ReasonRestoreAllocatedStorageTooSmall ConditionReason = "AllocatedStorageTooSmall"

	// RestoringVolume.
	ReasonRestoreHoldWaiting     ConditionReason = "RestoreHoldWaiting"
	ReasonRestoreVolumeRestoring ConditionReason = "VolumeRestoring"
	ReasonRestorePVCConflict     ConditionReason = "RestorePVCConflict"
	ReasonRestorePVCLost         ConditionReason = "RestorePVCLost"

	// StartingDatabase.
	ReasonRestoreTargetStarting     ConditionReason = "TargetStarting"
	ReasonRestoreTargetNameConflict ConditionReason = "TargetNameConflict"
	ReasonRestoreTargetRejected     ConditionReason = "TargetRejected"
	ReasonRestoreTargetInvalid      ConditionReason = "TargetInvalid"
	ReasonRestoreTargetLost         ConditionReason = "TargetLost"

	// Terminal / deletion.
	ReasonRestoreTimedOut   ConditionReason = "RestoreTimedOut"
	ReasonRestoreSucceeded  ConditionReason = "Succeeded"
	ReasonRestoreCancelling ConditionReason = "Cancelling"
)
