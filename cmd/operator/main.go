// minimesh-operator is the Kubernetes controller manager for miniMesh CRDs.
// It manages MeshPolicy (mTLS enforcement) and MeshCertificate (cert issuance)
// resources in the cluster.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	meshv1 "github.com/ianchen0119/miniMesh/operator/api/v1alpha1"
	"github.com/ianchen0119/miniMesh/operator/controller"
	"github.com/ianchen0119/miniMesh/pkg/cert"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meshv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint address")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	ca, err := cert.NewCA()
	if err != nil {
		logger.Error(err, "failed to create CA")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err := (&controller.MeshPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up MeshPolicy controller")
		os.Exit(1)
	}

	if err := (&controller.MeshCertificateReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		CA:     ca,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to set up MeshCertificate controller")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}
