// Package e2e is the operator's end-to-end suite: a kind cluster, a live NetBox, and the
// manager deployed from this repository's Helm chart.
//
// It is not part of `make test`. It needs Docker and it takes minutes, so `make test-e2e`
// runs it and skips with an accurate message when the prerequisites are absent. See
// docs/operations/e2e.md.
package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ricardomolendijk/netbox-operator/test/e2e/harness"
)

// suiteTimeout bounds the whole run. Twenty seeded permutations, each applying the graph and
// then tearing it down again, is minutes of real NetBox round trips.
const suiteTimeout = 90 * time.Minute

// resyncQuiescence is the endpoint resyncPeriod every run but the grant-last one uses.
//
// Short on purpose: the quiescence assertion has to observe two full resync periods, and a
// production-shaped 10m would make that a 20-minute wait per run. Short also makes the
// assertion harder rather than easier -- the drift check runs more often, so a hot loop has
// more chances to show itself.
const resyncQuiescence = 20 * time.Second

var (
	// env is the running environment, shared by every suite in this package.
	env *harness.Harness

	// skipReasons is why the suite is not running, empty when it is.
	skipReasons []string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "netbox-operator e2e")
}

var _ = BeforeSuite(func() {
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	DeferCleanup(cancel)

	cfg, err := harness.DefaultConfig(GinkgoWriter)
	Expect(err).NotTo(HaveOccurred(), "reading the harness configuration")

	skipReasons = harness.Preflight(ctx, cfg)
	if len(skipReasons) > 0 {
		// Reported here as well as in every spec's skip, so the reason is in the log even
		// when the runner summarises the skips away.
		logRun("e2e prerequisites are missing:\n  %s\n", strings.Join(skipReasons, "\n  "))
		return
	}

	env = harness.New(cfg)
	// Registered before Up rather than after: a failure partway through Up leaves a cluster
	// or a NetBox behind, and that is exactly the state the next run must not inherit.
	DeferCleanup(func() {
		By("tearing the environment down")
		Expect(env.Down(context.Background())).To(Succeed())
	})

	By("bringing up kind, NetBox and the operator")
	Expect(env.Up(ctx)).To(Succeed())

	By("seeding a NetBoxEndpoint and its credential in every fixture namespace")
	Expect(env.SeedEndpoints(ctx, resyncQuiescence)).To(Succeed())
})

// requireEnvironment skips the calling spec when the prerequisites are absent.
//
// A skip and never a pass: a suite that reported success because it could not run would be
// worse than one that did not run at all.
func requireEnvironment() {
	if len(skipReasons) == 0 {
		return
	}
	Skip("e2e prerequisites are missing:\n  " + strings.Join(skipReasons, "\n  "))
}
