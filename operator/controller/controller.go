// Package controller contains the Kubernetes controllers for miniMesh CRDs.
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	meshv1 "github.com/ianchen0119/miniMesh/operator/api/v1alpha1"
	"github.com/ianchen0119/miniMesh/pkg/cert"
)

// ─────────────────────────────────────────────────────────────────────────────
// MeshPolicyReconciler
// ─────────────────────────────────────────────────────────────────────────────

// MeshPolicyReconciler reconciles MeshPolicy objects.
type MeshPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=mesh.minimesh.io,resources=meshpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=mesh.minimesh.io,resources=meshpolicies/status,verbs=update;patch

// Reconcile ensures the MeshPolicy status reflects the current desired state.
func (r *MeshPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var policy meshv1.MeshPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if policy.Spec.MTLSMode == "" {
		policy.Spec.MTLSMode = "STRICT"
	}
	logger.Info("reconciling MeshPolicy",
		"name", policy.Name, "mtlsMode", policy.Spec.MTLSMode)

	policy.Status.Ready = true
	policy.Status.Phase = "Active"
	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller with the manager.
func (r *MeshPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&meshv1.MeshPolicy{}).
		Complete(r)
}

// ─────────────────────────────────────────────────────────────────────────────
// MeshCertificateReconciler
// ─────────────────────────────────────────────────────────────────────────────

// MeshCertificateReconciler reconciles MeshCertificate objects.
// It uses the shared CA to issue short-lived TLS certificates and stores them
// in Kubernetes Secrets for workloads to mount.
type MeshCertificateReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	CA     *cert.CA
}

// +kubebuilder:rbac:groups=mesh.minimesh.io,resources=meshcertificates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=mesh.minimesh.io,resources=meshcertificates/status,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// Reconcile issues a certificate for the service and stores it in a Secret.
func (r *MeshCertificateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var mc meshv1.MeshCertificate
	if err := r.Get(ctx, req.NamespacedName, &mc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	dnsName := fmt.Sprintf("%s.%s.svc.cluster.local", mc.Spec.ServiceName, mc.Spec.Namespace)
	logger.Info("issuing certificate", "dnsName", dnsName)

	bundle, err := r.CA.Issue(dnsName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("issue cert: %w", err)
	}

	secretName := "minimesh-cert-" + mc.Spec.ServiceName
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: mc.Spec.Namespace,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Type = corev1.SecretTypeTLS
		secret.Data = map[string][]byte{
			corev1.TLSCertKey:       bundle.CertPEM,
			corev1.TLSPrivateKeyKey: bundle.KeyPEM,
		}
		return nil
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("create/update secret: %w", err)
	}

	mc.Status.Ready = true
	mc.Status.CertSecretRef = secretName
	mc.Status.ExpiresAt = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if err := r.Status().Update(ctx, &mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// Re-issue before the cert expires (every 12 h).
	return ctrl.Result{RequeueAfter: 12 * time.Hour}, nil
}

// SetupWithManager registers the controller with the manager.
func (r *MeshCertificateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&meshv1.MeshCertificate{}).
		Complete(r)
}
