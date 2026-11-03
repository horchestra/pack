// Package kube is the k8s runtime of targets: a target's image in a Pod of a cluster, behind a Service, spoken to
// over gRPC through the Service — its address in the cluster when pack runs in one, a port-forward of client-go when
// it does not, or an address the operator gives (a NodePort, a LoadBalancer). The Pod lives as long as the command:
// Close takes it away, and what a pack that ended without closing left is taken away by the next one.
//
//	runtime:
//	  type: k8s
//	  kubeconfig: ~/.kube/config   # KUBECONFIG, ~/.kube/config — or the cluster's own, when pack runs in a Pod
//	  context: prod                # the kubeconfig's current one when none
//	  namespace: pack              # the context's, else default
//	  service_account: pack-target # what the target acts as in the cluster (the k8s target needs its rights)
//	  connect: auto                # auto | service | port-forward
//	  address: 10.0.0.5:30353      # the Service as pack reaches it from outside (NodePort, LoadBalancer)
//	  network_policy: true         # only Pods labelled io.horchestra.pack.client=true reach the target
//	  image_pull_secret: regcred   # the Secret the image is pulled with
//	  env:                         # variables passed in, by name: their values go in a Secret, never the Pod
//	    - VAULT_TOKEN
//
// Only this package links client-go: pack imports it for its runtime, nothing else of the core does.
package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/horchestra/pack/pkg/target/remote"
)

// The settings of the runtime, besides type and env.
var settingNames = []string{"kubeconfig", "context", "namespace", "service_account", "connect", "address", "network_policy", "image_pull_secret"}

// How pack reaches the target's Pod.
const (
	ConnectAuto        = "auto"         // the Service in the cluster when pack runs in one, else a port-forward
	ConnectService     = "service"      // the Service: its name in the cluster, or address
	ConnectPortForward = "port-forward" // a port-forward of client-go to the Pod
)

// The labels of what the runtime makes, besides the installation's and the target's (remote.Label*).
const (
	LabelInstance = "io.horchestra.pack.instance" // the Pod, its Service and Secret: the run of the target they are
	LabelClient   = "io.horchestra.pack.client"   // a Pod that may reach a target under network_policy: "true"
)

func init() {
	remote.RegisterRuntime(remote.RuntimeK8s, remote.Starter{Settings: settingNames, Start: start})
}

// serviceAccountNamespace is where a Pod reads its namespace.
const serviceAccountNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// afterRunning is how long a target whose Pod runs is given to answer: the image is pulled, the program starts.
const afterRunning = 30 * time.Second

// targetName is what a target's name may be to name a Pod and a Service (DNS-1035, pack-target-<name>-<10 hex>
// within 63).
var targetName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$`)

// config is the runtime section.
type config struct {
	kubeconfig, context, namespace string
	serviceAccount, pullSecret     string
	connect, address               string
	networkPolicy                  bool
}

func configOf(extra map[string]string) (config, error) {
	c := config{
		kubeconfig: extra["kubeconfig"], context: extra["context"], namespace: extra["namespace"],
		serviceAccount: extra["service_account"], pullSecret: extra["image_pull_secret"],
		connect: extra["connect"], address: extra["address"],
	}
	if len(c.connect) == 0 {
		c.connect = ConnectAuto
	}
	switch c.connect {
	case ConnectAuto, ConnectService, ConnectPortForward:
	default:
		return c, fmt.Errorf("runtime.connect: %q is not auto, service or port-forward", c.connect)
	}
	if len(c.address) > 0 && c.connect == ConnectPortForward {
		return c, errors.New("runtime.address is the Service's: connect: port-forward reaches the Pod itself")
	}
	if v, ok := extra["network_policy"]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("runtime.network_policy: %q is not true or false", v)
		}
		c.networkPolicy = b
	}
	return c, nil
}

// restConfig is how the cluster is reached: the kubeconfig and context the section names, else the cluster's own
// when pack runs in a Pod, else KUBECONFIG and ~/.kube/config. inCluster: pack runs in the cluster.
func restConfig(c config) (rc *rest.Config, namespace string, inCluster bool, err error) {
	if len(c.kubeconfig) == 0 && len(c.context) == 0 {
		if rc, err := rest.InClusterConfig(); err == nil {
			ns := c.namespace
			if len(ns) == 0 {
				b, err := os.ReadFile(serviceAccountNamespace)
				if ns = strings.TrimSpace(string(b)); err != nil || len(ns) == 0 {
					// a guess would put the Pod where the operator never said, likely with no right to
					return nil, "", true, fmt.Errorf("runtime.namespace: pack runs in a Pod and cannot tell its namespace (%s: %v): say it", serviceAccountNamespace, err)
				}
			}
			return rc, ns, true, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(c.kubeconfig) > 0 {
		path := c.kubeconfig
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, rest)
		}
		rules.ExplicitPath = path
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: c.context})
	if rc, err = cc.ClientConfig(); err != nil {
		return nil, "", false, fmt.Errorf("the cluster: %w", err)
	}
	namespace = c.namespace
	if len(namespace) == 0 {
		namespace, _, _ = cc.Namespace()
	}
	return rc, namespace, false, nil
}

// instance is one run of a target's image.
type instance struct {
	name                 string // the Pod's, its Service's, its Secret's
	target, installation string
	image, token, owner  string
	namespace            string
	env                  map[string]string // the values of runtime.env
	cfg                  config
}

func (in instance) labels() map[string]string {
	return map[string]string{remote.LabelInstallation: in.installation, remote.LabelTarget: in.target, LabelInstance: in.name}
}

func (in instance) meta() metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: in.name, Namespace: in.namespace, Labels: in.labels(),
		Annotations: map[string]string{remote.LabelOwner: in.owner}}
}

// objects are what a run of the target is in the cluster: the Pod, the Secret it reads its token and runtime.env
// from — never written into the Pod — the Service before it and, under network_policy, what keeps others out.
func objects(in instance) (*corev1.Pod, *corev1.Secret, *corev1.Service, *networkingv1.NetworkPolicy) {
	port, _ := strconv.Atoi(remote.ImagePort)
	data := map[string][]byte{remote.EnvToken: []byte(in.token)}
	for k, v := range in.env {
		data[k] = []byte(v)
	}
	secret := &corev1.Secret{ObjectMeta: in.meta(), Type: corev1.SecretTypeOpaque, Data: data}
	pod := &corev1.Pod{ObjectMeta: in.meta(), Spec: corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: in.cfg.serviceAccount,
		Containers: []corev1.Container{{
			Name:    "target",
			Image:   in.image,
			Env:     []corev1.EnvVar{{Name: remote.EnvAddress, Value: "tcp://0.0.0.0:" + remote.ImagePort}},
			EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: in.name}}}},
			Ports:   []corev1.ContainerPort{{Name: "grpc", ContainerPort: int32(port)}},
			// the target ends when its stdin closes: a Pod has one open as long as it lives
			Stdin: true,
		}},
	}}
	if len(in.cfg.pullSecret) > 0 {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: in.cfg.pullSecret}}
	}
	svc := &corev1.Service{ObjectMeta: in.meta(), Spec: corev1.ServiceSpec{
		Selector: map[string]string{LabelInstance: in.name},
		Ports:    []corev1.ServicePort{{Name: "grpc", Port: int32(port), TargetPort: intstr.FromInt32(int32(port))}},
	}}
	var np *networkingv1.NetworkPolicy
	if in.cfg.networkPolicy {
		tcp := corev1.ProtocolTCP
		p := intstr.FromInt32(int32(port))
		np = &networkingv1.NetworkPolicy{ObjectMeta: in.meta(), Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelInstance: in.name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{LabelClient: "true"}}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &p}},
			}},
		}}
	}
	return pod, secret, svc, np
}

// create makes the objects: the Pod first, the rest owned by it, so that taking the Pod away takes them along.
func create(ctx context.Context, cs kubernetes.Interface, in instance) (*corev1.Pod, error) {
	pod, secret, svc, np := objects(in)
	made, err := cs.CoreV1().Pods(in.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("the Pod %s/%s: %w", in.namespace, in.name, err)
	}
	owner := []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: made.Name, UID: made.UID}}
	secret.OwnerReferences, svc.OwnerReferences = owner, owner
	if _, err := cs.CoreV1().Secrets(in.namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return made, fmt.Errorf("the Secret %s/%s: %w", in.namespace, in.name, err)
	}
	if _, err := cs.CoreV1().Services(in.namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return made, fmt.Errorf("the Service %s/%s: %w", in.namespace, in.name, err)
	}
	if np != nil {
		np.OwnerReferences = owner
		if _, err := cs.NetworkingV1().NetworkPolicies(in.namespace).Create(ctx, np, metav1.CreateOptions{}); err != nil {
			return made, fmt.Errorf("the NetworkPolicy %s/%s: %w", in.namespace, in.name, err)
		}
	}
	return made, nil
}

// removePod takes a run of the target away: the Pod, and what it owns with it.
func removePod(ctx context.Context, cs kubernetes.Interface, namespace, name string) error {
	bg := metav1.DeletePropagationBackground
	err := cs.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// removeOrphans takes away the runs of the target for the installation that a pack of this machine left behind when
// it ended without closing them: their owner is gone.
func removeOrphans(ctx context.Context, cs kubernetes.Interface, namespace, installation, target string, orphaned func(string) bool) {
	sel := remote.LabelInstallation + "=" + installation + "," + remote.LabelTarget + "=" + target
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return // the run says what is wrong with the cluster
	}
	for _, p := range pods.Items {
		if orphaned(p.Annotations[remote.LabelOwner]) {
			_ = removePod(ctx, cs, namespace, p.Name)
		}
	}
}

// watchEnd waits for the Pod to end — its phase Failed or Succeeded, its container terminated, the Pod gone — and says
// why; nil when ctx ends first (the plugin is closed).
func watchEnd(ctx context.Context, cs kubernetes.Interface, namespace, name string, poll time.Duration) error {
	for {
		p, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case ctx.Err() != nil:
			return nil
		case apierrors.IsNotFound(err):
			return fmt.Errorf("the Pod %s is gone", name)
		case err == nil && (p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded):
			return fmt.Errorf("the Pod %s ended (%s): %s", name, p.Status.Phase, why(p))
		case err == nil:
			for _, st := range p.Status.ContainerStatuses {
				if t := st.State.Terminated; t != nil {
					return fmt.Errorf("the Pod %s: the target exited (%d %s): %s", name, t.ExitCode, t.Reason, t.Message)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

// waitRunning waits for the Pod's container to run, or says why it does not: an image that cannot be pulled, a
// container that ended, a Pod that cannot be placed.
func waitRunning(ctx context.Context, cs kubernetes.Interface, namespace, name string, poll time.Duration) error {
	for {
		p, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch p.Status.Phase {
		case corev1.PodRunning:
			return nil
		case corev1.PodFailed, corev1.PodSucceeded:
			return fmt.Errorf("the Pod %s ended (%s): %s", name, p.Status.Phase, why(p))
		}
		for _, st := range p.Status.ContainerStatuses {
			if w := st.State.Waiting; w != nil {
				switch w.Reason {
				case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CrashLoopBackOff":
					return fmt.Errorf("the Pod %s: %s: %s", name, w.Reason, w.Message)
				}
			}
			if t := st.State.Terminated; t != nil {
				return fmt.Errorf("the Pod %s: the target exited (%d %s): %s", name, t.ExitCode, t.Reason, t.Message)
			}
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
				return fmt.Errorf("the Pod %s cannot be placed: %s", name, c.Message)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the Pod %s does not run (%s): %w", name, why(p), ctx.Err())
		case <-time.After(poll):
		}
	}
}

func why(p *corev1.Pod) string {
	var parts []string
	if len(p.Status.Reason) > 0 {
		parts = append(parts, p.Status.Reason+" "+p.Status.Message)
	}
	for _, st := range p.Status.ContainerStatuses {
		if w := st.State.Waiting; w != nil {
			parts = append(parts, w.Reason+" "+w.Message)
		}
		if t := st.State.Terminated; t != nil {
			parts = append(parts, fmt.Sprintf("exit %d %s %s", t.ExitCode, t.Reason, t.Message))
		}
	}
	if len(parts) == 0 {
		return string(p.Status.Phase)
	}
	return strings.TrimSpace(strings.Join(parts, "; "))
}

// how is the way pack reaches the Pod: by the Service (its address) or by a port-forward.
func how(c config, inCluster bool, in instance) (service string, forward bool) {
	if len(c.address) > 0 {
		return c.address, false
	}
	switch c.connect {
	case ConnectService:
		return in.name + "." + in.namespace + ".svc:" + remote.ImagePort, false
	case ConnectPortForward:
		return "", true
	}
	if inCluster {
		return in.name + "." + in.namespace + ".svc:" + remote.ImagePort, false
	}
	return "", true
}

// forward opens a port-forward of the machine's loopback to the Pod's port: its address, and how to close it.
func forward(rc *rest.Config, cs kubernetes.Interface, namespace, pod string) (string, func(), error) {
	url := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	tr, upgrader, err := spdy.RoundTripperFor(rc)
	if err != nil {
		return "", nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: tr}, http.MethodPost, url)
	// websockets first, as kubectl: SPDY where the cluster speaks nothing else
	if ws, err := portforward.NewSPDYOverWebsocketDialer(url, rc); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool { return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err) })
	}
	stop, ready := make(chan struct{}), make(chan struct{})
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:" + remote.ImagePort}, stop, ready, nil, os.Stderr)
	if err != nil {
		return "", nil, err
	}
	failed := make(chan error, 1)
	go func() { failed <- fw.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-failed:
		return "", nil, fmt.Errorf("port-forward to %s/%s: %w", namespace, pod, err)
	case <-time.After(30 * time.Second):
		close(stop)
		return "", nil, fmt.Errorf("port-forward to %s/%s: not ready in 30s", namespace, pod)
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stop)
		return "", nil, fmt.Errorf("port-forward to %s/%s: no local port: %v", namespace, pod, err)
	}
	once := false
	return "127.0.0.1:" + strconv.Itoa(int(ports[0].Local)), func() {
		if !once {
			once = true
			close(stop)
		}
	}, nil
}

// start runs the target's image in a Pod and shakes hands with it.
func start(name, installation string, rt remote.Runtime) (*remote.Plugin, error) {
	if !targetName.MatchString(name) {
		return nil, fmt.Errorf("target %q: runtime k8s names a Pod and a Service after it: lower case letters, digits and dashes, a letter first, at most 40", name)
	}
	cfg, err := configOf(rt.Extra)
	if err != nil {
		return nil, err
	}
	rc, namespace, inCluster, err := restConfig(cfg)
	if err != nil {
		return nil, err
	}
	if len(namespace) == 0 {
		namespace = "default"
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, n := range rt.Env {
		v, ok := os.LookupEnv(n)
		if !ok {
			return nil, fmt.Errorf("runtime.env: %s is not set", n)
		}
		env[n] = v
	}
	token, err := remote.NewToken()
	if err != nil {
		return nil, err
	}
	in := instance{name: remote.Prefix + name + "-" + token[:10], target: name, installation: installation,
		image: rt.Image, token: token, owner: remote.Owner(), namespace: namespace, env: env, cfg: cfg}

	ctx, cancel := context.WithTimeout(context.Background(), remote.ImageStart)
	defer cancel()
	removeOrphans(ctx, cs, namespace, installation, name, remote.Orphaned)
	remove := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = removePod(ctx, cs, namespace, in.name)
	}
	if _, err := create(ctx, cs, in); err != nil {
		remove()
		return nil, fmt.Errorf("target %s: %w", name, err)
	}
	if err := waitRunning(ctx, cs, namespace, in.name, time.Second); err != nil {
		remove()
		return nil, fmt.Errorf("target %s: %w", name, err)
	}
	// the Pod runs: one that ends before the target answers ends the wait for it
	watch, unwatch := context.WithCancel(context.Background())
	exited := make(chan error, 1)
	go func() { exited <- watchEnd(watch, cs, namespace, in.name, 2*time.Second) }()
	gone := func() { unwatch(); remove() }
	addr, isForward := how(cfg, inCluster, in)
	stop := gone
	if isForward {
		local, closeForward, err := forward(rc, cs, namespace, in.name)
		if err != nil {
			gone()
			return nil, fmt.Errorf("target %s: %w", name, err)
		}
		addr, stop = local, func() { closeForward(); gone() }
	}
	return remote.Connect(name, rt.Image+" in "+namespace+"/"+in.name, addr, token, afterRunning, exited, stop)
}
