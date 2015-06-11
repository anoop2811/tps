package main_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudfoundry-incubator/consuladapter"
	receptorrunner "github.com/cloudfoundry-incubator/receptor/cmd/receptor/testrunner"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/lrp_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/services_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/cb"
	"github.com/cloudfoundry-incubator/tps/cmd/tpsrunner"
	"github.com/cloudfoundry/storeadapter"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager/lagertest"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"

	"testing"
)

var (
	receptorPath string
	receptorPort int

	trafficControllerAddress string
	trafficControllerPort    int
	trafficControllerURL     string

	etcdPort int

	consulRunner *consuladapter.ClusterRunner

	listenerPort int
	listenerAddr string
	listener     ifrit.Process
	runner       *ginkgomon.Runner

	listenerPath string

	fakeCC         *ghttp.Server
	etcdRunner     *etcdstorerunner.ETCDClusterRunner
	receptorRunner ifrit.Process
	store          storeadapter.StoreAdapter
	lrpBBS         *lrp_bbs.LRPBBS
	logger         *lagertest.TestLogger
)

const assetsPath = "../../../../cloudfoundry/storeadapter/assets/"

func TestTPS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TPS-Listener Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	tps, err := gexec.Build("github.com/cloudfoundry-incubator/tps/cmd/tps-listener", "-race")
	Expect(err).NotTo(HaveOccurred())

	receptor, err := gexec.Build("github.com/cloudfoundry-incubator/receptor/cmd/receptor", "-race")
	Expect(err).NotTo(HaveOccurred())

	payload, err := json.Marshal(map[string]string{
		"listener": tps,
		"receptor": receptor,
	})
	Expect(err).NotTo(HaveOccurred())

	return payload
}, func(payload []byte) {
	binaries := map[string]string{}

	err := json.Unmarshal(payload, &binaries)
	Expect(err).NotTo(HaveOccurred())

	etcdPort = 5001 + GinkgoParallelNode()
	receptorPort = 6001 + GinkgoParallelNode()*2
	listenerPort = 1518 + GinkgoParallelNode()

	trafficControllerPort = 7001 + GinkgoParallelNode()*2
	trafficControllerAddress = fmt.Sprintf("127.0.0.1:%d", trafficControllerPort)
	trafficControllerURL = fmt.Sprintf("ws://%s", trafficControllerAddress)

	etcdRunner = etcdstorerunner.NewETCDClusterRunner(etcdPort, 1,
		&etcdstorerunner.SSLConfig{
			CertFile: assetsPath + "server.crt",
			KeyFile:  assetsPath + "server.key",
			CAFile:   assetsPath + "ca.crt",
		})

	listenerPath = string(binaries["listener"])
	receptorPath = string(binaries["receptor"])
	store = etcdRunner.Adapter(
		&etcdstorerunner.SSLConfig{
			CertFile: assetsPath + "client.crt",
			KeyFile:  assetsPath + "client.key",
			CAFile:   assetsPath + "ca.crt",
		})

	consulRunner = consuladapter.NewClusterRunner(
		9001+config.GinkgoConfig.ParallelNode*consuladapter.PortOffsetLength,
		1,
		"http",
	)

	logger = lagertest.NewTestLogger("test")
})

var _ = BeforeEach(func() {
	etcdRunner.Start()
	consulRunner.Start()
	consulRunner.WaitUntilReady()

	taskHandlerAddress := fmt.Sprintf("127.0.0.1:%d", receptorPort+1)
	clock := clock.NewClock()
	lrpBBS = lrp_bbs.New(
		store,
		clock,
		cb.NewCellClient(),
		cb.NewAuctioneerClient(),
		services_bbs.New(consulRunner.NewSession("a-session"), clock, logger.Session("services-bbs")),
	)

	receptor := receptorrunner.New(receptorPath, receptorrunner.Args{
		Address:            fmt.Sprintf("127.0.0.1:%d", receptorPort),
		TaskHandlerAddress: taskHandlerAddress,
		EtcdCluster:        strings.Join(etcdRunner.NodeURLS(), ","),
		ConsulCluster:      consulRunner.ConsulCluster(),
		ClientCert:         assetsPath + "client.crt",
		ClientKey:          assetsPath + "client.key",
		CACert:             assetsPath + "ca.crt",
	})
	receptorRunner = ginkgomon.Invoke(receptor)

	fakeCC = ghttp.NewServer()

	listenerAddr = fmt.Sprintf("127.0.0.1:%d", uint16(listenerPort))

	runner = tpsrunner.NewListener(
		string(listenerPath),
		listenerAddr,
		fmt.Sprintf("http://127.0.0.1:%d", receptorPort),
		trafficControllerURL,
	)
})

var _ = AfterEach(func() {
	fakeCC.Close()
	ginkgomon.Kill(receptorRunner, 5)
	etcdRunner.Stop()
	consulRunner.Stop()
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	gexec.CleanupBuildArtifacts()
})
