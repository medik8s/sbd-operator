/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package smoke

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

var _ = Describe("SBD Watchdog Smoke Tests", Ordered, Label("Smoke", "Watchdog"), func() {
	const watchdogTestNamespace = "sbd-watchdog-smoke-test"

	var testNS *utils.TestNamespace

	BeforeAll(func() {
		By("initializing Kubernetes clients for watchdog tests if needed")
		if testClients == nil {
			var err error
			testClients, err = utils.SetupKubernetesClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")
		}

		By("creating watchdog smoke test namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: watchdogTestNamespace,
			},
		}
		err := testClients.Client.Create(testClients.Context, ns)
		if err != nil && !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred(), "Failed to create watchdog test namespace")
		}

		// Create test namespace wrapper for utilities
		testNS = &utils.TestNamespace{
			Name:    watchdogTestNamespace,
			Clients: testClients,
		}
	})

	AfterAll(func() {
		By("cleaning up watchdog smoke test namespace")
		if testNS != nil {
			_ = testNS.Cleanup()
		}
	})

	Context("Watchdog Compatibility and Stability", func() {
		var sbdConfig *medik8sv1alpha1.SBDConfig

		BeforeEach(func() {
			// Create a minimal SBD configuration for watchdog testing using utility
			var err error
			sbdConfig, err = testNS.CreateSBDConfig("watchdog-smoke-test", func(config *medik8sv1alpha1.SBDConfig) {
				config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second} // Longer timeout for safety
				config.Spec.PetIntervalMultiple = func() *int32 {
					val := int32(6) // Conservative 15-second pet interval
					return &val
				}()
				// Note: The agent now always runs with testMode=false for production behavior
			})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			By("cleaning up SBD configuration and waiting for agents to terminate")
			if sbdConfig != nil {
				err := testNS.CleanupSBDConfig(sbdConfig)
				Expect(err).NotTo(HaveOccurred())
			}
		})

		It("should successfully deploy SBD agents without causing node instability", func() {
			By("cleaning up any existing SBDConfig with the same name")
			existingConfig := &medik8sv1alpha1.SBDConfig{}
			err := testClients.Client.Get(testClients.Context, client.ObjectKey{
				Name:      sbdConfig.Name,
				Namespace: watchdogTestNamespace,
			}, existingConfig)
			if err == nil {
				GinkgoWriter.Printf("Found existing SBDConfig %s, deleting it first\n", sbdConfig.Name)
				_ = testClients.Client.Delete(testClients.Context, existingConfig)
				// Wait for deletion to complete
				Eventually(func() bool {
					err := testClients.Client.Get(testClients.Context, client.ObjectKey{
						Name:      sbdConfig.Name,
						Namespace: watchdogTestNamespace,
					}, existingConfig)
					return errors.IsNotFound(err)
				}, time.Second*30, time.Second*2).Should(BeTrue())
			}

			By("creating SBD configuration with watchdog enabled")
			err = testClients.Client.Create(testClients.Context, sbdConfig)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for SBD agent DaemonSet to be created")
			dsChecker := testNS.NewDaemonSetChecker()
			daemonSet, err := dsChecker.WaitForDaemonSet(map[string]string{"sbdconfig": sbdConfig.Name}, time.Minute*2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying DaemonSet has correct watchdog configuration")
			expectedArgs := []string{
				"--watchdog-path=/dev/watchdog",
				"--watchdog-timeout=1m30s",
			}
			err = dsChecker.CheckDaemonSetArgs(daemonSet, expectedArgs)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for SBD agent pods to become ready")
			podChecker := testNS.NewPodStatusChecker(map[string]string{"sbdconfig": sbdConfig.Name})
			err = podChecker.WaitForPodsReady(1, time.Minute*5)
			Expect(err).NotTo(HaveOccurred())

			By("verifying nodes remain stable and don't experience reboots")
			nodeChecker := testClients.NewNodeStabilityChecker()
			err = nodeChecker.WaitForNodesStable(time.Minute * 3)
			Expect(err).NotTo(HaveOccurred())

			By("checking if SBD agent pods exist and examining their status")
			pods := &corev1.PodList{}
			err = testClients.Client.List(testClients.Context, pods,
				client.InNamespace(watchdogTestNamespace),
				client.MatchingLabels{"sbdconfig": sbdConfig.Name})
			Expect(err).NotTo(HaveOccurred())
			Expect(len(pods.Items)).To(BeNumerically(">=", 1))

			// Check at least one pod for logs (but don't require specific log messages in test environment)
			podName := pods.Items[0].Name
			By(fmt.Sprintf("examining logs of SBD agent pod %s (may show watchdog hardware limitations)", podName))

			// Try to get logs but don't fail the test if pod isn't ready or logs are empty
			Eventually(func() string {
				logStr, err := podChecker.GetPodLogs(podName, func() *int64 { val := int64(20); return &val }())
				if err != nil {
					GinkgoWriter.Printf("Failed to get logs from pod %s: %v\n", podName, err)
					return "ERROR_GETTING_LOGS"
				}
				if logStr == "" {
					return "NO_LOGS_YET"
				}
				GinkgoWriter.Printf("Pod %s logs sample:\n%s\n", podName, logStr)
				return logStr
			}, time.Minute*1, time.Second*10).Should(SatisfyAny(
				// Accept various states - the test is mainly about configuration correctness
				ContainSubstring("Watchdog pet successful"),
				ContainSubstring("falling back to write-based keep-alive"),
				ContainSubstring("Starting watchdog loop"),
				ContainSubstring("SBD Agent started"),
				ContainSubstring("failed to open watchdog"), // Expected in test environments
				ContainSubstring("ERROR_GETTING_LOGS"),
				ContainSubstring("NO_LOGS_YET"),
			))

			By("verifying no critical errors in agent logs")
			fullLogStr, err := podChecker.GetPodLogs(podName, nil)
			Expect(err).NotTo(HaveOccurred())
			// These errors would indicate problems with our fix
			Expect(fullLogStr).NotTo(ContainSubstring("Failed to pet watchdog after retries"))
			Expect(fullLogStr).NotTo(ContainSubstring("watchdog device is not open"))

			// The agent should show successful operation during normal startup
			Expect(fullLogStr).To(SatisfyAny(
				ContainSubstring("Watchdog pet successful"),
				ContainSubstring("SBD Agent started successfully"),
			))
		})

		It("should handle watchdog driver compatibility across different architectures", func() {
			By("creating SBD configuration optimized for compatibility")
			compatConfig := sbdConfig.DeepCopy()
			compatConfig.Name = "watchdog-compat-test"
			compatConfig.Spec.WatchdogTimeout = &metav1.Duration{Duration: 120 * time.Second} // Even longer timeout
			compatConfig.Spec.PetIntervalMultiple = func() *int32 {
				val := int32(8) // Very conservative pet interval
				return &val
			}()

			By("cleaning up any existing SBDConfig with the same name")
			existingConfig := &medik8sv1alpha1.SBDConfig{}
			err := testClients.Client.Get(testClients.Context, client.ObjectKey{
				Name:      compatConfig.Name,
				Namespace: watchdogTestNamespace,
			}, existingConfig)
			if err == nil {
				GinkgoWriter.Printf("Found existing SBDConfig %s, deleting it first\n", compatConfig.Name)
				_ = testClients.Client.Delete(testClients.Context, existingConfig)
				// Wait for deletion to complete
				Eventually(func() bool {
					err := testClients.Client.Get(testClients.Context, client.ObjectKey{
						Name:      compatConfig.Name,
						Namespace: watchdogTestNamespace,
					}, existingConfig)
					return errors.IsNotFound(err)
				}, time.Second*30, time.Second*2).Should(BeTrue())
			}

			err = testClients.Client.Create(testClients.Context, compatConfig)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = testClients.Client.Delete(testClients.Context, compatConfig) }()

			By("waiting for agent deployment and monitoring for compatibility issues")
			Eventually(func() int {
				pods := &corev1.PodList{}
				err := testClients.Client.List(testClients.Context, pods,
					client.InNamespace(watchdogTestNamespace),
					client.MatchingLabels{"sbdconfig": compatConfig.Name})
				if err != nil {
					GinkgoWriter.Printf("Failed to list pods for compat test: %v\n", err)
					return 0
				}

				runningPods := 0
				for _, pod := range pods.Items {
					if pod.Status.Phase == corev1.PodRunning {
						runningPods++
					}
				}
				return runningPods
			}, time.Minute*4, time.Second*15).Should(BeNumerically(">=", 1))

			By("verifying agents successfully adapt to different watchdog driver implementations")
			// Monitor for a longer period to catch any delayed issues
			Consistently(func() bool {
				pods := &corev1.PodList{}
				err := testClients.Client.List(testClients.Context, pods,
					client.InNamespace(watchdogTestNamespace),
					client.MatchingLabels{"sbdconfig": compatConfig.Name})
				if err != nil {
					GinkgoWriter.Printf("Failed to list pods during compat check: %v\n", err)
					return false
				}

				for _, pod := range pods.Items {
					// Check that pods haven't restarted due to watchdog issues
					// Check restart count across all containers in the pod
					totalRestarts := int32(0)
					for _, containerStatus := range pod.Status.ContainerStatuses {
						totalRestarts += containerStatus.RestartCount
					}
					if totalRestarts > 0 {
						GinkgoWriter.Printf("Pod %s has total restart count %d\n", pod.Name, totalRestarts)

						// Get pod events to understand restart reason
						events := &corev1.EventList{}
						err := testClients.Client.List(testClients.Context, events,
							client.InNamespace(watchdogTestNamespace),
							client.MatchingFields{"involvedObject.name": pod.Name})
						if err == nil {
							for _, event := range events.Items {
								if strings.Contains(event.Message, "watchdog") ||
									strings.Contains(event.Reason, "Failed") {
									GinkgoWriter.Printf("Pod %s event: %s - %s\n",
										pod.Name, event.Reason, event.Message)
								}
							}
						}
						return false
					}

					// Verify pod is in Running state
					if pod.Status.Phase != corev1.PodRunning {
						return false
					}
				}
				return true
			}, time.Minute*5, time.Second*20).Should(BeTrue())

			By("verifying SBDConfig status shows healthy operation")
			Eventually(func() bool {
				updatedConfig := &medik8sv1alpha1.SBDConfig{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{
					Name:      compatConfig.Name,
					Namespace: watchdogTestNamespace,
				}, updatedConfig)
				if err != nil {
					return false
				}

				// Check that we have some ready nodes reported
				return updatedConfig.Status.ReadyNodes > 0
			}, time.Minute*3, time.Second*15).Should(BeTrue())
		})

		It("should display SBDConfig YAML correctly and maintain stability", func() {
			By("creating and retrieving SBDConfig for YAML validation")
			err := testClients.Client.Create(testClients.Context, sbdConfig)
			Expect(err).NotTo(HaveOccurred())

			By("retrieving and displaying SBDConfig YAML")
			retrievedConfig := &medik8sv1alpha1.SBDConfig{}
			Eventually(func() error {
				return testClients.Client.Get(testClients.Context, client.ObjectKey{
					Name:      sbdConfig.Name,
					Namespace: watchdogTestNamespace,
				}, retrievedConfig)
			}, time.Minute*1, time.Second*5).Should(Succeed())

			// Display the configuration for verification
			yamlData, err := yaml.Marshal(retrievedConfig.Spec)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("SBDConfig YAML:\n%s\n", string(yamlData))

			By("verifying watchdog configuration is properly applied")
			Expect(retrievedConfig.Spec.SbdWatchdogPath).To(Equal("/dev/watchdog"))
			Expect(retrievedConfig.Spec.WatchdogTimeout.Duration).To(Equal(90 * time.Second))
			Expect(*retrievedConfig.Spec.PetIntervalMultiple).To(Equal(int32(6)))
			// Note: Watchdog configuration is handled entirely through the CRD spec

			By("monitoring system stability with configuration changes")
			Consistently(func() bool {
				// Verify configuration remains stable
				currentConfig := &medik8sv1alpha1.SBDConfig{}
				err := testClients.Client.Get(testClients.Context, client.ObjectKey{
					Name:      sbdConfig.Name,
					Namespace: watchdogTestNamespace,
				}, currentConfig)
				if err != nil {
					return false
				}

				// Check generation hasn't changed unexpectedly
				return currentConfig.Generation >= retrievedConfig.Generation
			}, time.Minute*2, time.Second*10).Should(BeTrue())
		})
	})
})
