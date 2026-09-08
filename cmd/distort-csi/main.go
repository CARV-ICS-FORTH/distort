package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/csi"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(storagev1alpha1.AddToScheme(scheme))
}

func main() {
	var endpoint string
	var nodeID string
	flag.StringVar(&endpoint, "endpoint", csi.DefaultEndpoint, "CSI endpoint")
	flag.StringVar(&nodeID, "nodeid", "", "Kubernetes node ID")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if nodeID == "" {
		setupLog.Error(nil, "Node ID is required")
		os.Exit(1)
	}

	setupLog.Info("Starting DISTORT CSI driver", "nodeID", nodeID, "endpoint", endpoint)

	// Initialize K8s Client
	cfg, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "Unable to get Kubernetes configuration")
		os.Exit(1)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Unable to create Kubernetes client")
		os.Exit(1)
	}

	// Initialize CSI Driver (Identity, Controller, Node servers)
	driver := csi.NewDriver(nodeID, endpoint, k8sClient)
	if err := driver.Run(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "CSI driver stopped")
		os.Exit(1)
	}
}
