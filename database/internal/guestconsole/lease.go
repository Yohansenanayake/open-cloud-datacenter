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

package guestconsole

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationclient "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

const (
	LeaseDuration = 90 * time.Second
	RenewInterval = 30 * time.Second
)

var (
	ErrBusy      = errors.New("guest console is busy")
	ErrLeaseLost = errors.New("guest console lease was lost")
)

// LeaseName returns a DNS-safe, fixed-size name derived from the DBInstance
// UID. The UID hash prevents long UIDs from exceeding Kubernetes name limits.
func LeaseName(instanceUID string) string {
	digest := sha256.Sum256([]byte(instanceUID))
	return "dbaas-console-" + hex.EncodeToString(digest[:20])
}

type LeaseManager struct {
	Client coordinationclient.CoordinationV1Interface
	// Now is a function instead of calling time.Now() directly so tests can use a fixed clock and simulate expiry without waiting 90 seconds
	Now           func() time.Time
	Duration      time.Duration
	RenewInterval time.Duration
}

func NewLeaseManager(client coordinationclient.CoordinationV1Interface) *LeaseManager {
	return &LeaseManager{
		Client:        client,
		Now:           time.Now,
		Duration:      LeaseDuration,
		RenewInterval: RenewInterval,
	}
}

// LeaseGuard identifies an acquired Kubernetes Lease so this session can
// renew and release it.
type LeaseGuard struct {
	manager   *LeaseManager
	namespace string
	name      string
	holder    string
}

func (m *LeaseManager) Acquire(ctx context.Context, namespace, instanceUID, holder string) (*LeaseGuard, error) {
	if m == nil || m.Client == nil || namespace == "" || instanceUID == "" || holder == "" {
		return nil, errors.New("guest console lease input is incomplete")
	}
	if m.Now == nil || m.Duration <= 0 || m.RenewInterval <= 0 || m.RenewInterval >= m.Duration {
		return nil, errors.New("guest console lease configuration is invalid")
	}

	name := LeaseName(instanceUID)
	// leaseClient manages Lease resources in this namespace.
	leaseClient := m.Client.Leases(namespace)
	existing, err := leaseClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// microtime used for timestamp
		now := metav1.NewMicroTime(m.Now().UTC())
		durationSeconds := int32(m.Duration / time.Second)
		_, createErr := leaseClient.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       stringPointer(holder),
				LeaseDurationSeconds: &durationSeconds,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}, metav1.CreateOptions{})
		if createErr == nil {
			return &LeaseGuard{manager: m, namespace: namespace, name: name, holder: holder}, nil
		}
		if !apierrors.IsAlreadyExists(createErr) {
			return nil, fmt.Errorf("create guest console lease: %w", createErr)
		}
		// If the lease was created by another process between the Get and Create calls, we will get an AlreadyExists error.
		// In that case, we will try to Get the lease again to check if it is available for us to acquire.
		existing, err = leaseClient.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get guest console lease: %w", err)
	}
	if !m.available(existing, holder) {
		return nil, ErrBusy
	}
	//claim an available exising lease
	now := metav1.NewMicroTime(m.Now().UTC())
	previousHolder := value(existing.Spec.HolderIdentity)
	durationSeconds := int32(m.Duration / time.Second)
	existing.Spec.HolderIdentity = stringPointer(holder)
	existing.Spec.LeaseDurationSeconds = &durationSeconds
	existing.Spec.RenewTime = &now
	if previousHolder != holder {
		existing.Spec.AcquireTime = &now
		// counts ownership changes
		transitions := valueInt32(existing.Spec.LeaseTransitions) + 1
		existing.Spec.LeaseTransitions = &transitions
	}
	// k8s api server handles the final race condition check
	if _, err := leaseClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("acquire guest console lease: %w", err)
	}
	return &LeaseGuard{manager: m, namespace: namespace, name: name, holder: holder}, nil
}

// Lease is available if
// - it has no holder, or
// - it is held by the same holder, or
// - previous holder's lease has expired
func (m *LeaseManager) available(lease *coordinationv1.Lease, holder string) bool {
	currentHolder := value(lease.Spec.HolderIdentity)
	if currentHolder == "" || currentHolder == holder {
		return true
	}
	duration := m.Duration
	if lease.Spec.LeaseDurationSeconds != nil && *lease.Spec.LeaseDurationSeconds > 0 {
		duration = time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
	}
	//find the latest ownership timestamp to determine if the lease is expired.
	//fallback
	lastRenewed := lease.CreationTimestamp.Time
	if lease.Spec.AcquireTime != nil {
		//better than creation timestamp
		lastRenewed = lease.Spec.AcquireTime.Time
	}
	if lease.Spec.RenewTime != nil {
		//most reently updated timestamp
		lastRenewed = lease.Spec.RenewTime.Time
	}
	//check for expiry
	return !m.Now().Before(lastRenewed.Add(duration))
}

// RenewLoop renews the guard until ctx ends. A non-nil value means the caller
// must cancel its active console connection because exclusivity is no longer
// guaranteed.
func (g *LeaseGuard) RenewLoop(ctx context.Context) <-chan error {
	// reports renewal erros to the caller
	// It has capacity for one error because the loop stops after its first failure.
	// The goroutine can report that error without waiting for the caller to receive it immediately.
	result := make(chan error, 1)
	go func() {
		defer close(result)
		ticker := time.NewTicker(g.manager.RenewInterval)
		defer ticker.Stop()
		for {
			select {
			//waits for console session to end
			case <-ctx.Done():
				return
			// waits for the next renewal
			case <-ticker.C:
				if err := g.renew(ctx); err != nil {
					result <- ErrLeaseLost
					return
				}
			}
		}
	}()
	return result
}

func (g *LeaseGuard) renew(ctx context.Context) error {
	leaseClient := g.manager.Client.Leases(g.namespace)
	lease, err := leaseClient.Get(ctx, g.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if value(lease.Spec.HolderIdentity) != g.holder {
		return ErrLeaseLost
	}
	now := metav1.NewMicroTime(g.manager.Now().UTC())
	durationSeconds := int32(g.manager.Duration / time.Second)
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = &durationSeconds
	_, err = leaseClient.Update(ctx, lease, metav1.UpdateOptions{})
	return err
}

// Release clears this guard's holder identity. It does not delete the Lease,
// so every handoff retains Kubernetes resource-version conflict protection.
func (g *LeaseGuard) Release(ctx context.Context) error {
	leaseClient := g.manager.Client.Leases(g.namespace)
	lease, err := leaseClient.Get(ctx, g.name, metav1.GetOptions{})
	// If the lease is already gone, we can consider it released.
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get guest console lease for release: %w", err)
	}
	// This guard is no longer the holder, so we cannot release it.
	if value(lease.Spec.HolderIdentity) != g.holder {
		return nil
	}
	// Clear ownership only
	now := metav1.NewMicroTime(g.manager.Now().UTC())
	lease.Spec.HolderIdentity = nil
	lease.Spec.RenewTime = &now
	if _, err := leaseClient.Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("release guest console lease: %w", err)
	}
	return nil
}

func stringPointer(value string) *string { return &value }

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func valueInt32(pointer *int32) int32 {
	if pointer == nil {
		return 0
	}
	return *pointer
}
