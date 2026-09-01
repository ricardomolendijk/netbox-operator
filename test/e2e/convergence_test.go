package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/test/e2e/harness"
)

// defaultPermutations is NBO-017's N. Lowered with NBO_E2E_PERMUTATIONS when iterating; a
// maintainer running the gate for real runs it at 20.
const defaultPermutations = 20

// maxJitter is the upper bound on the pause between two applies.
//
// It is load-bearing rather than decorative: without a pause the API server hands the manager
// the whole set in one work-queue drain and the intermediate states this gate exists to test
// may never occur.
const maxJitter = 500 * time.Millisecond

// Each run draws from the seed with its own stream id, so that adding a run does not change
// the orders every other run gets from the same seed -- which is what makes a printed seed
// reproduce a failure rather than a different failure.
const (
	streamRandom    uint64 = 1
	streamGrantLast uint64 = 2
	streamRestart   uint64 = 3
	streamTeardown  uint64 = 4
	streamFixed     uint64 = 5
)

// runResult is what one apply-and-converge pass produced.
type runResult struct {
	Name      string
	Dump      harness.Dump
	Mutations float64
	Duration  time.Duration

	// ReconcileErrors and ErrorLines are the engine-quality numbers, recorded per run and
	// asserted once at the end rather than inside the run.
	//
	// That placement is deliberate. NBO-017 requires both to be zero for a whole passing
	// run, and they are not -- #252 makes the second reconcile of every object lose its
	// status write to a stale-cache 409. Asserting inside each pass would stop the suite at
	// the forward run and leave the twenty permutations, the dump equality, the quiescence
	// and the write economy unexecuted, which is most of the gate. One spec at the end names
	// the defect once and lets the rest of the gate do its job.
	ReconcileErrors float64
	ErrorLines      []string
}

// seed is package-level so that the fixed-stream helper can reach it. Set once, in BeforeAll,
// and printed there.
var seed uint64

// Ordered because the forward run's dump is the baseline every other run is compared against,
// and ContinueOnFailure because a gate that stops at the first red spec answers one question
// where the maintainer needs all of them: "reverse differs *and* the cycle run is fine" is a
// much smaller bug than "reverse differs" alone.
var _ = Describe("Ordering", Ordered, ContinueOnFailure, func() {
	var (
		graph    []harness.Fixture
		cycle    []harness.Fixture
		baseline harness.Dump
		timings  []runResult
	)

	BeforeAll(func() {
		requireEnvironment()

		var err error
		graph, err = harness.LoadFixtures(filepath.Join("fixtures", "graph"))
		Expect(err).NotTo(HaveOccurred(), "loading the convergence graph")
		cycle, err = harness.LoadFixtures(filepath.Join("fixtures", "cycle"))
		Expect(err).NotTo(HaveOccurred(), "loading the cycle fixtures")

		var explicit bool
		seed, explicit, err = harness.SeedFromEnv()
		Expect(err).NotTo(HaveOccurred())

		// Printed unconditionally and early, because a failure in a random permutation is
		// only reproducible if the seed reached the log before the failure did.
		origin := "randomly chosen"
		if explicit {
			origin = "from " + harness.EnvSeed
		}
		AddReportEntry("seed", fmt.Sprintf("%d (%s) -- reproduce with %s=%d",
			seed, origin, harness.EnvSeed, seed))
		logRun(
			"\nPRNG seed %d (%s). Reproduce this run with %s=%d\n\n",
			seed, origin, harness.EnvSeed, seed)

		By("starting from an empty NetBox")
		Expect(resetNetBox(graph)).To(Succeed())
	})

	AfterAll(func() {
		if len(timings) == 0 {
			return
		}
		// Printed as a table so a regression in convergence latency is visible even when the
		// run passes -- a gate that only says pass or fail cannot show a graph getting
		// slower.
		logRun("\nconvergence timings\n")
		for _, result := range timings {
			logRun("  %-34s %6.1fs  %3.0f mutations  %3d objects in NetBox\n",
				result.Name, result.Duration.Seconds(), result.Mutations, result.Dump.Count)
		}
	})

	It("converges when the graph is applied in dependency order", func(ctx SpecContext) {
		result := applyAndConverge(ctx, "forward", graph)
		baseline = result.Dump
		Expect(baseline.Count).To(BeNumerically(">", 0),
			"the forward run wrote nothing to NetBox, so there is no baseline to compare against")
		timings = append(timings, result)
	})

	It("converges when the graph is applied in exactly reverse order", func(ctx SpecContext) {
		Expect(resetNetBox(graph)).To(Succeed())

		result := applyAndConverge(ctx, "reverse", harness.Reverse(graph))
		timings = append(timings, result)
		expectBaseline(baseline, result.Dump, "reverse order produced a different NetBox state")
	})

	It("converges in every seeded random permutation", func(ctx SpecContext) {
		permutations := permutationCount()
		rng := rand.New(rand.NewPCG(seed, streamRandom))

		for i := range permutations {
			order := harness.Permute(graph, rng)
			name := fmt.Sprintf("random-%02d", i+1)
			logRun("%s order: %v\n", name, harness.Order(order))

			Expect(resetNetBox(graph)).To(Succeed())
			result := applyAndConvergeWith(ctx, name, order, rng)
			timings = append(timings, result)
			expectBaseline(baseline, result.Dump,
				"%s produced a different NetBox state (seed %d, %s=%d)",
				name, seed, harness.EnvSeed, seed)
		}
	})

	It("wakes denied referrers when the grant is applied last, with no resync to help", func(ctx SpecContext) {
		Expect(resetNetBox(graph)).To(Succeed())

		// An hour, so that a referrer reaching Ready cannot be the resync's doing. Restored
		// afterwards, because every other run needs a period two of which fit in a test.
		Expect(env.SetResyncPeriod(ctx, time.Hour)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(env.SetResyncPeriod(ctx, resyncQuiescence)).To(Succeed())
		})
		Expect(env.WaitEndpointsReady(ctx)).To(Succeed())

		referrers, grants := harness.SplitGrants(graph)
		rng := rand.New(rand.NewPCG(seed, streamGrantLast))
		Expect(harness.Apply(ctx, env.Cluster.Client, harness.Permute(referrers, rng),
			harness.ApplyOptions{MaxJitter: maxJitter, Rng: rng})).To(Succeed())

		By("asserting the cross-namespace referrers are denied, and have written nothing")
		// The *message* per object, and the *reason* over the set. `RefsResolved`'s reason is
		// one value for the whole object, and a referrer that also waits on a same-namespace
		// ref reports that instead -- the prefix waits on the VLAN as well as on the denied
		// location, so it settles on RefNotReady with both named in the message. Asserting
		// RefDenied on every crossing referrer would therefore fail on a graph that is
		// behaving exactly as designed.
		Eventually(func(g Gomega) {
			states, err := harness.ReadStates(ctx, env.Cluster.Client, crossNamespaceReferrers(graph))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(states).NotTo(BeEmpty(), "no fixture crosses a namespace, so this proves nothing")

			var denied int
			for _, state := range states {
				g.Expect(state.Is(netboxv1alpha1.ConditionRefsResolved,
					metav1.ConditionFalse, "")).To(BeTrue(),
					"expected RefsResolved=False, got %s", state)
				g.Expect(state.Condition(netboxv1alpha1.ConditionRefsResolved).Message).
					To(ContainSubstring("NetBoxRefGrant"),
						"the message does not name the missing grant as the remedy: %s", state)
				g.Expect(state.NetBoxID).To(BeZero(),
					"a denied object wrote to NetBox and holds an id: %s", state)

				if state.Is(netboxv1alpha1.ConditionRefsResolved,
					metav1.ConditionFalse, netboxv1alpha1.ReasonRefDenied) {
					denied++
				}
			}
			g.Expect(denied).To(BeNumerically(">", 0),
				"no crossing referrer reported Reason=RefDenied, so the reason is never exercised")
		}).WithContext(ctx).WithTimeout(env.Cfg.ReadyTimeout).WithPolling(time.Second).Should(Succeed())

		By("applying the grant")
		Expect(harness.Apply(ctx, env.Cluster.Client, grants, harness.ApplyOptions{})).To(Succeed())

		Expect(harness.WaitConverged(ctx, env.Cluster.Client, graph, env.Cfg.ReadyTimeout)).To(Succeed())
		dump, err := harness.DumpNetBox(ctx, env.NetBox.Client)
		Expect(err).NotTo(HaveOccurred())
		expectBaseline(baseline, dump, "the grant-last run produced a different NetBox state")
	})

	It("converges when the manager is restarted partway through a random-order apply", func(ctx SpecContext) {
		Expect(resetNetBox(graph)).To(Succeed())

		rng := rand.New(rand.NewPCG(seed, streamRestart))
		order := harness.Permute(graph, rng)
		killAt := rng.IntN(len(order))
		logRun("restarting the manager after apply %d of %d\n", killAt+1, len(order))

		opts := harness.ApplyOptions{
			MaxJitter: maxJitter,
			Rng:       rng,
			Between: func(ctx context.Context, index int) error {
				if index != killAt {
					return nil
				}
				return env.Operator.Restart(ctx, env.Cfg.ReadyTimeout)
			},
		}
		Expect(harness.Apply(ctx, env.Cluster.Client, order, opts)).To(Succeed())

		// No write-economy assertion here: the counters live in the process that was killed,
		// so the figure would be the replacement's alone and would mean nothing.
		Expect(harness.WaitConverged(ctx, env.Cluster.Client, graph, env.Cfg.ReadyTimeout)).To(Succeed())
		dump, err := harness.DumpNetBox(ctx, env.NetBox.Client)
		Expect(err).NotTo(HaveOccurred())
		expectBaseline(baseline, dump, "the restart run produced a different NetBox state")
	})

	It("reports RefCycle without writing, without storming, and converges once the cycle is broken", func(ctx SpecContext) {
		Expect(resetNetBox(graph)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(harness.DeleteAll(ctx, env.Cluster.Client, cycle, env.Cfg.ReadyTimeout)).To(Succeed())
		})

		// An hour, so that the reconcile count below measures requeue behaviour and nothing
		// else. A cycle is terminal, so what the periodic resync does about it is not what
		// "no requeue storm" is asking -- and at the 20 s the other runs need, three resyncs
		// of two objects is most of the budget before the engine has requeued anything.
		Expect(env.SetResyncPeriod(ctx, time.Hour)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(env.SetResyncPeriod(ctx, resyncQuiescence)).To(Succeed())
		})
		Expect(env.WaitEndpointsReady(ctx)).To(Succeed())

		before, err := env.Operator.Scrape(ctx)
		Expect(err).NotTo(HaveOccurred())

		Expect(harness.Apply(ctx, env.Cluster.Client, cycle, harness.ApplyOptions{})).To(Succeed())

		By("both objects reporting RefCycle and holding no NetBox id")
		Eventually(func(g Gomega) {
			states, err := harness.ReadStates(ctx, env.Cluster.Client, cycle)
			g.Expect(err).NotTo(HaveOccurred())
			for _, state := range states {
				g.Expect(state.Is(netboxv1alpha1.ConditionRefsResolved,
					metav1.ConditionFalse, netboxv1alpha1.ReasonRefCycle)).To(BeTrue(),
					"expected RefsResolved=False/RefCycle, got %s", state)
				g.Expect(state.NetBoxID).To(BeZero(),
					"an object in a cycle wrote to NetBox: %s", state)
			}
		}).WithContext(ctx).WithTimeout(env.Cfg.ReadyTimeout).WithPolling(time.Second).Should(Succeed())

		By("not turning the cycle into a requeue storm")
		// A cycle is terminal, so the correct behaviour is to stop. Sixty seconds is the
		// window NBO-017 fixed and ten reconciles is its "under five each" for two objects:
		// enough for two creates and each object being woken by the other's arrival, and
		// nowhere near a controller that keeps requeueing something it can never resolve.
		time.Sleep(60 * time.Second)
		after, err := env.Operator.Scrape(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Reconciles()-before.Reconciles()).To(BeNumerically("<", 10),
			"two objects in a cycle produced %.0f reconciles in 60s, which is a requeue storm",
			after.Reconciles()-before.Reconciles())
		Expect(after.Mutations()-before.Mutations()).To(BeZero(),
			"a cycle produced NetBox writes: %s", after.MutationBreakdown())

		By("converging both once one spec is fixed")
		fixed := cycle[0].Object.DeepCopy()
		Expect(env.Cluster.Client.Get(ctx, keyOf(fixed), fixed)).To(Succeed())
		Expect(unsetParentRef(fixed)).To(Succeed())
		Expect(env.Cluster.Client.Update(ctx, fixed)).To(Succeed())

		Expect(harness.WaitConverged(ctx, env.Cluster.Client, cycle, env.Cfg.ReadyTimeout)).To(Succeed())
	})

	It("leaves NetBox empty after a random-order teardown, with no stuck finalizer", func(ctx SpecContext) {
		Expect(resetNetBox(graph)).To(Succeed())

		rng := rand.New(rand.NewPCG(seed, streamTeardown))
		Expect(harness.Apply(ctx, env.Cluster.Client, harness.Permute(graph, rng),
			harness.ApplyOptions{MaxJitter: maxJitter, Rng: rng})).To(Succeed())
		Expect(harness.WaitConverged(ctx, env.Cluster.Client, graph, env.Cfg.ReadyTimeout)).To(Succeed())

		By("deleting in random order")
		// No ordering to get right, and NetBox's PROTECT 409s have to resolve themselves as
		// the dependents go away -- which is NBO-007's claim, now with real references
		// behind it.
		Expect(harness.DeleteAll(ctx, env.Cluster.Client, harness.Permute(graph, rng),
			env.Cfg.ReadyTimeout)).To(Succeed())

		empty, detail, err := harness.NetBoxEmpty(ctx, env.NetBox.Client)
		Expect(err).NotTo(HaveOccurred())
		Expect(empty).To(BeTrue(), "netbox is not empty after teardown: %s", detail)
	})

	// Last, and over every run at once. Waiting on a reference is a legitimate intermediate
	// state and reaching Ready through an error path is not, so a converging graph has no
	// business producing either of these -- see #252 for the defect that currently does.
	It("never went through an error path and never logged at error level", func(_ SpecContext) {
		Expect(timings).NotTo(BeEmpty(), "no run recorded its numbers, so this asserts nothing")

		var errors float64
		var lines []string
		for _, result := range timings {
			errors += result.ReconcileErrors
			for _, line := range result.ErrorLines {
				lines = append(lines, result.Name+": "+line)
			}
		}

		Expect(errors).To(BeZero(),
			"netbox_operator_reconcile_total{result=\"error\"} moved by %.0f across %d runs",
			errors, len(timings))
		Expect(lines).To(BeEmpty(), "the manager logged %d error-level lines:\n%s",
			len(lines), strings.Join(lines, "\n"))
	})
})

// expectBaseline compares a run's dump against the forward run's.
//
// It requires the baseline rather than skipping without it: with ContinueOnFailure a later run
// executes even when the forward run failed, and silently comparing against an empty string
// would turn one failure into six.
func expectBaseline(baseline, got harness.Dump, format string, args ...any) {
	GinkgoHelper()

	Expect(baseline.Count).To(BeNumerically(">", 0),
		"there is no baseline to compare against; the forward run must have failed")
	Expect(got.Text).To(Equal(baseline.Text),
		fmt.Sprintf(format, args...)+":\n"+harness.Diff(baseline, got))
}

// applyAndConverge is one full pass whose jitter comes from the seed's fixed stream, so the
// forward and reverse runs are as reproducible as the random ones.
func applyAndConverge(ctx SpecContext, name string, order []harness.Fixture) runResult {
	return applyAndConvergeWith(ctx, name, order, rand.New(rand.NewPCG(seed, streamFixed)))
}

// applyAndConvergeWith applies the order, waits for convergence, and asserts everything
// NBO-017 requires of a run that passes.
func applyAndConvergeWith(ctx SpecContext, name string, order []harness.Fixture, rng *rand.Rand) runResult {
	GinkgoHelper()

	before, err := env.Operator.Scrape(ctx)
	Expect(err).NotTo(HaveOccurred(), "scraping the manager's metrics before %s", name)
	logBefore, err := env.Operator.Logs(ctx)
	Expect(err).NotTo(HaveOccurred(), "reading the manager's log before %s", name)

	started := time.Now()
	Expect(harness.Apply(ctx, env.Cluster.Client, order,
		harness.ApplyOptions{MaxJitter: maxJitter, Rng: rng})).To(Succeed())
	Expect(harness.WaitConverged(ctx, env.Cluster.Client, order, env.Cfg.ReadyTimeout)).
		To(Succeed(), "%s did not converge", name)
	elapsed := time.Since(started)

	after, err := env.Operator.Scrape(ctx)
	Expect(err).NotTo(HaveOccurred(), "scraping the manager's metrics after %s", name)

	mutations := after.Mutations() - before.Mutations()
	Expect(mutations).To(BeNumerically("<=", float64(writeBudget(order))),
		"%s cost %.0f mutating requests for %d objects (%s); convergence that expensive is churn",
		name, mutations, objectCount(order), after.MutationBreakdown())
	By("asserting two resync periods produce no NetBox mutation at all")
	quietWrites, err := env.Operator.WaitQuiet(ctx, 2*resyncQuiescence)
	Expect(err).NotTo(HaveOccurred())
	Expect(quietWrites).To(BeZero(),
		"%s kept writing after convergence: %.0f mutating requests in %s",
		name, quietWrites, 2*resyncQuiescence)

	logAfter, err := env.Operator.Logs(ctx)
	Expect(err).NotTo(HaveOccurred())

	dump, err := harness.DumpNetBox(ctx, env.NetBox.Client)
	Expect(err).NotTo(HaveOccurred())

	return runResult{
		Name:            name,
		Dump:            dump,
		Mutations:       mutations,
		Duration:        elapsed,
		ReconcileErrors: after.ReconcileErrors() - before.ReconcileErrors(),
		ErrorLines:      harness.ErrorLogLines(harness.LogSince(logBefore, logAfter)),
	}
}

// resetNetBox deletes the graph's CRs and waits for NetBox to be empty, so the next run
// creates rather than adopts.
//
// Without it the second run would adopt the first run's objects, produce an identical dump
// for the wrong reason, and prove nothing about ordering.
func resetNetBox(graph []harness.Fixture) error {
	ctx := context.Background()
	if err := harness.DeleteAll(ctx, env.Cluster.Client, graph, env.Cfg.ReadyTimeout); err != nil {
		return err
	}
	return harness.WaitFor(ctx, "netbox to be empty", env.Cfg.ReadyTimeout,
		func(ctx context.Context) (bool, string, error) {
			return harness.NetBoxEmpty(ctx, env.NetBox.Client)
		})
}

// writeBudget is NBO-017's write-economy bound: one create per object, plus one follow-up
// PATCH per field the engine may have deferred.
//
// The bound is what makes convergence mean something. An end-state check passes a controller
// that got there by brute force, and forty PATCHes for seventeen objects is churn however
// correct the result.
func writeBudget(order []harness.Fixture) int {
	return objectCount(order) + harness.DeferredWrites(order)
}

func objectCount(order []harness.Fixture) int {
	var count int
	for _, fixture := range order {
		if fixture.Object.GetKind() == "NetBoxRefGrant" {
			continue
		}
		count++
	}
	return count
}

// crossNamespaceReferrers are the fixtures whose references reach into another namespace, and
// therefore the ones the grant governs.
func crossNamespaceReferrers(graph []harness.Fixture) []harness.Fixture {
	var out []harness.Fixture
	for _, fixture := range graph {
		if harness.CrossesNamespace(fixture) {
			out = append(out, fixture)
		}
	}
	return out
}

func permutationCount() int {
	raw := os.Getenv(harness.EnvPermutations)
	if raw == "" {
		return defaultPermutations
	}
	value, err := strconv.Atoi(raw)
	Expect(err).NotTo(HaveOccurred(), "%s=%q is not a number", harness.EnvPermutations, raw)
	Expect(value).To(BeNumerically(">", 0))
	return value
}

// logRun writes one line of the run's transcript. The suite's output is read as a narrative
// of what was applied in what order, which is what makes a failure diagnosable from the log.
func logRun(format string, args ...any) {
	_, _ = fmt.Fprintf(GinkgoWriter, format, args...)
}

// keyOf is the object's namespaced name, for a Get.
func keyOf(obj *unstructured.Unstructured) client.ObjectKey {
	return client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()}
}

// unsetParentRef removes spec.parentRef, which is how the cycle run breaks the cycle: one of
// the two regions becomes top-level, and both then resolve.
func unsetParentRef(obj *unstructured.Unstructured) error {
	unstructured.RemoveNestedField(obj.Object, "spec", "parentRef")
	_, found, err := unstructured.NestedMap(obj.Object, "spec", "parentRef")
	if err != nil || found {
		return fmt.Errorf("spec.parentRef survived removal on %s/%s",
			obj.GetNamespace(), obj.GetName())
	}
	return nil
}
