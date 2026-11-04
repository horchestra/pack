package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/horchestra/pack/pkg/target/remote"
)

func testInstance(cfg config) instance {
	return instance{name: "pack-target-k8s-0123456789", target: "k8s", installation: "inst-1", image: "reg/targets/k8s@sha256:abc",
		token: "secret-token", owner: "host-a/42", namespace: "pack", env: map[string]string{"VAULT_TOKEN": "s.x"}, cfg: cfg}
}

// The Pod reads its token and runtime.env from its Secret, never from its spec; the Service selects it alone; the
// NetworkPolicy is there only when asked for.
func TestObjects(t *testing.T) {
	pod, secret, svc, np := objects(testInstance(config{serviceAccount: "pack-target", pullSecret: "regcred"}))
	c := pod.Spec.Containers[0]
	if c.Image != "reg/targets/k8s@sha256:abc" || pod.Spec.ServiceAccountName != "pack-target" || pod.Spec.RestartPolicy != corev1.RestartPolicyNever ||
		!c.Stdin || pod.Spec.ImagePullSecrets[0].Name != "regcred" {
		t.Errorf("pod: %+v", pod.Spec)
	}
	for _, e := range c.Env {
		if e.Name == remote.EnvToken || strings.Contains(e.Value, "secret-token") || e.Name == "VAULT_TOKEN" {
			t.Errorf("a secret in the Pod's spec: %+v", e)
		}
	}
	if c.EnvFrom[0].SecretRef.Name != pod.Name || string(secret.Data[remote.EnvToken]) != "secret-token" || string(secret.Data["VAULT_TOKEN"]) != "s.x" {
		t.Errorf("the secret: %+v, envFrom %+v", secret.Data, c.EnvFrom)
	}
	if svc.Spec.Selector[LabelInstance] != pod.Name || len(svc.Spec.Selector) != 1 || svc.Spec.Ports[0].Port != 7353 {
		t.Errorf("service: %+v", svc.Spec)
	}
	for _, m := range []metav1.ObjectMeta{pod.ObjectMeta, secret.ObjectMeta, svc.ObjectMeta} {
		if m.Labels[remote.LabelInstallation] != "inst-1" || m.Labels[remote.LabelTarget] != "k8s" || m.Annotations[remote.LabelOwner] != "host-a/42" {
			t.Errorf("%s: labels %v, annotations %v", m.Name, m.Labels, m.Annotations)
		}
	}
	if np != nil {
		t.Error("a NetworkPolicy not asked for")
	}
	_, _, _, np = objects(testInstance(config{networkPolicy: true}))
	if np == nil || np.Spec.PodSelector.MatchLabels[LabelInstance] != pod.Name || np.Spec.Ingress[0].From[0].PodSelector.MatchLabels[LabelClient] != "true" {
		t.Errorf("network policy: %+v", np)
	}
}

// What a run makes is owned by its Pod; removing the Pod is all there is to do. An orphan is a run whose owner is
// gone; another's that runs stays.
func TestCreateAndOrphans(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	in := testInstance(config{networkPolicy: true})
	if _, err := create(ctx, cs, in); err != nil {
		t.Fatal(err)
	}
	s, err := cs.CoreV1().Secrets("pack").Get(ctx, in.name, metav1.GetOptions{})
	if err != nil || len(s.OwnerReferences) != 1 || s.OwnerReferences[0].Kind != "Pod" {
		t.Fatalf("the secret is the Pod's: %+v %v", s, err)
	}
	if np, err := cs.NetworkingV1().NetworkPolicies("pack").Get(ctx, in.name, metav1.GetOptions{}); err != nil || len(np.OwnerReferences) != 1 {
		t.Errorf("the policy is the Pod's: %v", err)
	}

	other := in
	other.name, other.owner = "pack-target-k8s-live", "host-a/7"
	if _, err := create(ctx, cs, other); err != nil {
		t.Fatal(err)
	}
	removeOrphans(ctx, cs, "pack", "inst-1", "k8s", func(owner string) bool { return owner == "host-a/42" })
	pods, _ := cs.CoreV1().Pods("pack").List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 1 || pods.Items[0].Name != "pack-target-k8s-live" {
		t.Errorf("after the orphans: %v", pods.Items)
	}
	if err := removePod(ctx, cs, "pack", "pack-target-k8s-live"); err != nil {
		t.Fatal(err)
	}
	if err := removePod(ctx, cs, "pack", "pack-target-k8s-live"); err != nil {
		t.Errorf("removed twice: %v", err)
	}
}

// waitRunning returns when the Pod runs and says why when it will not: an image not pulled, the target exited, no
// node for it, the time up.
func TestWaitRunning(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		status corev1.PodStatus
		want   string
	}{
		"runs":        {corev1.PodStatus{Phase: corev1.PodRunning}, ""},
		"image":       {corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull access denied"}}}}}, "ImagePullBackOff: pull access denied"},
		"exited":      {corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error"}}}}}, "ended (Failed): exit 2 Error"},
		"unscheduled": {corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available"}}}, "cannot be placed: 0/3 nodes"},
		"time is up":  {corev1.PodStatus{Phase: corev1.PodPending}, "does not run (Pending)"},
	} {
		cs := fake.NewClientset(&corev1.Pod{Name: "p", Namespace: "pack", Status: c.status})
		ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		err := waitRunning(ctx, cs, "pack", "p", 10*time.Millisecond)
		cancel()
		switch {
		case len(c.want) == 0 && err != nil:
			t.Errorf("%s: %v", name, err)
		case len(c.want) > 0 && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The section is the runtime's: its settings read, the machine's container settings refused; how pack reaches the
// Pod follows where it runs.
func TestConfig(t *testing.T) {
	rt, _, err := remote.RuntimeOf(map[string]string{"runtime_type": "k8s", "runtime_namespace": "pack", "runtime_connect": "service",
		"runtime_network_policy": "true", "runtime_env": "VAULT_TOKEN", "hosts": "a"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configOf(rt.Extra)
	if err != nil || cfg.namespace != "pack" || cfg.connect != ConnectService || !cfg.networkPolicy || rt.Env[0] != "VAULT_TOKEN" {
		t.Errorf("%+v %+v %v", rt, cfg, err)
	}
	for _, bad := range []map[string]string{
		{"runtime_type": "k8s", "runtime_mounts": "/a:/b"},
		{"runtime_type": "k8s", "runtime_nope": "x"},
		{"runtime_type": "docker", "runtime_namespace": "pack"},
	} {
		if _, _, err := remote.RuntimeOf(bad); err == nil {
			t.Errorf("%v: no refusal", bad)
		}
	}
	for _, bad := range []map[string]string{{"connect": "ssh"}, {"connect": "port-forward", "address": "1.2.3.4:30353"}, {"network_policy": "yes"}} {
		if _, err := configOf(bad); err == nil {
			t.Errorf("%v: no refusal", bad)
		}
	}

	in := testInstance(config{})
	for _, c := range []struct {
		cfg       config
		inCluster bool
		addr      string
		forward   bool
	}{
		{config{connect: ConnectAuto}, true, "pack-target-k8s-0123456789.pack.svc:7353", false},
		{config{connect: ConnectAuto}, false, "", true},
		{config{connect: ConnectService}, false, "pack-target-k8s-0123456789.pack.svc:7353", false},
		{config{connect: ConnectAuto, address: "10.0.0.5:30353"}, false, "10.0.0.5:30353", false},
		{config{connect: ConnectPortForward}, true, "", true},
	} {
		if addr, fw := how(c.cfg, c.inCluster, in); addr != c.addr || fw != c.forward {
			t.Errorf("%+v in cluster %v: %q %v", c.cfg, c.inCluster, addr, fw)
		}
	}
}

// A Pod that ends while pack waits for its target to answer ends the wait, saying why; a closed plugin's watch ends
// with nothing to say.
func TestWatchEnd(t *testing.T) {
	ctx := context.Background()
	exited := &corev1.Pod{Name: "p", Namespace: "pack", Status: corev1.PodStatus{Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: "bad token"}}}}}}
	if err := watchEnd(ctx, fake.NewClientset(exited), "pack", "p", time.Millisecond); err == nil || !strings.Contains(err.Error(), "exited (1 Error): bad token") {
		t.Errorf("exited: %v", err)
	}
	if err := watchEnd(ctx, fake.NewClientset(), "pack", "p", time.Millisecond); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Errorf("gone: %v", err)
	}
	running := &corev1.Pod{Name: "p", Namespace: "pack", Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := watchEnd(cctx, fake.NewClientset(running), "pack", "p", time.Millisecond); err != nil {
		t.Errorf("closed while it runs: %v", err)
	}
}

// A target's name that cannot name a Pod and a Service is refused before the cluster is asked.
func TestTargetName(t *testing.T) {
	for _, name := range []string{"my_target", "K8s", "9lives", "a-", strings.Repeat("a", 41)} {
		if _, err := start(name, "inst", remote.Runtime{Type: remote.RuntimeK8s, Image: "x"}); err == nil || !strings.Contains(err.Error(), "names a Pod and a Service") {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"k8s", "edge-2", strings.Repeat("a", 40)} {
		if !targetName.MatchString(name) {
			t.Errorf("%q refused", name)
		}
	}
}
