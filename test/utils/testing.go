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

package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
)

// TestClients holds the Kubernetes clients used for testing
type TestClients struct {
	Client    client.Client
	Clientset *kubernetes.Clientset
	Context   context.Context
}

// TestNamespace represents a test namespace with cleanup functionality
type TestNamespace struct {
	Name    string
	Clients *TestClients
}

// PodStatusChecker provides utilities for checking pod status
type PodStatusChecker struct {
	Clients   *TestClients
	Namespace string
	Labels    map[string]string
}

// ResourceCleaner provides utilities for cleaning up test resources
type ResourceCleaner struct {
	Clients *TestClients
}

// SetupKubernetesClients initializes Kubernetes clients for testing
func SetupKubernetesClients() (*TestClients, error) {
	// Load kubeconfig - try environment variable first, then default location
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfig = filepath.Join(homeDir, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	// Create scheme with core Kubernetes types and add our CRDs
	clientScheme := runtime.NewScheme()
	err = scheme.AddToScheme(clientScheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	err = medik8sv1alpha1.AddToScheme(clientScheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add SBD types to scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := client.New(config, client.Options{Scheme: clientScheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create standard clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &TestClients{
		Client:    k8sClient,
		Clientset: clientset,
		Context:   context.Background(),
	}, nil
}

// CreateTestNamespace creates a test namespace and returns a cleanup function
func (tc *TestClients) CreateTestNamespace(namePrefix string) (*TestNamespace, error) {
	name := fmt.Sprintf("%s-%d", namePrefix, time.Now().UnixNano())

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	err := tc.Client.Create(tc.Context, ns)
	if err != nil {
		return nil, fmt.Errorf("failed to create namespace %s: %w", name, err)
	}

	return &TestNamespace{
		Name:    name,
		Clients: tc,
	}, nil
}

// Cleanup removes the test namespace and all its resources
func (tn *TestNamespace) Cleanup() error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: tn.Name,
		},
	}

	err := tn.Clients.Client.Delete(tn.Clients.Context, ns)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace %s: %w", tn.Name, err)
	}

	// Wait for namespace to be fully deleted
	Eventually(func() bool {
		var namespace corev1.Namespace
		err := tn.Clients.Client.Get(tn.Clients.Context, client.ObjectKey{Name: tn.Name}, &namespace)
		return errors.IsNotFound(err)
	}, time.Minute*2, time.Second*5).Should(BeTrue())

	return nil
}

// CreateSBDConfig creates a test SBDConfig with common defaults
func (tn *TestNamespace) CreateSBDConfig(name string, options ...func(*medik8sv1alpha1.SBDConfig)) (*medik8sv1alpha1.SBDConfig, error) {
	sbdConfig := &medik8sv1alpha1.SBDConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tn.Name,
		},
		Spec: medik8sv1alpha1.SBDConfigSpec{
			SbdWatchdogPath: "/dev/watchdog",
			WatchdogTimeout: &metav1.Duration{Duration: 60 * time.Second},
			PetIntervalMultiple: func() *int32 {
				val := int32(4)
				return &val
			}(),
		},
	}

	// Apply any custom options
	for _, option := range options {
		option(sbdConfig)
	}

	err := tn.Clients.Client.Create(tn.Clients.Context, sbdConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create SBDConfig %s: %w", name, err)
	}

	return sbdConfig, nil
}

// CleanupSBDConfig deletes an SBDConfig and waits for cleanup to complete
func (tn *TestNamespace) CleanupSBDConfig(sbdConfig *medik8sv1alpha1.SBDConfig) error {
	err := tn.Clients.Client.Delete(tn.Clients.Context, sbdConfig)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete SBDConfig %s: %w", sbdConfig.Name, err)
	}

	// Wait for SBDConfig to be fully deleted
	Eventually(func() bool {
		var config medik8sv1alpha1.SBDConfig
		err := tn.Clients.Client.Get(tn.Clients.Context, client.ObjectKey{
			Name:      sbdConfig.Name,
			Namespace: tn.Name,
		}, &config)
		return errors.IsNotFound(err)
	}, time.Minute*2, time.Second*5).Should(BeTrue())

	// Wait for associated DaemonSets to be deleted
	Eventually(func() int {
		daemonSets := &appsv1.DaemonSetList{}
		err := tn.Clients.Client.List(tn.Clients.Context, daemonSets,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbdconfig": sbdConfig.Name})
		if err != nil {
			return -1
		}
		return len(daemonSets.Items)
	}, time.Minute*2, time.Second*5).Should(Equal(0))

	// Wait for associated pods to be terminated
	Eventually(func() int {
		pods := &corev1.PodList{}
		err := tn.Clients.Client.List(tn.Clients.Context, pods,
			client.InNamespace(tn.Name),
			client.MatchingLabels{"sbdconfig": sbdConfig.Name})
		if err != nil {
			return -1
		}
		return len(pods.Items)
	}, time.Minute*2, time.Second*5).Should(Equal(0))

	return nil
}

// NewPodStatusChecker creates a new PodStatusChecker for monitoring pods
func (tn *TestNamespace) NewPodStatusChecker(labels map[string]string) *PodStatusChecker {
	return &PodStatusChecker{
		Clients:   tn.Clients,
		Namespace: tn.Name,
		Labels:    labels,
	}
}

// WaitForPodsReady waits for pods matching the labels to become ready
func (psc *PodStatusChecker) WaitForPodsReady(minCount int, timeout time.Duration) error {
	Eventually(func() int {
		pods := &corev1.PodList{}
		err := psc.Clients.Client.List(psc.Clients.Context, pods,
			client.InNamespace(psc.Namespace),
			client.MatchingLabels(psc.Labels))
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods: %v\n", err)
			return 0
		}

		readyPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady &&
						condition.Status == corev1.ConditionTrue {
						readyPods++
						break
					}
				}
			}
		}

		GinkgoWriter.Printf("Found %d ready pods out of %d total\n", readyPods, len(pods.Items))
		return readyPods
	}, timeout, time.Second*15).Should(BeNumerically(">=", minCount))

	return nil
}

// WaitForPodsRunning waits for pods to be in Running state (not necessarily ready)
func (psc *PodStatusChecker) WaitForPodsRunning(minCount int, timeout time.Duration) error {
	Eventually(func() int {
		pods := &corev1.PodList{}
		err := psc.Clients.Client.List(psc.Clients.Context, pods,
			client.InNamespace(psc.Namespace),
			client.MatchingLabels(psc.Labels))
		if err != nil {
			GinkgoWriter.Printf("Failed to list pods: %v\n", err)
			return 0
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPods++
			}
		}

		GinkgoWriter.Printf("Found %d running pods out of %d total\n", runningPods, len(pods.Items))
		return runningPods
	}, timeout, time.Second*15).Should(BeNumerically(">=", minCount))

	return nil
}

// GetPodLogs retrieves logs from a pod
func (psc *PodStatusChecker) GetPodLogs(podName string, tailLines *int64) (string, error) {
	logOptions := &corev1.PodLogOptions{}
	if tailLines != nil {
		logOptions.TailLines = tailLines
	}

	logs, err := psc.Clients.Clientset.CoreV1().Pods(psc.Namespace).
		GetLogs(podName, logOptions).DoRaw(psc.Clients.Context)
	if err != nil {
		return "", fmt.Errorf("failed to get logs from pod %s: %w", podName, err)
	}

	return string(logs), nil
}

// CheckPodRestarts checks if any pods have been restarted and returns details
func (psc *PodStatusChecker) CheckPodRestarts() (bool, []string) {
	pods := &corev1.PodList{}
	err := psc.Clients.Client.List(psc.Clients.Context, pods,
		client.InNamespace(psc.Namespace),
		client.MatchingLabels(psc.Labels))
	if err != nil {
		return false, []string{fmt.Sprintf("Failed to list pods: %v", err)}
	}

	hasRestarts := false
	var restartInfo []string

	for _, pod := range pods.Items {
		totalRestarts := int32(0)
		for _, containerStatus := range pod.Status.ContainerStatuses {
			totalRestarts += containerStatus.RestartCount
		}

		if totalRestarts > 0 {
			hasRestarts = true
			restartInfo = append(restartInfo, fmt.Sprintf("Pod %s has %d total restarts", pod.Name, totalRestarts))
		}
	}

	return hasRestarts, restartInfo
}

// GetFirstPod returns the first pod matching the labels
func (psc *PodStatusChecker) GetFirstPod() (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	err := psc.Clients.Client.List(psc.Clients.Context, pods,
		client.InNamespace(psc.Namespace),
		client.MatchingLabels(psc.Labels))
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found matching labels")
	}

	return &pods.Items[0], nil
}

// DebugCollector provides utilities for collecting debug information
type DebugCollector struct {
	Clients *TestClients
}

// NewDebugCollector creates a new DebugCollector
func (tc *TestClients) NewDebugCollector() *DebugCollector {
	return &DebugCollector{Clients: tc}
}

// CollectControllerLogs collects logs from the controller manager pod
func (dc *DebugCollector) CollectControllerLogs(namespace, podName string) {
	By("Fetching controller manager pod logs")
	req := dc.Clients.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	podLogs, err := req.Stream(dc.Clients.Context)
	if err == nil {
		defer podLogs.Close()
		buf := new(bytes.Buffer)
		_, _ = io.Copy(buf, podLogs)
		_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", buf.String())
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
	}
}

// CollectKubernetesEvents collects Kubernetes events from a namespace
func (dc *DebugCollector) CollectKubernetesEvents(namespace string) {
	By("Fetching Kubernetes events")
	events, err := dc.Clients.Clientset.CoreV1().Events(namespace).List(dc.Clients.Context, metav1.ListOptions{})
	if err == nil {
		eventsOutput := ""
		for _, event := range events.Items {
			eventsOutput += fmt.Sprintf("%s  %s     %s  %s/%s  %s\n",
				event.LastTimestamp.Format("2006-01-02T15:04:05Z"),
				event.Type,
				event.Reason,
				event.InvolvedObject.Kind,
				event.InvolvedObject.Name,
				event.Message)
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
	}
}

// CollectPodDescription collects and prints pod description
func (dc *DebugCollector) CollectPodDescription(namespace, podName string) {
	By(fmt.Sprintf("Fetching %s pod description", podName))
	pod := &corev1.Pod{}
	err := dc.Clients.Client.Get(dc.Clients.Context, client.ObjectKey{Name: podName, Namespace: namespace}, pod)
	if err == nil {
		podYAML, _ := yaml.Marshal(pod)
		_, _ = fmt.Fprintf(GinkgoWriter, "Pod description:\n%s", string(podYAML))
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get pod description: %s", err)
	}
}

// ServiceAccountTokenGenerator provides utilities for generating service account tokens
type ServiceAccountTokenGenerator struct {
	Clients *TestClients
}

// NewServiceAccountTokenGenerator creates a new token generator
func (tc *TestClients) NewServiceAccountTokenGenerator() *ServiceAccountTokenGenerator {
	return &ServiceAccountTokenGenerator{Clients: tc}
}

// GenerateToken generates a token for the specified service account
func (satg *ServiceAccountTokenGenerator) GenerateToken(namespace, serviceAccountName string) (string, error) {
	var token string

	Eventually(func() error {
		// Create TokenRequest using the typed client
		tokenRequest := &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				// Set a reasonable expiration time (1 hour)
				ExpirationSeconds: func() *int64 {
					val := int64(3600)
					return &val
				}(),
			},
		}

		// Use the authentication client to create the token
		result, err := satg.Clients.Clientset.CoreV1().ServiceAccounts(namespace).
			CreateToken(satg.Clients.Context, serviceAccountName, tokenRequest, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create service account token: %w", err)
		}

		token = result.Status.Token
		if token == "" {
			return fmt.Errorf("received empty token")
		}

		return nil
	}, time.Minute*2, time.Second*10).Should(Succeed())

	return token, nil
}

// NodeStabilityChecker provides utilities for checking node stability
type NodeStabilityChecker struct {
	Clients *TestClients
}

// NewNodeStabilityChecker creates a new node stability checker
func (tc *TestClients) NewNodeStabilityChecker() *NodeStabilityChecker {
	return &NodeStabilityChecker{Clients: tc}
}

// WaitForNodesStable waits for all nodes to remain stable (Ready)
func (nsc *NodeStabilityChecker) WaitForNodesStable(duration time.Duration) error {
	Consistently(func() bool {
		nodes := &corev1.NodeList{}
		err := nsc.Clients.Client.List(nsc.Clients.Context, nodes)
		if err != nil {
			GinkgoWriter.Printf("Failed to list nodes: %v\n", err)
			return false
		}

		readyNodeCount := 0
		for _, node := range nodes.Items {
			isReady := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady &&
					condition.Status == corev1.ConditionTrue {
					isReady = true
					readyNodeCount++
					break
				}
			}
			if !isReady {
				GinkgoWriter.Printf("Node %s is not ready: %+v\n", node.Name, node.Status.Conditions)
				return false
			}
		}

		GinkgoWriter.Printf("All %d nodes remain ready\n", readyNodeCount)
		return true
	}, duration, time.Second*15).Should(BeTrue())

	return nil
}

// DaemonSetChecker provides utilities for checking DaemonSet status
type DaemonSetChecker struct {
	Clients   *TestClients
	Namespace string
}

// NewDaemonSetChecker creates a new DaemonSet checker
func (tn *TestNamespace) NewDaemonSetChecker() *DaemonSetChecker {
	return &DaemonSetChecker{
		Clients:   tn.Clients,
		Namespace: tn.Name,
	}
}

// WaitForDaemonSet waits for a DaemonSet to be created and returns it
func (dsc *DaemonSetChecker) WaitForDaemonSet(labels map[string]string, timeout time.Duration) (*appsv1.DaemonSet, error) {
	var daemonSet *appsv1.DaemonSet

	Eventually(func() bool {
		daemonSets := &appsv1.DaemonSetList{}
		err := dsc.Clients.Client.List(dsc.Clients.Context, daemonSets,
			client.InNamespace(dsc.Namespace),
			client.MatchingLabels(labels))
		if err != nil {
			GinkgoWriter.Printf("Failed to list DaemonSets: %v\n", err)
			return false
		}
		if len(daemonSets.Items) == 0 {
			GinkgoWriter.Printf("No DaemonSets found with labels %v\n", labels)
			return false
		}
		daemonSet = &daemonSets.Items[0]
		GinkgoWriter.Printf("Found DaemonSet: %s\n", daemonSet.Name)
		return true
	}, timeout, time.Second*10).Should(BeTrue())

	return daemonSet, nil
}

// CheckDaemonSetArgs verifies that a DaemonSet container has expected arguments
func (dsc *DaemonSetChecker) CheckDaemonSetArgs(ds *appsv1.DaemonSet, expectedArgs []string) error {
	Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
	container := ds.Spec.Template.Spec.Containers[0]

	argsStr := strings.Join(container.Args, " ")
	GinkgoWriter.Printf("DaemonSet container args: %s\n", argsStr)

	for _, expectedArg := range expectedArgs {
		Expect(argsStr).To(ContainSubstring(expectedArg))
	}

	return nil
}
