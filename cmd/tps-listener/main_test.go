package main_test

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/cloudfoundry/sonde-go/events"
	"github.com/gogo/protobuf/proto"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
	"github.com/tedsuo/rata"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/tps"
)

var _ = Describe("TPS-Listener", func() {
	var (
		httpClient       *http.Client
		requestGenerator *rata.RequestGenerator

		desiredLRP, desiredLRP2 *models.DesiredLRP
	)

	BeforeEach(func() {
		requestGenerator = rata.NewRequestGenerator(fmt.Sprintf("http://%s", listenerAddr), tps.Routes)
		httpClient = &http.Client{
			Transport: &http.Transport{},
		}
	})

	JustBeforeEach(func() {
		listener = ginkgomon.Invoke(runner)

		desiredLRP = &models.DesiredLRP{
			Domain:      "some-domain",
			ProcessGuid: "some-process-guid",
			Instances:   3,
			RootFs:      "some:rootfs",
			MemoryMb:    1024,
			DiskMb:      512,
			LogGuid:     "some-log-guid",
			Action: models.WrapAction(&models.RunAction{
				User: "me",
				Path: "ls",
			}),
		}

		err := bbsClient.DesireLRP(desiredLRP)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if listener != nil {
			listener.Signal(os.Kill)
			Eventually(listener.Wait()).Should(Receive())
		}
	})

	Describe("GET /v1/actual_lrps/:guid", func() {
		Context("when the receptor is running", func() {
			JustBeforeEach(func() {
				instanceKey0 := models.NewActualLRPInstanceKey("some-instance-guid-0", "cell-id")

				err := bbsClient.ClaimActualLRP("some-process-guid", 0, &instanceKey0)
				Expect(err).NotTo(HaveOccurred())

				lrpKey1 := models.NewActualLRPKey("some-process-guid", 1, "some-domain")
				instanceKey1 := models.NewActualLRPInstanceKey("some-instance-guid-1", "cell-id")
				netInfo := models.NewActualLRPNetInfo("1.2.3.4", models.NewPortMapping(65100, 8080))
				err = bbsClient.StartActualLRP(&lrpKey1, &instanceKey1, &netInfo)
				Expect(err).NotTo(HaveOccurred())
			})

			It("reports the state of the given process guid's instances", func() {
				getLRPs, err := requestGenerator.CreateRequest(
					tps.LRPStatus,
					rata.Params{"guid": "some-process-guid"},
					nil,
				)
				Expect(err).NotTo(HaveOccurred())

				response, err := httpClient.Do(getLRPs)
				Expect(err).NotTo(HaveOccurred())

				var lrpInstances []cc_messages.LRPInstance
				err = json.NewDecoder(response.Body).Decode(&lrpInstances)
				Expect(err).NotTo(HaveOccurred())

				Expect(lrpInstances).To(HaveLen(3))
				for i, _ := range lrpInstances {
					Expect(lrpInstances[i]).NotTo(BeZero())
					lrpInstances[i].Since = 0

					Eventually(lrpInstances[i]).ShouldNot(BeZero())
					lrpInstances[i].Uptime = 0
				}

				Expect(lrpInstances).To(ContainElement(cc_messages.LRPInstance{
					ProcessGuid:  "some-process-guid",
					InstanceGuid: "some-instance-guid-0",
					Index:        0,
					State:        cc_messages.LRPInstanceStateStarting,
				}))

				Expect(lrpInstances).To(ContainElement(cc_messages.LRPInstance{
					ProcessGuid:  "some-process-guid",
					InstanceGuid: "some-instance-guid-1",
					Index:        1,
					State:        cc_messages.LRPInstanceStateRunning,
				}))

				Expect(lrpInstances).To(ContainElement(cc_messages.LRPInstance{
					ProcessGuid:  "some-process-guid",
					InstanceGuid: "",
					Index:        2,
					State:        cc_messages.LRPInstanceStateStarting,
				}))
			})
		})

		Context("when the receptor is not running", func() {
			BeforeEach(func() {
				ginkgomon.Kill(receptorRunner, 5)
			})

			It("returns 500", func() {
				getLRPs, err := requestGenerator.CreateRequest(
					tps.LRPStatus,
					rata.Params{"guid": "some-process-guid"},
					nil,
				)
				Expect(err).NotTo(HaveOccurred())

				response, err := httpClient.Do(getLRPs)
				Expect(err).NotTo(HaveOccurred())

				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})

	Describe("GET /v1/actual_lrps/:guid/stats", func() {
		Context("when the receptor is running", func() {
			var trafficControllerProcess ifrit.Process

			JustBeforeEach(func() {
				instanceKey0 := models.NewActualLRPInstanceKey("some-instance-guid-0", "cell-id")

				err := bbsClient.ClaimActualLRP("some-process-guid", 0, &instanceKey0)
				Expect(err).NotTo(HaveOccurred())

				lrpKey1 := models.NewActualLRPKey("some-process-guid", 1, "some-domain")
				instanceKey1 := models.NewActualLRPInstanceKey("some-instance-guid-1", "cell-id")
				netInfo := models.NewActualLRPNetInfo("1.2.3.4", models.NewPortMapping(65100, 8080))
				err = bbsClient.StartActualLRP(&lrpKey1, &instanceKey1, &netInfo)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when a DesiredLRP is not found", func() {
				It("returns a NotFound", func() {
					getLRPStats, err := requestGenerator.CreateRequest(
						tps.LRPStats,
						rata.Params{"guid": "some-bogus-guid"},
						nil,
					)
					Expect(err).ToNot(HaveOccurred())
					getLRPStats.Header.Add("Authorization", "I can do this.")

					response, err := httpClient.Do(getLRPStats)
					Expect(err).ToNot(HaveOccurred())
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when the traffic controller is running", func() {
				BeforeEach(func() {
					message1 := marshalMessage(createContainerMetric("some-process-guid", 0, 3.0, 1024, 2048, 0))
					message2 := marshalMessage(createContainerMetric("some-process-guid", 1, 4.0, 1024, 2048, 0))
					message3 := marshalMessage(createContainerMetric("some-process-guid", 2, 5.0, 1024, 2048, 0))

					messages := map[string][][]byte{}
					messages["some-log-guid"] = [][]byte{message1, message2, message3}

					handler := NewHttpHandler(messages)
					httpServer := http_server.New(trafficControllerAddress, handler)
					trafficControllerProcess = ifrit.Invoke(sigmon.New(httpServer))
					Expect(trafficControllerProcess.Ready()).To(BeClosed())
				})

				AfterEach(func() {
					ginkgomon.Interrupt(trafficControllerProcess)
				})

				It("reports the state of the given process guid's instances", func() {
					getLRPStats, err := requestGenerator.CreateRequest(
						tps.LRPStats,
						rata.Params{"guid": "some-process-guid"},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())
					getLRPStats.Header.Add("Authorization", "I can do this.")

					response, err := httpClient.Do(getLRPStats)
					Expect(err).NotTo(HaveOccurred())
					Expect(response.StatusCode).To(Equal(http.StatusOK))

					var lrpInstances []cc_messages.LRPInstance
					err = json.NewDecoder(response.Body).Decode(&lrpInstances)
					Expect(err).NotTo(HaveOccurred())

					Expect(lrpInstances).To(HaveLen(3))
					zeroTime := time.Unix(0, 0)
					for i, _ := range lrpInstances {
						Expect(lrpInstances[i].Stats.Time).NotTo(BeZero())
						lrpInstances[i].Stats.Time = zeroTime

						Expect(lrpInstances[i]).NotTo(BeZero())
						lrpInstances[i].Since = 0

						Eventually(lrpInstances[i]).ShouldNot(BeZero())
						lrpInstances[i].Uptime = 0
					}

					Expect(lrpInstances).To(ContainElement(cc_messages.LRPInstance{
						ProcessGuid:  "some-process-guid",
						InstanceGuid: "some-instance-guid-0",
						Index:        0,
						State:        cc_messages.LRPInstanceStateStarting,
						Stats: &cc_messages.LRPInstanceStats{
							Time:          zeroTime,
							CpuPercentage: 0.03,
							MemoryBytes:   1024,
							DiskBytes:     2048,
						},
					}))

					Expect(lrpInstances).To(ContainElement(cc_messages.LRPInstance{
						ProcessGuid:  "some-process-guid",
						InstanceGuid: "some-instance-guid-1",
						Index:        1,
						State:        cc_messages.LRPInstanceStateRunning,
						Host:         "1.2.3.4",
						Port:         65100,
						Stats: &cc_messages.LRPInstanceStats{
							Time:          zeroTime,
							CpuPercentage: 0.04,
							MemoryBytes:   1024,
							DiskBytes:     2048,
						},
					}))

					Expect(lrpInstances).To(ContainElement(cc_messages.LRPInstance{
						ProcessGuid:  "some-process-guid",
						InstanceGuid: "",
						Index:        2,
						State:        cc_messages.LRPInstanceStateStarting,
						Stats: &cc_messages.LRPInstanceStats{
							Time:          zeroTime,
							CpuPercentage: 0.05,
							MemoryBytes:   1024,
							DiskBytes:     2048,
						},
					}))
				})
			})

			Context("when the traffic controller is not running", func() {
				It("reports the status with nil stats", func() {
					getLRPStats, err := requestGenerator.CreateRequest(
						tps.LRPStats,
						rata.Params{"guid": "some-process-guid"},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())
					getLRPStats.Header.Add("Authorization", "I can do this.")

					response, err := httpClient.Do(getLRPStats)
					Expect(err).NotTo(HaveOccurred())
					Expect(response.StatusCode).To(Equal(http.StatusOK))

					var lrpInstances []cc_messages.LRPInstance
					err = json.NewDecoder(response.Body).Decode(&lrpInstances)
					Expect(err).NotTo(HaveOccurred())

					Expect(lrpInstances).To(HaveLen(3))

					for _, instance := range lrpInstances {
						Expect(instance.Stats).To(BeNil())
					}
				})
			})
		})

		Context("when the receptor is not running", func() {
			BeforeEach(func() {
				ginkgomon.Kill(receptorRunner, 5)
			})

			It("returns internal server error", func() {
				getLRPs, err := requestGenerator.CreateRequest(
					tps.LRPStats,
					rata.Params{"guid": "some-process-guid"},
					nil,
				)
				Expect(err).NotTo(HaveOccurred())
				getLRPs.Header.Add("Authorization", "I can do this.")

				response, err := httpClient.Do(getLRPs)
				Expect(err).NotTo(HaveOccurred())

				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})

	Describe("GET /v1/bulk_actual_lrp_status", func() {
		startActualLRP := func(processGuid string) {
			instanceKey0 := models.NewActualLRPInstanceKey("some-instance-guid-0", "cell-id")

			err := bbsClient.ClaimActualLRP(processGuid, 0, &instanceKey0)
			Expect(err).NotTo(HaveOccurred())

			lrpKey1 := models.NewActualLRPKey(processGuid, 1, "some-domain")
			instanceKey1 := models.NewActualLRPInstanceKey("some-instance-guid-1", "cell-id")
			netInfo := models.NewActualLRPNetInfo("1.2.3.4", models.NewPortMapping(65100, 8080))

			err = bbsClient.StartActualLRP(&lrpKey1, &instanceKey1, &netInfo)
			Expect(err).NotTo(HaveOccurred())
		}

		JustBeforeEach(func() {
			desiredLRP2 = &models.DesiredLRP{
				Domain:      "some-domain",
				ProcessGuid: "some-other-process-guid",
				Instances:   3,
				RootFs:      "some:rootfs",
				MemoryMb:    1024,
				DiskMb:      512,
				LogGuid:     "some-other-log-guid",
				Action: models.WrapAction(&models.RunAction{
					User: "me",
					Path: "ls",
				}),
			}

			err := bbsClient.DesireLRP(desiredLRP2)
			Expect(err).NotTo(HaveOccurred())

			startActualLRP(desiredLRP.ProcessGuid)
			startActualLRP(desiredLRP2.ProcessGuid)
		})

		It("reports the status for all the process guids supplied", func() {
			getLRPStatus, err := requestGenerator.CreateRequest(
				tps.BulkLRPStatus,
				nil,
				nil,
			)
			Expect(err).NotTo(HaveOccurred())
			getLRPStatus.Header.Add("Authorization", "I can do this.")

			query := getLRPStatus.URL.Query()
			query.Set("guids", "some-process-guid,some-other-process-guid")
			getLRPStatus.URL.RawQuery = query.Encode()

			response, err := httpClient.Do(getLRPStatus)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			var lrpInstanceStatus map[string][]cc_messages.LRPInstance
			err = json.NewDecoder(response.Body).Decode(&lrpInstanceStatus)
			Expect(err).NotTo(HaveOccurred())

			Expect(lrpInstanceStatus).To(HaveLen(2))
			for guid, instances := range lrpInstanceStatus {
				for i, _ := range instances {
					Expect(instances[i]).NotTo(BeZero())
					instances[i].Since = 0

					Eventually(instances[i]).ShouldNot(BeZero())
					instances[i].Uptime = 0
				}

				Expect(instances).To(ContainElement(cc_messages.LRPInstance{
					ProcessGuid:  guid,
					InstanceGuid: "some-instance-guid-0",
					Index:        0,
					State:        cc_messages.LRPInstanceStateStarting,
				}))

				Expect(instances).To(ContainElement(cc_messages.LRPInstance{
					ProcessGuid:  guid,
					InstanceGuid: "some-instance-guid-1",
					Index:        1,
					State:        cc_messages.LRPInstanceStateRunning,
				}))

				Expect(instances).To(ContainElement(cc_messages.LRPInstance{
					ProcessGuid:  guid,
					InstanceGuid: "",
					Index:        2,
					State:        cc_messages.LRPInstanceStateStarting,
				}))
			}
		})
	})
})

func createContainerMetric(appId string, instanceIndex int32, cpuPercentage float64, memoryBytes uint64, diskByte uint64, timestamp int64) *events.Envelope {
	if timestamp == 0 {
		timestamp = time.Now().UnixNano()
	}

	cm := &events.ContainerMetric{
		ApplicationId: proto.String(appId),
		InstanceIndex: proto.Int32(instanceIndex),
		CpuPercentage: proto.Float64(cpuPercentage),
		MemoryBytes:   proto.Uint64(memoryBytes),
		DiskBytes:     proto.Uint64(diskByte),
	}

	return &events.Envelope{
		ContainerMetric: cm,
		EventType:       events.Envelope_ContainerMetric.Enum(),
		Origin:          proto.String("fake-origin-1"),
		Timestamp:       proto.Int64(timestamp),
	}
}

func marshalMessage(message *events.Envelope) []byte {
	data, err := proto.Marshal(message)
	if err != nil {
		log.Println(err.Error())
	}

	return data
}
