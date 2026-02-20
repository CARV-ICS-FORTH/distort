package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	storagev1alpha1 "distort/api/v1alpha1"
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
	var nodeId string
	flag.StringVar(&endpoint, "endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	flag.StringVar(&nodeId, "nodeid", "", "node id")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if nodeId == "" {
		setupLog.Error(nil, "nodeid is required")
		os.Exit(1)
	}

	setupLog.Info("Starting Distort CSI Driver", "nodeID", nodeId, "endpoint", endpoint)

	// TODO: Initialize CSI Driver (Identity, Controller, Node servers)
	// driver := csi.NewDriver(nodeId, endpoint)
	// driver.Run()

	select {} // block forever
}
