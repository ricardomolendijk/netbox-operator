package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// OperatorNamespace is where the manager is installed. Fixed rather than configurable: the
// chart's leader-election Role is namespaced to the release namespace and nothing in the
// suite benefits from moving it.
const OperatorNamespace = "netbox-operator-system"

// ReleaseName is the Helm release name, and therefore the prefix of every object the chart
// renders.
const ReleaseName = "netbox-operator"

// metricsNodePort is the NodePort the harness puts in front of the manager's metrics port,
// and the port test/e2e/kind/cluster.yaml publishes to the host. The two have to agree.
//
// A published NodePort rather than a port-forward: it is one applied Service and one line of
// cluster config, against a port-forward's goroutine, its lifecycle and its reconnection
// logic -- and the assertion that reads it runs dozens of times per suite.
const metricsNodePort = 30081

// Operator is the deployed manager.
type Operator struct {
	// Namespace and Release identify the Helm installation.
	Namespace string
	Release   string

	// MetricsURL is the manager's /metrics, reachable from the test process.
	MetricsURL string

	cluster *Cluster
	typed   *kubernetes.Clientset
}

// DeployOperator builds this checkout into an image, loads it into the cluster, installs
// the CRDs and installs the chart.
//
// The chart and not a hand-written Deployment. It is the artefact users install, it already
// exposes the two values the suite needs to set -- credentialNamespaces and plain-HTTP
// metrics -- and a second copy of the manager's Deployment in test/ would drift from it.
func DeployOperator(ctx context.Context, cfg Config, cluster *Cluster) (*Operator, error) {
	if err := buildAndLoadImage(ctx, cfg); err != nil {
		return nil, err
	}
	if err := installCRDs(ctx, cfg, cluster); err != nil {
		return nil, err
	}

	op := &Operator{
		Namespace:  OperatorNamespace,
		Release:    ReleaseName,
		MetricsURL: fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsNodePort),
		cluster:    cluster,
	}

	typed, err := kubernetes.NewForConfig(cluster.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("building a typed clientset: %w", err)
	}
	op.typed = typed

	if err := op.install(ctx, cfg); err != nil {
		return nil, err
	}
	if err := op.exposeMetrics(ctx); err != nil {
		return nil, err
	}
	if err := op.WaitAvailable(ctx, cfg.ReadyTimeout); err != nil {
		return nil, err
	}
	return op, nil
}

func buildAndLoadImage(ctx context.Context, cfg Config) error {
	if !cfg.SkipImageBuild {
		logf(cfg.Out, "building %s from %s", cfg.OperatorImage, cfg.RootDir)
		build := command{
			out:  cfg.Out,
			dir:  cfg.RootDir,
			name: "docker",
			args: []string{"build", "-t", cfg.OperatorImage, "."},
		}
		if _, err := build.run(ctx); err != nil {
			return fmt.Errorf("building the operator image: %w", err)
		}
	}

	kind, err := toolPath(cfg.RootDir, "kind")
	if err != nil {
		return err
	}
	// Always loaded, even when the build was skipped: the image may be in the daemon and
	// not in the cluster, and `kind load` on an image the node already has is cheap.
	if _, err := run(ctx, cfg.Out, kind, "load", "docker-image",
		cfg.OperatorImage, "--name", cfg.ClusterName); err != nil {
		return fmt.Errorf("loading %s into cluster %s: %w", cfg.OperatorImage, cfg.ClusterName, err)
	}
	return nil
}

// installCRDs server-side applies the chart's crds/ directory.
//
// Server-side, for the reason `make upgrade-crds` gives: a CRD of this size exceeds the
// last-applied-configuration annotation a client-side apply stores in it. Applied through
// the Go client rather than kubectl, so the suite needs one fewer external binary.
func installCRDs(ctx context.Context, cfg Config, cluster *Cluster) error {
	dir := filepath.Join(cfg.RootDir, "charts", "netbox-operator", "crds")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading the chart's crds directory %s: %w", dir, err)
	}

	logf(cfg.Out, "applying %d CRDs from %s", len(entries), dir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		if err := applyYAMLFile(ctx, cluster.Client, filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func applyYAMLFile(ctx context.Context, c client.Client, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(body, obj); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := c.Patch(ctx, obj, client.Apply,
		client.ForceOwnership, client.FieldOwner("nbo-e2e")); err != nil {
		return fmt.Errorf("applying %s: %w", path, err)
	}
	return nil
}

// install runs `helm upgrade --install`, so a retained cluster is upgraded in place.
func (op *Operator) install(ctx context.Context, cfg Config) error {
	helm, err := toolPath(cfg.RootDir, "helm")
	if err != nil {
		return err
	}
	if err := op.clearFailedRelease(ctx, cfg, helm); err != nil {
		return err
	}
	chart := filepath.Join(cfg.RootDir, "charts", "netbox-operator")

	repository, tag := splitImageRef(cfg.OperatorImage)
	args := []string{
		"upgrade", "--install", op.Release, chart,
		"--namespace", op.Namespace, "--create-namespace",
		"--kubeconfig", op.cluster.Kubeconfig,
		// No --wait. Helm's readiness wait reports "timed out waiting for the condition" and
		// nothing else, while WaitAvailable below names the Pod, its phase and the reason its
		// container is not running -- which is the difference between diagnosing a
		// CrashLoopBackOff from the log and re-running the suite to look at the cluster.
		"--set", "image.repository=" + repository,
		"--set", "image.tag=" + tag,
		// Never pull: the image was side-loaded by `kind load` and does not exist in any
		// registry, so IfNotPresent would still try on a node that has it under a
		// different digest.
		"--set", "image.pullPolicy=Never",
		// One replica and no leader election, so a restart is one pod going away and
		// coming back rather than a lease handover the suite would have to wait out.
		"--set", "leaderElection=false",
		// Plain HTTP on a known port: the harness scrapes /metrics as an assertion, and a
		// self-signed HTTPS listener would mean teaching the test to skip verification for
		// no gain inside a throwaway cluster.
		"--set", "metrics.secure=false",
		"--set", "metrics.port=8080",
		// The namespaces holding endpoint token Secrets. Without them the manager builds no
		// informer for those namespaces and every endpoint reports SecretMissing.
		"--set", "credentialNamespaces={" + strings.Join(FixtureNamespaces, ",") + "}",
		// No webhook override. This kind cluster has no cert-manager, so the chart skips the
		// whole webhook and starts the manager with --enable-webhooks=false itself (#249) --
		// which means the suite exercises the same degraded path a default install on a
		// cluster without cert-manager gets, rather than a flag only e2e passes. The rules
		// the webhook would enforce are asserted at reconcile time by the convergence specs,
		// which is where their authority lives anyway
		// (docs/operations/admission-webhooks.md#what-breaks-when-it-is-off).
	}
	if _, err := run(ctx, cfg.Out, helm, args...); err != nil {
		return fmt.Errorf("installing the chart into %s: %w", op.Namespace, err)
	}
	return nil
}

// clearFailedRelease uninstalls a release that never reached `deployed`.
//
// `helm upgrade --install` cannot repair one: an install that failed leaves the release in
// `failed` or `pending-install`, and the next upgrade refuses with "has no deployed releases".
// That is precisely the state a retained cluster is in after the run that failed, so without
// this the fix-and-rerun loop needs a manual `helm uninstall` in the middle of it.
func (op *Operator) clearFailedRelease(ctx context.Context, cfg Config, helm string) error {
	status, installed := op.releaseStatus(ctx, cfg, helm)
	if !installed || strings.Contains(status, "STATUS: deployed") {
		return nil
	}

	logf(cfg.Out, "uninstalling a release that is not deployed before reinstalling")
	if _, err := run(ctx, cfg.Out, helm, "uninstall", op.Release,
		"--namespace", op.Namespace, "--kubeconfig", op.cluster.Kubeconfig, "--wait"); err != nil {
		return fmt.Errorf("uninstalling the undeployed release %s: %w", op.Release, err)
	}
	return nil
}

// releaseStatus returns `helm status`'s output, and false when there is no such release. A
// bool rather than an error, because "not installed" is the ordinary case on a fresh cluster
// and not a failure anything upstream should classify.
func (op *Operator) releaseStatus(ctx context.Context, cfg Config, helm string) (string, bool) {
	out, err := run(ctx, cfg.Out, helm, "status", op.Release,
		"--namespace", op.Namespace, "--kubeconfig", op.cluster.Kubeconfig)
	if err != nil {
		return "", false
	}
	return out, true
}

// exposeMetrics adds a NodePort Service in front of the manager's metrics port.
func (op *Operator) exposeMetrics(ctx context.Context) error {
	name := types.NamespacedName{Namespace: op.Namespace, Name: "netbox-operator-e2e-metrics"}

	// Get before Create, and not Create-and-ignore-AlreadyExists. A second Create of this
	// Service fails validation on the *nodePort* -- "provided port is already allocated",
	// held by the Service that is already there -- before the API server ever gets to the
	// name collision, so the AlreadyExists that would make Create idempotent never arrives.
	// Found by the second run against a retained cluster.
	existing := &corev1.Service{}
	if err := op.cluster.Client.Get(ctx, name, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("looking for the metrics service %s: %w", name, err)
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: name.Namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: op.podSelector(),
			Ports: []corev1.ServicePort{{
				Name:     "metrics",
				Port:     8080,
				NodePort: metricsNodePort,
			}},
		},
	}
	if err := op.cluster.Client.Create(ctx, service); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("exposing the manager's metrics on nodePort %d: %w", metricsNodePort, err)
	}
	return nil
}

// podSelector is the chart's own selector for the manager Pod. Read off the Deployment
// would be better still, but the Deployment does not exist yet when the Service is created
// on a fresh cluster, and these two labels are the chart's contract.
func (op *Operator) podSelector() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "netbox-operator",
		"app.kubernetes.io/instance": op.Release,
	}
}

// WaitAvailable waits for the manager Deployment to report an available replica.
func (op *Operator) WaitAvailable(ctx context.Context, timeout time.Duration) error {
	key := types.NamespacedName{Namespace: op.Namespace, Name: op.Release}
	return WaitFor(ctx, "the manager deployment to become available", timeout,
		func(ctx context.Context) (bool, string, error) {
			deployment := &appsv1.Deployment{}
			if err := op.cluster.Client.Get(ctx, key, deployment); err != nil {
				return false, fmt.Sprintf("getting %s: %v", key, err), nil
			}
			if deployment.Status.AvailableReplicas > 0 {
				return true, "", nil
			}
			return false, fmt.Sprintf("%s has %d/%d available replicas; %s",
				key, deployment.Status.AvailableReplicas, deployment.Status.Replicas,
				op.podDetail(ctx)), nil
		})
}

// podDetail is why the manager's Pods are not running, in one line. Read from container
// statuses rather than the Pod phase: a CrashLoopBackOff is `phase: Running` with a waiting
// container, so the phase alone says nothing.
func (op *Operator) podDetail(ctx context.Context) string {
	pods, err := op.Pods(ctx)
	if err != nil {
		return "could not list the manager's pods: " + err.Error()
	}
	if len(pods) == 0 {
		return "no manager pod exists yet"
	}

	details := make([]string, 0, len(pods))
	for i := range pods {
		details = append(details, fmt.Sprintf("%s is %s%s",
			pods[i].Name, pods[i].Status.Phase, containerDetail(pods[i].Status.ContainerStatuses)))
	}
	return strings.Join(details, "; ")
}

func containerDetail(statuses []corev1.ContainerStatus) string {
	parts := make([]string, 0, len(statuses))
	for i := range statuses {
		waiting := statuses[i].State.Waiting
		if waiting == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf(" (%s waiting: %s %q)",
			statuses[i].Name, waiting.Reason, waiting.Message))
	}
	return strings.Join(parts, "")
}

// Pods lists the manager's Pods.
func (op *Operator) Pods(ctx context.Context) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := op.cluster.Client.List(ctx, pods,
		client.InNamespace(op.Namespace), client.MatchingLabels(op.podSelector())); err != nil {
		return nil, fmt.Errorf("listing the manager's pods: %w", err)
	}
	return pods.Items, nil
}

// Logs returns the manager's log, concatenated across its Pods.
//
// Used as an assertion -- NBO-017 requires that no passing run logged at error level -- so
// it reads the whole log rather than a tail.
func (op *Operator) Logs(ctx context.Context) (string, error) {
	pods, err := op.Pods(ctx)
	if err != nil {
		return "", err
	}

	var combined strings.Builder
	for i := range pods {
		body, err := op.typed.CoreV1().
			Pods(op.Namespace).
			GetLogs(pods[i].Name, &corev1.PodLogOptions{}).
			DoRaw(ctx)
		if err != nil {
			return "", fmt.Errorf("reading logs from pod %s: %w", pods[i].Name, err)
		}
		combined.Write(body)
	}
	return combined.String(), nil
}

// ErrorLogLines returns the manager's error-level log lines.
//
// The manager runs with zap's production encoder, so a line's level is a JSON field and not a
// word in a sentence -- which is what makes this a reliable assertion rather than a grep that
// also matches an object *named* error.
//
// NBO-017 requires it to be empty for a passing run: every waiting state in this suite is a
// legitimate intermediate state, and any of them arriving through an error path is a bug the
// gate exists to catch.
func ErrorLogLines(log string) []string {
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"level":"error"`) {
			lines = append(lines, line)
		}
	}
	return lines
}

// LogSince returns the part of after that was not already in before.
//
// A pod's log is cumulative and this suite runs twenty-odd passes against one manager, so
// asserting on the whole log would make one transient error in the first run fail every run
// after it -- and the failure would name the wrong pass. Falls back to the whole log when the
// prefix does not match, which is what a restarted pod looks like.
func LogSince(before, after string) string {
	if strings.HasPrefix(after, before) {
		return after[len(before):]
	}
	return after
}

// Restart deletes the manager's Pods and waits for the replacement to be available.
//
// The NBO-017 assertion it exists for: reconciliation is level-triggered, so no in-memory
// state may be load-bearing and a restart mid-apply must be a non-event.
func (op *Operator) Restart(ctx context.Context, timeout time.Duration) error {
	pods, err := op.Pods(ctx)
	if err != nil {
		return err
	}
	for i := range pods {
		if err := op.cluster.Client.Delete(ctx, &pods[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting manager pod %s: %w", pods[i].Name, err)
		}
	}

	// The old Pod is Terminating and still Ready for a moment, so waiting on the
	// Deployment alone can pass before the restart has happened at all.
	if err := op.waitPodsReplaced(ctx, pods, timeout); err != nil {
		return err
	}
	return op.WaitAvailable(ctx, timeout)
}

func (op *Operator) waitPodsReplaced(ctx context.Context, old []corev1.Pod, timeout time.Duration) error {
	gone := make(map[string]bool, len(old))
	for i := range old {
		gone[old[i].Name] = true
	}

	return WaitFor(ctx, "the old manager pods to go away", timeout,
		func(ctx context.Context) (bool, string, error) {
			pods, err := op.Pods(ctx)
			if err != nil {
				return transient(err)
			}
			for i := range pods {
				if gone[pods[i].Name] {
					return false, "pod " + pods[i].Name + " is still there", nil
				}
			}
			return len(pods) > 0, "no replacement pod yet", nil
		})
}

func splitImageRef(ref string) (repository, tag string) {
	index := strings.LastIndex(ref, ":")
	if index < 0 || strings.Contains(ref[index:], "/") {
		return ref, "latest"
	}
	return ref[:index], ref[index+1:]
}

func isAlreadyExists(err error) bool { return apierrors.IsAlreadyExists(err) }
