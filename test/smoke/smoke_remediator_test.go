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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "sbd-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "sbd-operator-metrics-binding"

var _ = Describe("SBD Remediation Smoke Tests", Ordered, Label("Smoke", "Remediation"), func() {
	var controllerPodName string
	var testNS utils.TestNamespace

	// Verify the environment is set up correctly (setup handled by Makefile)
	BeforeAll(func() {
		By("initializing Kubernetes clients for watchdog tests if needed")
		if testClients == nil {
			var err error
			testClients, err = utils.SetupKubernetesClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to setup Kubernetes clients")
		}

		testNS = utils.TestNamespace{
			Name:    testNamespace,
			Clients: testClients,
		}
		utils.CleanupSBDConfigs(testClients.Client, testNS, testClients.Context)
	})

	// Clean up test-specific resources (overall cleanup handled by Makefile)
	AfterAll(func() {
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			debugCollector := testClients.NewDebugCollector()

			// Collect controller logs
			debugCollector.CollectControllerLogs(namespace, controllerPodName)

			// Collect Kubernetes events
			debugCollector.CollectKubernetesEvents(namespace)

			By("Fetching curl-metrics logs")
			req := clientset.CoreV1().Pods(namespace).GetLogs("curl-metrics", &corev1.PodLogOptions{})
			podLogs, err := req.Stream(ctx)
			if err == nil {
				defer podLogs.Close()
				buf := new(bytes.Buffer)
				_, _ = io.Copy(buf, podLogs)
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", buf.String())
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			// Collect controller pod description
			debugCollector.CollectPodDescription(namespace, controllerPodName)
		}

	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("SBD Remediation", func() {
		var sbdRemediationName string
		var tmpFile string

		BeforeEach(func() {
			sbdRemediationName = fmt.Sprintf("test-sbdremediation-%d", time.Now().UnixNano())
		})

		AfterEach(func() {
			// Clean up SBDRemediation
			By("cleaning up SBDRemediation resource")
			sbdRemediation := &medik8sv1alpha1.SBDRemediation{}
			err := testClients.Client.Get(ctx, client.ObjectKey{Name: sbdRemediationName, Namespace: testNamespace}, sbdRemediation)
			if err == nil {
				_ = testClients.Client.Delete(ctx, sbdRemediation)
			}

			// Wait for SBDRemediation deletion to complete
			By("waiting for SBDRemediation deletion to complete")
			Eventually(func() bool {
				sbdRemediation := &medik8sv1alpha1.SBDRemediation{}
				err := testClients.Client.Get(ctx, client.ObjectKey{Name: sbdRemediationName, Namespace: testNamespace}, sbdRemediation)
				return errors.IsNotFound(err) // Error means resource not found (deleted)
			}, 30*time.Second, 2*time.Second).Should(BeTrue())

			// Clean up temporary file
			if tmpFile != "" {
				os.Remove(tmpFile)
			}
		})

		It("should create and manage SBDRemediation resource", func() {
			By("creating an SBDRemediation resource")
			sbdRemediationYAML := fmt.Sprintf(`
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: %s
spec:
  nodeName: "test-node"
  reason: ManualFencing
  timeoutSeconds: 60
`, sbdRemediationName)

			// Write SBDRemediation to temporary file
			tmpFile = filepath.Join("/tmp", fmt.Sprintf("sbdremediation-%s.yaml", sbdRemediationName))
			err := os.WriteFile(tmpFile, []byte(sbdRemediationYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			// Apply the SBDRemediation
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation resource exists")
			verifySBDRemediationExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(sbdRemediationName))
			}
			Eventually(verifySBDRemediationExists, 30*time.Second).Should(Succeed())

			By("verifying the SBDRemediation has conditions")
			verifySBDRemediationConditions := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).ToNot(BeEmpty(), "SBDRemediation should have status conditions")
			}
			Eventually(verifySBDRemediationConditions, 60*time.Second).Should(Succeed())

			By("verifying the SBDRemediation status fields are set")
			verifySBDRemediationStatus := func(g Gomega) {
				// Check if operatorInstance is set
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.status.operatorInstance}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).ToNot(BeEmpty(), "SBDRemediation should have operatorInstance set")

				// Check if lastUpdateTime is set
				cmd = exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", sbdRemediationName, "-o", "jsonpath={.status.lastUpdateTime}")
				output, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).ToNot(BeEmpty(), "SBDRemediation should have lastUpdateTime set")
			}
			Eventually(verifySBDRemediationStatus, 60*time.Second).Should(Succeed())
		})
	})

	Context("SBD Remediation Enhancements", func() {
		It("should be able to create and reconcile SBDConfig resources", func() {
			By("creating a test SBDConfig from sample configuration")
			// Load sample configuration
			samplePath := "../../config/samples/medik8s_v1alpha1_sbdconfig.yaml"
			sampleData, err := os.ReadFile(samplePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read sample SBDConfig")

			// Replace the sample name with test name and ensure Always pull policy
			sbdConfigYAML := strings.ReplaceAll(string(sampleData), "sbdconfig-sample", "test-sbdconfig")
			sbdConfigYAML = strings.ReplaceAll(sbdConfigYAML, `imagePullPolicy: "IfNotPresent"`, `imagePullPolicy: "Always"`)

			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			cmd.Stdin = strings.NewReader(sbdConfigYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDConfig")

			By("verifying the SBDConfig is created and processed")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdconfig", "test-sbdconfig", "-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.Contains(output, "Ready")
			}, 4*time.Minute).Should(BeTrue())

			By("cleaning up the test SBDConfig")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdconfig", "test-sbdconfig", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should handle SBDRemediation resources with timeout validation", func() {
			By("creating a test SBDRemediation with custom timeout")
			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			sbdRemediationYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-sbdremediation-timeout
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 120
`
			cmd.Stdin = strings.NewReader(sbdRemediationYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation timeout is preserved")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", "test-sbdremediation-timeout", "-o", "jsonpath={.spec.timeoutSeconds}")
				output, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(output)
			}).Should(Equal("120"))

			By("verifying the SBDRemediation is processed")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", "test-sbdremediation-timeout", "-o", "jsonpath={.status.conditions}")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return strings.Contains(output, "Ready")
			}).Should(BeTrue())

			By("cleaning up the test SBDRemediation")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-sbdremediation-timeout", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should handle multiple SBDRemediation resources concurrently", func() {
			const numRemediations = 5
			remediationNames := make([]string, numRemediations)

			By(fmt.Sprintf("creating %d SBDRemediation resources", numRemediations))
			for i := 0; i < numRemediations; i++ {
				remediationNames[i] = fmt.Sprintf("test-concurrent-remediation-%d", i)
				sbdRemediationYAML := fmt.Sprintf(`
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: %s
spec:
  nodeName: test-worker-node-%d
  reason: NodeUnresponsive
  timeoutSeconds: 60
`, remediationNames[i], i)

				cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
				cmd.Stdin = strings.NewReader(sbdRemediationYAML)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create SBDRemediation %d", i))
			}

			By("verifying all SBDRemediations are processed")
			for i, name := range remediationNames {
				Eventually(func() bool {
					cmd := exec.Command("kubectl", "-n", testNamespace, "get", "sbdremediation", name, "-o", "jsonpath={.status.conditions}")
					output, err := utils.Run(cmd)
					if err != nil {
						return false
					}
					return strings.Contains(output, "Ready")
				}).Should(BeTrue(), fmt.Sprintf("SBDRemediation %d should be ready", i))
			}

			By("cleaning up all test SBDRemediations")
			for _, name := range remediationNames {
				cmd := exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", name, "--ignore-not-found=true")
				_, _ = utils.Run(cmd)
			}
		})

		It("should handle SBDRemediation resources with invalid node names", func() {
			By("creating a test SBDRemediation with invalid node name")
			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			sbdRemediationYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-invalid-node-remediation
spec:
  nodeName: non-existent-node-12345
  reason: HeartbeatTimeout
  timeoutSeconds: 30
`
			cmd.Stdin = strings.NewReader(sbdRemediationYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create SBDRemediation")

			By("verifying the SBDRemediation becomes ready with failed fencing")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "-n", testNamespace, "sbdremediation", "test-invalid-node-remediation", "-o", "json")
				output, err := utils.Run(cmd)
				if err != nil {
					return false
				}

				var remediation map[string]interface{}
				if err := json.Unmarshal([]byte(output), &remediation); err != nil {
					return false
				}

				status, ok := remediation["status"].(map[string]interface{})
				if !ok {
					return false
				}

				conditions, ok := status["conditions"].([]interface{})
				if !ok {
					return false
				}

				readyFound := false
				fencingFailed := false

				for _, conditionInterface := range conditions {
					condition, ok := conditionInterface.(map[string]interface{})
					if !ok {
						continue
					}

					conditionType, ok := condition["type"].(string)
					if !ok {
						continue
					}

					conditionStatus, ok := condition["status"].(string)
					if !ok {
						continue
					}

					if conditionType == "Ready" && conditionStatus == "True" {
						readyFound = true
						reason, _ := condition["reason"].(string)
						if reason == "Failed" {
							fencingFailed = true
						}
					}
				}

				return readyFound && fencingFailed
			}).Should(BeTrue())

			By("cleaning up the test SBDRemediation")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-invalid-node-remediation", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should validate timeout ranges in SBDRemediation CRD", func() {
			By("attempting to create SBDRemediation with timeout below minimum")
			cmd := exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			invalidTimeoutYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-invalid-timeout-low
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 29
`
			cmd.Stdin = strings.NewReader(invalidTimeoutYAML)
			_, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should reject timeout below minimum (30)")

			By("attempting to create SBDRemediation with timeout above maximum")
			cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			invalidTimeoutYAML = `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-invalid-timeout-high
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 301
`
			cmd.Stdin = strings.NewReader(invalidTimeoutYAML)
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Should reject timeout above maximum (300)")

			By("creating SBDRemediation with valid timeout at boundaries")
			validTimeoutYAML := `
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: test-valid-timeout-boundary
spec:
  nodeName: test-worker-node
  reason: ManualFencing
  timeoutSeconds: 30
`
			cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			cmd.Stdin = strings.NewReader(validTimeoutYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should accept minimum valid timeout (30)")

			validTimeoutYAML = strings.ReplaceAll(validTimeoutYAML, "timeoutSeconds: 30", "timeoutSeconds: 300")
			validTimeoutYAML = strings.ReplaceAll(validTimeoutYAML, "test-valid-timeout-boundary", "test-valid-timeout-boundary-max")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(validTimeoutYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Should accept maximum valid timeout (300)")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-valid-timeout-boundary", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "-n", testNamespace, "sbdremediation", "test-valid-timeout-boundary-max", "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})
	})
})
