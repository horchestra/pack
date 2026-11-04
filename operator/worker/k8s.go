package worker

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// K8s is the Kubernetes runtime (К6): the job is a batch/v1 Job of one Pod — backoffLimit 0, restartPolicy
// Never, the environment a Secret, checkout and home emptyDir, the labels pack-operator.job/.request on the
// Job and the Pod. An init container (alpine/git) clones the repository at the sha; the pack container runs
// the argv. A cancel deletes the Pod with a grace period (SIGTERM). A kubeconfig here is the worker's own —
// never the user's ~/.kube/config unless the deployer says it.
type K8s struct {
	Kubeconfig string // the kubeconfig path; empty — in-cluster (service account)
	Namespace  string // default "default"
	Image      string // the pack image (A7)
	GitImage   string // the init container's; "alpine/git:latest" by default
	Rollback   string // terminationGracePeriodSeconds, seconds as a number; "660" by default
	Timeout    int    // activeDeadlineSeconds — the command's timeout; 0 is none
	HostIP     string // an IP the repository's host answers at inside the cluster (hostAliases); empty is none
	RunAsUser  int64  // the pod's user/group; 0 keeps the image's (a pack image runs as uid 1000)
	// Log takes the runtime's own lines (the pod's waits and the stream's errors); nil is silence.
	Log func(line string)

	cl *k8sClient
}

var _ Runtime = &K8s{}

// k8sClient is the small REST client of the runtime.
type k8sClient struct {
	base   string // https://host:port
	token  string
	client *http.Client
}

func (k *K8s) ns() string {
	if len(k.Namespace) > 0 {
		return k.Namespace
	}
	return "default"
}

func (k *K8s) gitImage() string {
	if len(k.GitImage) > 0 {
		return k.GitImage
	}
	return "alpine/git:latest"
}

func (k *K8s) grace() int {
	if len(k.Rollback) > 0 {
		var n int
		_, _ = fmt.Sscan(k.Rollback, &n)
		if n > 0 {
			return n
		}
	}
	return 660
}

// connect loads the kubeconfig (or the in-cluster account) once.
func (k *K8s) connect() (*k8sClient, error) {
	if k.cl != nil {
		return k.cl, nil
	}
	if len(k.Kubeconfig) == 0 {
		host := os.Getenv("KUBERNETES_SERVICE_HOST")
		port := os.Getenv("KUBERNETES_SERVICE_PORT")
		token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
		if err != nil || len(host) == 0 {
			return nil, fmt.Errorf("no kubeconfig and not in a cluster: %w", err)
		}
		ca, _ := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
		cl, err := newK8sClient("https://"+host+":"+port, strings.TrimSpace(string(token)), ca)
		if err != nil {
			return nil, err
		}
		k.cl = cl
		return cl, nil
	}
	b, err := os.ReadFile(k.Kubeconfig)
	if err != nil {
		return nil, err
	}
	var kc struct {
		Current  string `json:"current-context"`
		Clusters []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server string `json:"server"`
				CAData string `json:"certificate-authority-data"`
			} `json:"cluster"`
		} `json:"clusters"`
		Contexts []struct {
			Name    string `json:"name"`
			Context struct {
				Cluster string `json:"cluster"`
				User    string `json:"user"`
			} `json:"context"`
		} `json:"contexts"`
		Users []struct {
			Name string `json:"name"`
			User struct {
				Token    string `json:"token"`
				CertData string `json:"client-certificate-data"`
				KeyData  string `json:"client-key-data"`
			} `json:"user"`
		} `json:"users"`
	}
	if err := yaml.Unmarshal(b, &kc); err != nil {
		return nil, fmt.Errorf("%s: %w", k.Kubeconfig, err)
	}
	var server, caData, token, certData, keyData string
	var cname, uname string
	for _, c := range kc.Contexts {
		if c.Name == kc.Current {
			cname, uname = c.Context.Cluster, c.Context.User
		}
	}
	for _, c := range kc.Clusters {
		if c.Name == cname {
			server, caData = c.Cluster.Server, c.Cluster.CAData
		}
	}
	for _, u := range kc.Users {
		if u.Name == uname {
			token, certData, keyData = u.User.Token, u.User.CertData, u.User.KeyData
		}
	}
	if len(server) == 0 {
		return nil, fmt.Errorf("%s: no cluster of the current context", k.Kubeconfig)
	}
	var ca []byte
	if len(caData) > 0 {
		if ca, err = base64.StdEncoding.DecodeString(caData); err != nil {
			return nil, err
		}
	}
	cl, err := newK8sClient(server, token, ca)
	if err != nil {
		return nil, err
	}
	if len(certData) > 0 {
		cert, _ := base64.StdEncoding.DecodeString(certData)
		key, _ := base64.StdEncoding.DecodeString(keyData)
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, fmt.Errorf("%s: the client certificate: %w", k.Kubeconfig, err)
		}
		cl.client.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{pair}
	}
	k.cl = cl
	return cl, nil
}

func newK8sClient(base, token string, ca []byte) (*k8sClient, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if len(ca) > 0 {
		pool.AppendCertsFromPEM(ca)
	}
	return &k8sClient{base: strings.TrimSuffix(base, "/"), token: token,
		client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}}, nil
}

// do is one API call; body and out are the JSON documents.
func (c *k8sClient) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	if len(c.token) > 0 {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.StatusCode, nil
}

// stream opens a follow of a pod's log.
func (c *k8sClient) stream(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if len(c.token) > 0 {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	noTimeout := *c.client
	noTimeout.Timeout = 0
	resp, err := noTimeout.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", path, resp.Status)
	}
	return resp.Body, nil
}

// Run creates the Secret, the Job, follows the Pod's log, and ends with the container's exit code.
func (k *K8s) Run(ctx context.Context, run *Run) (int, error) {
	cl, err := k.connect()
	if err != nil {
		return 1, err
	}
	ns := k.ns()
	name := "pack-operator-job-" + strings.TrimPrefix(run.Job.ID, "j-")
	if len(name) > 60 {
		name = name[:60]
	}
	// the environment as a Secret (К6)
	secretData := map[string]string{}
	for _, e := range run.Env {
		key, v, _ := strings.Cut(e, "=")
		secretData[key] = v
	}
	secret := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": ns,
			"labels": map[string]string{labelJob: run.Job.ID, labelRequest: run.Job.Request}},
		"stringData": secretData,
	}
	if code, err := cl.do(ctx, "POST", "/api/v1/namespaces/"+ns+"/secrets", secret, nil); err != nil {
		return 1, fmt.Errorf("the secret: %w", err)
	} else if code >= 300 {
		return 1, fmt.Errorf("the secret: %d", code)
	}
	// the repository's clone is the init container's work; HTTPS credentials go as a git header (a token in
	// the project's credentials; SSH is not a k8s runtime's way — the worker runs those jobs native or docker)
	clone := fmt.Sprintf("git -c safe.directory='*' clone --no-checkout %s /checkout && git -c safe.directory='*' -C /checkout checkout %s && rm -rf /checkout/.git",
		shellQuote(run.RepoURL), run.Request.SHA)
	job := map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": ns,
			"labels": map[string]string{labelJob: run.Job.ID, labelRequest: run.Job.Request}},
		"spec": map[string]any{
			"backoffLimit":            0,
			"ttlSecondsAfterFinished": 600,
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{labelJob: run.Job.ID, labelRequest: run.Job.Request}},
				"spec": map[string]any{
					"restartPolicy":                 "Never",
					"terminationGracePeriodSeconds": k.grace(),
					"initContainers": []any{map[string]any{
						"name":    "checkout",
						"image":   k.gitImage(),
						"command": []string{"sh", "-c", clone}, // the git image's entrypoint is git itself
						"volumeMounts": []any{
							map[string]any{"name": "checkout", "mountPath": "/checkout"},
						},
					}},
					"containers": []any{map[string]any{
						"name":       "pack",
						"image":      k.Image,
						"args":       run.Job.Argv[1:],
						"envFrom":    []any{map[string]any{"secretRef": map[string]any{"name": name}}},
						"env":        []any{map[string]any{"name": "HOME", "value": "/home"}, map[string]any{"name": "PACK_CACHE", "value": "/cache"}},
						"workingDir": "/checkout",
						"volumeMounts": []any{
							map[string]any{"name": "checkout", "mountPath": "/checkout"},
							map[string]any{"name": "home", "mountPath": "/home"},
							map[string]any{"name": "cache", "mountPath": "/cache"},
						},
					}},
					"volumes": []any{
						map[string]any{"name": "checkout", "emptyDir": map[string]any{}},
						map[string]any{"name": "home", "emptyDir": map[string]any{}},
						map[string]any{"name": "cache", "emptyDir": map[string]any{}},
					},
				},
			},
		},
	}
	if k.Timeout > 0 { // activeDeadlineSeconds — the command's timeout (К6)
		job["spec"].(map[string]any)["activeDeadlineSeconds"] = k.Timeout
	}
	podSpec := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if len(k.HostIP) > 0 { // the repository's host, named for the pod (a cluster without a host's DNS)
		podSpec["hostAliases"] = []any{map[string]any{"ip": k.HostIP, "hostnames": []string{"host.docker.internal"}}}
	}
	if k.RunAsUser > 0 { // the emptyDir volumes are the user's to write (a pack image's uid is 1000)
		podSpec["securityContext"] = map[string]any{"runAsUser": k.RunAsUser, "runAsGroup": k.RunAsUser,
			"fsGroup": k.RunAsUser, "fsGroupChangePolicy": "OnRootMismatch"}
	}
	if _, err := cl.do(ctx, "POST", "/apis/batch/v1/namespaces/"+ns+"/jobs", job, nil); err != nil {
		return 1, fmt.Errorf("the job: %w", err)
	}
	// a cancel is the Pod's deletion with the grace period (SIGTERM) (К6)
	go func() {
		<-ctx.Done()
		del := map[string]any{"gracePeriodSeconds": k.grace()}
		_, _ = cl.do(context.Background(), "DELETE",
			fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s%%3D%s&gracePeriodSeconds=%d", ns, labelJob, run.Job.ID, del["gracePeriodSeconds"]), nil, nil)
	}()
	code, err := k.follow(ctx, cl, ns, run, 0)
	// the Secret and the Job go with the job (the Job itself has a ttl)
	_, _ = cl.do(context.Background(), "DELETE", "/api/v1/namespaces/"+ns+"/secrets/"+name, nil, nil)
	return code, err
}

// follow reads the pod's log from seqFrom events on, to the container's end; the exit code is the answer.
func (k *K8s) follow(ctx context.Context, cl *k8sClient, ns string, run *Run, skip int64) (int, error) {
	// wait for the pod
	var pod string
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var pods struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if _, err := cl.do(ctx, "GET",
			fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s%%3D%s", ns, labelJob, run.Job.ID), nil, &pods); err != nil {
			return 1, err
		}
		if len(pods.Items) > 0 {
			pod = pods.Items[0].Metadata.Name
			break
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if len(pod) == 0 {
		return 1, fmt.Errorf("no pod of job %s", run.Job.ID)
	}
	// the log, followed; a ContainerCreating pod has none yet — retry until it speaks (an image pull of a
	// fresh node may be minutes)
	var rd io.ReadCloser
	deadline = time.Now().Add(15 * time.Minute)
	for {
		var err error
		rd, err = cl.stream(ctx, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?container=pack&follow=true", ns, pod))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return 1, fmt.Errorf("%w; the pod: %s", err, k.podStatus(ctx, cl, ns, pod))
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	defer rd.Close()
	var seen int64
	events := bufio.NewScanner(rd)
	events.Buffer(make([]byte, 1024*1024), 1024*1024)
	for events.Scan() {
		line := strings.TrimSpace(events.Text())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		seen++
		if seen > skip {
			run.Event(json.RawMessage(line))
		}
	}
	// the exit code is the terminated container's
	var podDoc struct {
		Status struct {
			ContainerStatuses []struct {
				State struct {
					Terminated *struct {
						ExitCode int `json:"exitCode"`
					} `json:"terminated"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}
	deadline = time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if _, err := cl.do(ctx, "GET", "/api/v1/namespaces/"+ns+"/pods/"+pod, nil, &podDoc); err == nil &&
			len(podDoc.Status.ContainerStatuses) > 0 && podDoc.Status.ContainerStatuses[0].State.Terminated != nil {
			return podDoc.Status.ContainerStatuses[0].State.Terminated.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return 1, fmt.Errorf("the pod %s never terminated", pod)
}

// podStatus is the pod's phase and its containers' states in one line (a failed init speaks here, not in logs).
func (k *K8s) podStatus(ctx context.Context, cl *k8sClient, ns, pod string) string {
	var doc struct {
		Status struct {
			Phase                 string `json:"phase"`
			InitContainerStatuses []struct {
				Name  string `json:"name"`
				State struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
					Terminated *struct {
						Reason   string `json:"reason"`
						ExitCode int    `json:"exitCode"`
					} `json:"terminated"`
				} `json:"state"`
			} `json:"initContainerStatuses"`
		} `json:"status"`
	}
	if _, err := cl.do(ctx, "GET", "/api/v1/namespaces/"+ns+"/pods/"+pod, nil, &doc); err != nil {
		return err.Error()
	}
	out := doc.Status.Phase
	for _, c := range doc.Status.InitContainerStatuses {
		out += "; " + c.Name
		if c.State.Waiting != nil {
			out += " waiting: " + c.State.Waiting.Reason
		}
		if c.State.Terminated != nil {
			out += fmt.Sprintf(" terminated: %s (%d)", c.State.Terminated.Reason, c.State.Terminated.ExitCode)
		}
	}
	return out
}

// Reattach finds the Job's Pod by the labels and reads its log on from lastSeq (К6); no Job — ErrLost.
func (k *K8s) Reattach(ctx context.Context, run *Run, lastSeq int64) (int, error) {
	cl, err := k.connect()
	if err != nil {
		return 0, err
	}
	ns := k.ns()
	var jobs struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := cl.do(ctx, "GET",
		fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs?labelSelector=%s%%3D%s", ns, labelJob, run.Job.ID), nil, &jobs); err != nil {
		return 0, err
	}
	if len(jobs.Items) == 0 {
		return 0, ErrLost
	}
	go func() { // the same cancel as Run's
		<-ctx.Done()
		_, _ = cl.do(context.Background(), "DELETE",
			fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s%%3D%s&gracePeriodSeconds=%d", ns, labelJob, run.Job.ID, k.grace()), nil, nil)
	}()
	return k.follow(ctx, cl, ns, run, lastSeq)
}

// shellQuote quotes one argument for the init container's sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
