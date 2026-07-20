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

package credentials

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// defaultMasterUser mirrors the controller's own default (dbinstance_types.go
// doc comment: "MasterUsername ... Default 'dbadmin'"). Duplicated as a small
// stable literal rather than threading it through the Resolve call signature.
const defaultMasterUser = "dbadmin"

// TenantCredentialsSecretName, InternalSecretName, and TLSSecretName are the
// deterministic Secret names Resolver reads and creates. All three are
// recomputable from the live DBInstance (name/UID) — never status-memory.
func TenantCredentialsSecretName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("pg-%s-credentials", inst.Name)
}

func InternalSecretName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("dbi-%s-internal", inst.UID)
}

func TLSSecretName(inst *dbaasv1.DBInstance) string {
	return fmt.Sprintf("dbi-%s-tls", inst.UID)
}

// Resolver resolves the durable credential/TLS Material for a DBInstance. It
// generates each backing Secret exactly once and reuses it on every later
// call — regenerating after a VM has already booted with the old password/CA
// would diverge from the running instance.
type Resolver struct {
	Client client.Client
	// Scheme is used to stamp a controller owner reference on the tenant
	// credentials Secret (same-namespace). May be nil in tests that don't
	// assert on owner refs.
	Scheme *runtime.Scheme
	// OperatorNamespace is where the two controller-private Secrets (internal
	// DB credentials, TLS) live — outside the tenant namespace.
	OperatorNamespace string
}

// ResolveResult reports both the resolved material and whether this call
// changed cluster state. Callers use Changed to enforce a reconcile boundary
// and re-observe the persisted Secrets before advancing.
type ResolveResult struct {
	Material *Material
	Changed  bool
}

// Resolve returns the Material for inst, generating and persisting whatever
// is missing. Existing Secrets are validated but never regenerated or repaired:
// rotating durable material behind an already-booted VM would break it.
func (r *Resolver) Resolve(ctx context.Context, inst *dbaasv1.DBInstance) (ResolveResult, error) {
	adminUser, adminPassword, tenantChanged, err := r.getOrCreateTenant(ctx, inst)
	if err != nil {
		return ResolveResult{}, err
	}
	replPw, exporterPw, internalChanged, err := r.getOrCreateInternal(ctx, inst)
	if err != nil {
		return ResolveResult{}, err
	}
	vmName := harvester.VMName(inst.Name)
	tls, tlsChanged, err := r.getOrCreateTLS(ctx, inst, vmName)
	if err != nil {
		return ResolveResult{}, err
	}

	return ResolveResult{
		Material: &Material{
			AdminUser:        adminUser,
			AdminPassword:    adminPassword,
			ReplPassword:     replPw,
			ExporterPassword: exporterPw,
			TLS:              tls,
		},
		Changed: tenantChanged || internalChanged || tlsChanged,
	}, nil
}

// getOrCreateTenant resolves the tenant admin-credentials Secret
// (admin_user/admin_password only). On a concurrent-create race it adopts
// the winner's material instead of the caller's, so the returned values
// always match what is actually persisted.
func (r *Resolver) getOrCreateTenant(ctx context.Context, inst *dbaasv1.DBInstance) (adminUser, adminPassword string, changed bool, err error) {
	key := types.NamespacedName{Namespace: inst.Namespace, Name: TenantCredentialsSecretName(inst)}
	var sec corev1.Secret
	if getErr := r.Client.Get(ctx, key, &sec); getErr == nil {
		adminUser, adminPassword, err = tenantMaterialFrom(&sec, key)
		return adminUser, adminPassword, false, err
	} else if !apierrors.IsNotFound(getErr) {
		return "", "", false, getErr
	}

	adminUser = inst.Spec.MasterUsername
	if adminUser == "" {
		adminUser = defaultMasterUser
	}
	adminPassword, err = randomString(32)
	if err != nil {
		return "", "", false, fmt.Errorf("generate admin password: %w", err)
	}

	newSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    map[string]string{dbaasv1.LabelInstance: inst.Name},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"admin_user": adminUser, "admin_password": adminPassword},
	}
	if r.Scheme != nil {
		if err := controllerutil.SetControllerReference(inst, newSec, r.Scheme); err != nil {
			return "", "", false, err
		}
	}
	if err := r.Client.Create(ctx, newSec); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", "", false, err
		}
		// race winner already created the secret
		var won corev1.Secret
		if gerr := r.Client.Get(ctx, key, &won); gerr != nil {
			return "", "", false, gerr
		}
		adminUser, adminPassword, err = tenantMaterialFrom(&won, key)
		return adminUser, adminPassword, true, err
	}
	return adminUser, adminPassword, true, nil
}

// getOrCreateInternal resolves the operator-namespace internal-credentials
// Secret (repl_password, exporter_password).
func (r *Resolver) getOrCreateInternal(ctx context.Context, inst *dbaasv1.DBInstance) (replPw, exporterPw string, changed bool, err error) {
	key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: InternalSecretName(inst)}
	var sec corev1.Secret
	if getErr := r.Client.Get(ctx, key, &sec); getErr == nil {
		replPw, exporterPw, err = internalMaterialFrom(&sec, key)
		return replPw, exporterPw, false, err
	} else if !apierrors.IsNotFound(getErr) {
		return "", "", false, getErr //Transient Error
	}

	replPw, err = randomString(32)
	if err != nil {
		return "", "", false, fmt.Errorf("generate replication password: %w", err)
	}
	exporterPw, err = randomString(24)
	if err != nil {
		return "", "", false, fmt.Errorf("generate exporter password: %w", err)
	}

	newSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels: map[string]string{
				dbaasv1.LabelInstance:      inst.Name,
				dbaasv1.LabelDBInstanceUID: string(inst.UID),
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"repl_password": replPw, "exporter_password": exporterPw},
	}
	if err := r.Client.Create(ctx, newSec); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", "", false, err
		}
		var won corev1.Secret
		if gerr := r.Client.Get(ctx, key, &won); gerr != nil {
			return "", "", false, gerr
		}
		replPw, exporterPw, err = internalMaterialFrom(&won, key)
		return replPw, exporterPw, true, err
	}
	return replPw, exporterPw, true, nil
}

// getOrCreateTLS resolves the operator-namespace private TLS Secret: CA +
// server cert signed by it, CN/SAN = vmName.
func (r *Resolver) getOrCreateTLS(ctx context.Context, inst *dbaasv1.DBInstance, vmName string) (*TLSBundle, bool, error) {
	key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: TLSSecretName(inst)}
	var sec corev1.Secret
	if getErr := r.Client.Get(ctx, key, &sec); getErr == nil {
		bundle, err := tlsBundleFrom(&sec, key)
		return bundle, false, err
	} else if !apierrors.IsNotFound(getErr) {
		return nil, false, getErr
	}

	bundle, genErr := generateTLS(vmName)
	if genErr != nil {
		return nil, false, fmt.Errorf("TLS generation: %w", genErr)
	}

	newSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels: map[string]string{
				dbaasv1.LabelInstance:      inst.Name,
				dbaasv1.LabelDBInstanceUID: string(inst.UID),
			},
		},
		Type: corev1.SecretTypeTLS,
		StringData: map[string]string{
			"ca.crt":  bundle.CACertPEM,
			"ca.key":  bundle.CAKeyPEM,
			"tls.crt": bundle.ServerCertPEM,
			"tls.key": bundle.ServerKeyPEM,
		},
	}
	if err := r.Client.Create(ctx, newSec); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, false, err
		}
		var won corev1.Secret
		if gerr := r.Client.Get(ctx, key, &won); gerr != nil {
			return nil, false, gerr
		}
		wonBundle, err := tlsBundleFrom(&won, key)
		return wonBundle, true, err
	}
	return bundle, true, nil
}

// get reads a Secret key from Data (populated by the apiserver), falling back
// to StringData (set on a freshly built object, e.g. under the fake client).
func get(s *corev1.Secret, key string) string {
	if v, ok := s.Data[key]; ok {
		return string(v)
	}
	return s.StringData[key]
}

func tenantMaterialFrom(s *corev1.Secret, key types.NamespacedName) (string, string, error) {
	user, password := get(s, "admin_user"), get(s, "admin_password")
	if user == "" || password == "" {
		return "", "", fmt.Errorf("credentials secret %s/%s is missing admin_user or admin_password", key.Namespace, key.Name)
	}
	return user, password, nil
}

func internalMaterialFrom(s *corev1.Secret, key types.NamespacedName) (string, string, error) {
	repl, exporter := get(s, "repl_password"), get(s, "exporter_password")
	if repl == "" || exporter == "" {
		return "", "", fmt.Errorf("internal credentials secret %s/%s is missing repl_password or exporter_password", key.Namespace, key.Name)
	}
	return repl, exporter, nil
}

func tlsBundleFrom(s *corev1.Secret, key types.NamespacedName) (*TLSBundle, error) {
	bundle := &TLSBundle{
		CACertPEM:     get(s, "ca.crt"),
		CAKeyPEM:      get(s, "ca.key"),
		ServerCertPEM: get(s, "tls.crt"),
		ServerKeyPEM:  get(s, "tls.key"),
	}
	if bundle.CACertPEM == "" || bundle.CAKeyPEM == "" || bundle.ServerCertPEM == "" || bundle.ServerKeyPEM == "" {
		return nil, fmt.Errorf("TLS secret %s/%s is missing required CA or server certificate material", key.Namespace, key.Name)
	}
	return bundle, nil
}
