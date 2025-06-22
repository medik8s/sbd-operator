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

// Package sbdprotocol implements node mapping management for SBD devices
package sbdprotocol

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// SBDDevice interface for block device operations
type SBDDevice interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
}

// NodeManager manages node-to-slot mappings with persistence to SBD device
type NodeManager struct {
	device      SBDDevice
	table       *NodeMapTable
	hasher      *NodeHasher
	clusterName string
	logger      logr.Logger
	mutex       sync.RWMutex

	// Configuration
	syncInterval     time.Duration
	staleNodeTimeout time.Duration

	// State
	lastSync    time.Time
	lastCleanup time.Time
	dirty       bool
}

// NodeManagerConfig holds configuration for the node manager
type NodeManagerConfig struct {
	ClusterName      string
	SyncInterval     time.Duration
	StaleNodeTimeout time.Duration
	Logger           logr.Logger
}

// DefaultNodeManagerConfig returns a default configuration
func DefaultNodeManagerConfig() NodeManagerConfig {
	return NodeManagerConfig{
		ClusterName:      "default-cluster",
		SyncInterval:     30 * time.Second,
		StaleNodeTimeout: 10 * time.Minute,
		Logger:           logr.Discard(),
	}
}

// NewNodeManager creates a new node manager with the specified SBD device
func NewNodeManager(device SBDDevice, config NodeManagerConfig) (*NodeManager, error) {
	if device == nil {
		return nil, fmt.Errorf("SBD device cannot be nil")
	}

	if config.ClusterName == "" {
		config.ClusterName = "default-cluster"
	}

	if config.SyncInterval == 0 {
		config.SyncInterval = 30 * time.Second
	}

	if config.StaleNodeTimeout == 0 {
		config.StaleNodeTimeout = 10 * time.Minute
	}

	manager := &NodeManager{
		device:           device,
		clusterName:      config.ClusterName,
		logger:           config.Logger,
		syncInterval:     config.SyncInterval,
		staleNodeTimeout: config.StaleNodeTimeout,
		hasher:           NewNodeHasher(config.ClusterName),
	}

	// Try to load existing mapping table from device
	if err := manager.loadFromDevice(); err != nil {
		manager.logger.Info("Failed to load existing node mapping, creating new table", "error", err)
		manager.table = NewNodeMapTable(config.ClusterName)
		manager.dirty = true
	}

	return manager, nil
}

// GetSlotForNode returns the slot ID for a given node name, assigning one if necessary
func (nm *NodeManager) GetSlotForNode(nodeName string) (uint16, error) {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	if nodeName == "" {
		return 0, fmt.Errorf("node name cannot be empty")
	}

	// Check if node already has a slot
	if slot, found := nm.table.GetSlotForNode(nodeName); found {
		// Update last seen timestamp
		if err := nm.table.UpdateLastSeen(nodeName); err != nil {
			nm.logger.Error(err, "Failed to update last seen timestamp", "nodeName", nodeName)
		}
		nm.dirty = true
		return slot, nil
	}

	// Assign a new slot
	slot, err := nm.table.AssignSlot(nodeName, nm.hasher)
	if err != nil {
		return 0, fmt.Errorf("failed to assign slot for node %s: %w", nodeName, err)
	}

	nm.dirty = true
	nm.logger.Info("Assigned new slot to node", "nodeName", nodeName, "slotID", slot)

	// Trigger immediate sync for new assignments
	if err := nm.syncToDevice(); err != nil {
		nm.logger.Error(err, "Failed to sync node mapping to device after assignment", "nodeName", nodeName)
		// Don't fail the assignment, but log the error
	}

	return slot, nil
}

// GetNodeForSlot returns the node name for a given slot ID
func (nm *NodeManager) GetNodeForSlot(slotID uint16) (string, bool) {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	return nm.table.GetNodeForSlot(slotID)
}

// RemoveNode removes a node from the mapping table
func (nm *NodeManager) RemoveNode(nodeName string) error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	if err := nm.table.RemoveNode(nodeName); err != nil {
		return err
	}

	nm.dirty = true
	nm.logger.Info("Removed node from mapping", "nodeName", nodeName)

	return nil
}

// GetActiveNodes returns a list of nodes that have been seen recently
func (nm *NodeManager) GetActiveNodes() []string {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	return nm.table.GetActiveNodes(nm.staleNodeTimeout)
}

// GetStats returns statistics about the node mapping
func (nm *NodeManager) GetStats() map[string]interface{} {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	stats := nm.table.GetStats()
	stats["last_sync"] = nm.lastSync
	stats["last_cleanup"] = nm.lastCleanup
	stats["sync_interval"] = nm.syncInterval
	stats["stale_timeout"] = nm.staleNodeTimeout

	return stats
}

// Sync forces an immediate synchronization of the mapping table to the SBD device
func (nm *NodeManager) Sync() error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	return nm.syncToDevice()
}

// CleanupStaleNodes removes nodes that haven't been seen for the configured timeout
func (nm *NodeManager) CleanupStaleNodes() ([]string, error) {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	removedNodes := nm.table.CleanupStaleNodes(nm.staleNodeTimeout)
	if len(removedNodes) > 0 {
		nm.dirty = true
		nm.lastCleanup = time.Now()
		nm.logger.Info("Cleaned up stale nodes", "count", len(removedNodes), "nodes", removedNodes)

		// Sync after cleanup
		if err := nm.syncToDevice(); err != nil {
			nm.logger.Error(err, "Failed to sync after stale node cleanup")
			return removedNodes, err
		}
	}

	return removedNodes, nil
}

// StartPeriodicSync starts a background goroutine that periodically syncs and cleans up
func (nm *NodeManager) StartPeriodicSync() chan struct{} {
	stopChan := make(chan struct{})

	go func() {
		ticker := time.NewTicker(nm.syncInterval)
		defer ticker.Stop()

		cleanupTicker := time.NewTicker(nm.staleNodeTimeout / 2) // Cleanup twice as often as stale timeout
		defer cleanupTicker.Stop()

		for {
			select {
			case <-stopChan:
				nm.logger.Info("Stopping periodic sync")
				return
			case <-ticker.C:
				if err := nm.Sync(); err != nil {
					nm.logger.Error(err, "Periodic sync failed")
				}
			case <-cleanupTicker.C:
				if _, err := nm.CleanupStaleNodes(); err != nil {
					nm.logger.Error(err, "Periodic cleanup failed")
				}
			}
		}
	}()

	nm.logger.Info("Started periodic sync", "syncInterval", nm.syncInterval, "staleTimeout", nm.staleNodeTimeout)
	return stopChan
}

// loadFromDevice loads the node mapping table from the SBD device
func (nm *NodeManager) loadFromDevice() error {
	// Read the node mapping slot (slot 0)
	slotOffset := int64(SBD_NODE_MAP_SLOT) * SBD_SLOT_SIZE
	slotData := make([]byte, SBD_SLOT_SIZE)

	n, err := nm.device.ReadAt(slotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read node mapping slot: %w", err)
	}

	if n != SBD_SLOT_SIZE {
		return fmt.Errorf("partial read from node mapping slot: read %d bytes, expected %d", n, SBD_SLOT_SIZE)
	}

	// Check if the slot contains valid data
	if isEmptySlot(slotData) {
		return fmt.Errorf("node mapping slot is empty")
	}

	// The slot might contain multiple chunks, so we need to read the full mapping
	// For now, we'll assume it fits in one slot, but this could be extended
	table, err := UnmarshalNodeMapTable(slotData)
	if err != nil {
		return fmt.Errorf("failed to unmarshal node mapping table: %w", err)
	}

	// Verify cluster name matches
	if table.ClusterName != nm.clusterName {
		return fmt.Errorf("cluster name mismatch: expected %s, got %s", nm.clusterName, table.ClusterName)
	}

	nm.table = table
	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.Info("Loaded node mapping from device",
		"nodeCount", len(table.Entries),
		"clusterName", table.ClusterName,
		"lastUpdate", table.LastUpdate)

	return nil
}

// syncToDevice writes the node mapping table to the SBD device
func (nm *NodeManager) syncToDevice() error {
	if !nm.dirty {
		return nil // Nothing to sync
	}

	// Marshal the table
	data, err := nm.table.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal node mapping table: %w", err)
	}

	// Check if data fits in one slot
	if len(data) > SBD_SLOT_SIZE {
		return fmt.Errorf("node mapping table too large: %d bytes, maximum %d", len(data), SBD_SLOT_SIZE)
	}

	// Prepare slot data (pad with zeros if necessary)
	slotData := make([]byte, SBD_SLOT_SIZE)
	copy(slotData, data)

	// Write to the node mapping slot (slot 0)
	slotOffset := int64(SBD_NODE_MAP_SLOT) * SBD_SLOT_SIZE
	n, err := nm.device.WriteAt(slotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write node mapping slot: %w", err)
	}

	if n != SBD_SLOT_SIZE {
		return fmt.Errorf("partial write to node mapping slot: wrote %d bytes, expected %d", n, SBD_SLOT_SIZE)
	}

	// Ensure data is synced to disk
	if err := nm.device.Sync(); err != nil {
		return fmt.Errorf("failed to sync node mapping to device: %w", err)
	}

	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.V(1).Info("Synced node mapping to device",
		"dataSize", len(data),
		"nodeCount", len(nm.table.Entries))

	return nil
}

// isEmptySlot checks if a slot contains only zeros or is uninitialized
func isEmptySlot(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// Close closes the node manager and syncs any pending changes
func (nm *NodeManager) Close() error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Sync any pending changes
	if nm.dirty {
		if err := nm.syncToDevice(); err != nil {
			nm.logger.Error(err, "Failed to sync during close")
			return err
		}
	}

	nm.logger.Info("Node manager closed")
	return nil
}

// ValidateIntegrity checks the integrity of the node mapping
func (nm *NodeManager) ValidateIntegrity() error {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	// Check for duplicate slots
	slotCount := make(map[uint16]int)
	for _, entry := range nm.table.Entries {
		slotCount[entry.SlotID]++
	}

	for slotID, count := range slotCount {
		if count > 1 {
			return fmt.Errorf("duplicate slot assignment detected: slot %d assigned to %d nodes", slotID, count)
		}
	}

	// Check for orphaned slots in SlotUsage
	for slotID, nodeName := range nm.table.SlotUsage {
		if entry, exists := nm.table.Entries[nodeName]; !exists {
			return fmt.Errorf("orphaned slot usage: slot %d points to non-existent node %s", slotID, nodeName)
		} else if entry.SlotID != slotID {
			return fmt.Errorf("slot usage mismatch: slot %d points to node %s, but node has slot %d", slotID, nodeName, entry.SlotID)
		}
	}

	// Check for orphaned entries
	for nodeName, entry := range nm.table.Entries {
		if usedNode, exists := nm.table.SlotUsage[entry.SlotID]; !exists {
			return fmt.Errorf("orphaned entry: node %s has slot %d, but slot is not in usage map", nodeName, entry.SlotID)
		} else if usedNode != nodeName {
			return fmt.Errorf("entry mismatch: node %s has slot %d, but slot is assigned to %s", nodeName, entry.SlotID, usedNode)
		}
	}

	return nil
}

// GetClusterName returns the cluster name
func (nm *NodeManager) GetClusterName() string {
	return nm.clusterName
}

// IsDirty returns whether the mapping table has unsaved changes
func (nm *NodeManager) IsDirty() bool {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()
	return nm.dirty
}

// GetLastSync returns the timestamp of the last successful sync
func (nm *NodeManager) GetLastSync() time.Time {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()
	return nm.lastSync
}
