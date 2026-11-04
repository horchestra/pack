package kube

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/horchestra/pack/pkg/target/remote"
)

// A target's image in a Pod of a real cluster: started, spoken to through a port-forward, taken away on Close. It
// runs when PACK_KUBE_E2E_KUBECONFIG names a cluster's kubeconfig (a kind cluster of the machine) and
// PACK_KUBE_E2E_IMAGE an image of a target there (kind load docker-image).
func TestLiveCluster(t *testing.T) {
	kubeconfig, image := os.Getenv("PACK_KUBE_E2E_KUBECONFIG"), os.Getenv("PACK_KUBE_E2E_IMAGE")
	if len(kubeconfig) == 0 || len(image) == 0 {
		t.Skip("PACK_KUBE_E2E_KUBECONFIG and PACK_KUBE_E2E_IMAGE name a cluster and a target's image in it")
	}
	rt, _, err := remote.RuntimeOf(map[string]string{"runtime_type": "k8s", "runtime_kubeconfig": kubeconfig, "runtime_namespace": "default"})
	if err != nil {
		t.Fatal(err)
	}
	rt.Image = image
	p, err := rt.Start("k8s", "inst-live")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Capabilities()) == 0 {
		t.Error("no capabilities in the handshake")
	}
	rc, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(rc)
	ctx := context.Background()
	sel := metav1.ListOptions{LabelSelector: remote.LabelInstallation + "=inst-live"}
	pods, _ := cs.CoreV1().Pods("default").List(ctx, sel)
	svcs, _ := cs.CoreV1().Services("default").List(ctx, sel)
	if len(pods.Items) != 1 || len(svcs.Items) != 1 {
		t.Fatalf("pods %d, services %d", len(pods.Items), len(svcs.Items))
	}
	t.Logf("handshake with %s: %v", pods.Items[0].Name, p.Capabilities())
	p.Close()
	deadline := time.Now().Add(90 * time.Second)
	for {
		pods, _ := cs.CoreV1().Pods("default").List(ctx, sel)
		svcs, _ := cs.CoreV1().Services("default").List(ctx, sel)
		secrets, _ := cs.CoreV1().Secrets("default").List(ctx, sel)
		if len(pods.Items)+len(svcs.Items)+len(secrets.Items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after Close: pods %d, services %d, secrets %d", len(pods.Items), len(svcs.Items), len(secrets.Items))
		}
		time.Sleep(2 * time.Second)
	}
}
