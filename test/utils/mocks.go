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
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/sbd-operator/pkg/mocks"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
	rtclient "sigs.k8s.io/controller-runtime/pkg/client"
	rtfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// WritePeerHeartbeat writes a heartbeat message to a specific peer slot for testing
func WritePeerHeartbeat(device *mocks.MockBlockDevice, nodeID uint16, timestamp uint64, sequence uint64) error {
	// Create heartbeat message
	header := sbdprotocol.NewHeartbeat(nodeID, sequence)
	header.Timestamp = timestamp
	heartbeatMsg := sbdprotocol.SBDHeartbeatMessage{Header: header}

	// Marshal the message
	msgBytes, err := sbdprotocol.MarshalHeartbeat(heartbeatMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat message: %w", err)
	}

	// Calculate slot offset for this node
	slotOffset := int64(nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write heartbeat message to the designated slot
	_, err = device.WriteAt(msgBytes, slotOffset)
	return err
}

// WriteFenceMessage writes a fence message to a specific slot for testing
func WriteFenceMessage(device *mocks.MockBlockDevice, nodeID, targetNodeID uint16, sequence uint64, reason uint8) error {
	// Create fence message header
	fenceMsg := sbdprotocol.NewFence(nodeID, targetNodeID, sequence, reason)

	// Marshal the fence message
	msgBytes, err := sbdprotocol.MarshalFence(fenceMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal fence message: %w", err)
	}

	// Calculate slot offset for the target node (where the fence message is written)
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write fence message to the designated slot
	_, err = device.WriteAt(msgBytes, slotOffset)
	return err
}

// NewFakeClient creates a minimal fake controller-runtime client for tests
func NewFakeClient(tb testing.TB) rtclient.Client {
	tb.Helper()
	// Use the default scheme with no custom types required for these tests
	return rtfake.NewClientBuilder().Build()
}

// TestAgentOptions contains options for creating test SBD agents
type TestAgentOptions struct {
	NodeName           string
	MetricsPort        int
	FileLockingEnabled bool
	PetInterval        time.Duration
	HeartbeatInterval  time.Duration
	PeerCheckInterval  time.Duration
	WatchdogTimeout    time.Duration
	SBDTimeoutSeconds  int
	RebootMethod       string
	SyncInterval       time.Duration
	K8sClient          rtclient.Client
	Logger             logr.Logger
}

// DefaultTestAgentOptions returns default options for test SBD agents
func DefaultTestAgentOptions() TestAgentOptions {
	return TestAgentOptions{
		NodeName:           "test-node",
		MetricsPort:        8081,
		FileLockingEnabled: true,
		PetInterval:        1 * time.Second,
		HeartbeatInterval:  1 * time.Second,
		PeerCheckInterval:  1 * time.Second,
		WatchdogTimeout:    1 * time.Second,
		SBDTimeoutSeconds:  30,
		RebootMethod:       "panic",
		SyncInterval:       2 * time.Second,
		Logger:             logr.Discard(),
	}
}
