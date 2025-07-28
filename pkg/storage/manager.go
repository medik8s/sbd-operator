package storage

import (
	"context"
	"fmt"
	"log"

	"github.com/medik8s/sbd-operator/pkg/storage/aws"
	"github.com/medik8s/sbd-operator/pkg/storage/k8s"
)

// Config holds all configuration for storage setup
type Config struct {
	// Storage Driver Selection
	StorageDriver string // "efs" or "nfs"
	NFSServer     string // For Standard NFS CSI driver
	NFSShare      string // For Standard NFS CSI driver

	// AWS Configuration
	AWSRegion        string
	ClusterName      string
	EFSName          string
	EFSFilesystemID  string
	StorageClassName string

	// Behavior flags
	CreateEFS  bool
	DryRun     bool
	UpdateMode bool

	// EFS Configuration
	PerformanceMode       string
	ThroughputMode        string
	ProvisionedThroughput int64

	// IAM Configuration
	EFSCSIRoleName string
}

// SetupResult contains the results of storage setup
type SetupResult struct {
	EFSFilesystemID  string
	StorageClassName string
	IAMRoleARN       string
	MountTargets     []string
	SecurityGroupID  string
	TestPassed       bool
}

// Manager orchestrates AWS and Kubernetes operations for shared storage setup
type Manager struct {
	config     *Config
	awsManager *aws.Manager
	k8sManager *k8s.Manager
}

// NewManager creates a new storage manager
func NewManager(ctx context.Context, config *Config) (*Manager, error) {
	// Set default storage driver if not specified
	if config.StorageDriver == "" {
		config.StorageDriver = "efs"
	}

	// Auto-detect cluster information if not provided (only for EFS driver)
	if config.StorageDriver == "efs" {
		if err := autoDetectConfig(ctx, config); err != nil {
			return nil, fmt.Errorf("failed to auto-detect configuration: %w", err)
		}
	}

	// Set defaults
	setDefaults(config)

	if config.DryRun {
		log.Println("[DRY-RUN] Would create storage manager with configuration:")
		printConfig(config)
		return &Manager{config: config}, nil
	}

	// Initialize AWS manager only for EFS driver
	var awsManager *aws.Manager
	if config.StorageDriver == "efs" {
		var err error
		awsManager, err = aws.NewManager(ctx, &aws.Config{
			Region:                config.AWSRegion,
			ClusterName:           config.ClusterName,
			EFSName:               config.EFSName,
			PerformanceMode:       config.PerformanceMode,
			ThroughputMode:        config.ThroughputMode,
			ProvisionedThroughput: config.ProvisionedThroughput,
			EFSCSIRoleName:        config.EFSCSIRoleName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create AWS manager: %w", err)
		}
	}

	// Initialize Kubernetes manager
	k8sManager, err := k8s.NewManager(ctx, &k8s.Config{
		StorageClassName: config.StorageClassName,
		ClusterName:      config.ClusterName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes manager: %w", err)
	}

	return &Manager{
		config:     config,
		awsManager: awsManager, // Will be nil for Standard NFS CSI driver
		k8sManager: k8sManager,
	}, nil
}

// SetupSharedStorage orchestrates the complete setup process
func (m *Manager) SetupSharedStorage(ctx context.Context) (*SetupResult, error) {
	result := &SetupResult{}

	if m.config.DryRun {
		return m.dryRunSetup(ctx)
	}

	log.Printf("🚀 Starting shared storage setup with %s driver...", m.config.StorageDriver)

	// Handle different storage drivers
	switch m.config.StorageDriver {
	case "efs":
		return m.setupEFSStorage(ctx, result)
	case "nfs":
		return m.setupStandardNFSStorage(ctx, result)
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", m.config.StorageDriver)
	}
}

// setupEFSStorage handles EFS CSI driver setup
func (m *Manager) setupEFSStorage(ctx context.Context, result *SetupResult) (*SetupResult, error) {
	// Step 1: Validate AWS permissions
	log.Println("📋 Step 1: Validating AWS permissions...")
	if err := m.awsManager.ValidateAWSPermissions(ctx); err != nil {
		return nil, fmt.Errorf("AWS permission validation failed: %w", err)
	}
	log.Println("✅ AWS permissions validated")

	// Step 2: Setup or validate EFS filesystem
	var efsID string
	if m.config.CreateEFS {
		log.Println("📁 Ensuring EFS filesystem exists...")
		var err error
		efsID, err = m.awsManager.CreateEFS(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create EFS: %w", err)
		}
	} else {
		efsID = m.config.EFSFilesystemID
		log.Printf("📁 Using existing EFS filesystem: %s", efsID)
		if err := m.awsManager.ValidateEFS(ctx, efsID); err != nil {
			return nil, fmt.Errorf("EFS validation failed: %w", err)
		}
	}
	result.EFSFilesystemID = efsID

	// Step 3: Setup EFS networking
	log.Println("🔗 Setting up EFS networking...")
	networkResult, err := m.awsManager.SetupNetworking(ctx, efsID)
	if err != nil {
		return nil, fmt.Errorf("failed to setup networking: %w", err)
	}
	result.MountTargets = networkResult.MountTargets
	result.SecurityGroupID = networkResult.SecurityGroupID
	log.Printf("✅ Networking configured: %d mount targets, security group %s",
		len(result.MountTargets), result.SecurityGroupID)

	// Step 4: Install/verify EFS CSI driver
	log.Println("🔧 Installing EFS CSI driver...")
	if err := m.k8sManager.InstallEFSCSIDriver(ctx); err != nil {
		return nil, fmt.Errorf("failed to install EFS CSI driver: %w", err)
	}
	log.Println("✅ EFS CSI driver installed")

	// Step 5: Configure EFS CSI service account for OpenShift
	log.Println("🔗 Configuring EFS CSI service account...")
	if err := m.k8sManager.ConfigureServiceAccount(ctx, ""); err != nil {
		return nil, fmt.Errorf("failed to configure service account: %w", err)
	}
	log.Println("✅ Service account configured")

	// Step 6: Create StorageClass for EFS
	log.Println("💾 Creating EFS StorageClass...")
	if err := m.k8sManager.CreateStorageClass(ctx, efsID); err != nil {
		return nil, fmt.Errorf("failed to create StorageClass: %w", err)
	}
	result.StorageClassName = m.config.StorageClassName
	log.Printf("✅ EFS StorageClass created: %s", result.StorageClassName)

	// Step 7: Test credentials
	log.Println("🧪 Testing EFS CSI driver credentials...")
	testPassed, err := m.k8sManager.TestCredentials(ctx, result.StorageClassName)
	if err != nil {
		log.Printf("⚠️ Credential test failed: %v", err)
		result.TestPassed = false
	} else {
		result.TestPassed = testPassed
		if testPassed {
			log.Println("✅ Credential test passed")
		} else {
			log.Println("⚠️ Credential test failed, but setup completed")
		}
	}

	return result, nil
}

// setupStandardNFSStorage handles Standard NFS CSI driver setup
func (m *Manager) setupStandardNFSStorage(ctx context.Context, result *SetupResult) (*SetupResult, error) {
	log.Printf("📋 Setting up Standard NFS CSI storage with server: %s, share: %s", m.config.NFSServer, m.config.NFSShare)

	// Step 1: Check if Standard NFS CSI driver is installed
	log.Println("🔧 Checking Standard NFS CSI driver installation...")
	if err := m.k8sManager.CheckStandardNFSCSIDriver(ctx); err != nil {
		return nil, fmt.Errorf("Standard NFS CSI driver not found. Install it with: kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/install-driver.yaml")
	}
	log.Println("✅ Standard NFS CSI driver found")

	// Step 2: Create StorageClass for Standard NFS CSI
	log.Println("💾 Creating Standard NFS StorageClass with SBD cache coherency options...")
	if err := m.k8sManager.CreateStandardNFSStorageClass(ctx, m.config.NFSServer, m.config.NFSShare); err != nil {
		return nil, fmt.Errorf("failed to create Standard NFS StorageClass: %w", err)
	}
	result.StorageClassName = m.config.StorageClassName
	log.Printf("✅ Standard NFS StorageClass created: %s", result.StorageClassName)

	// Step 3: Test Standard NFS storage
	log.Println("🧪 Testing Standard NFS CSI driver...")
	testPassed, err := m.k8sManager.TestCredentials(ctx, result.StorageClassName)
	if err != nil {
		log.Printf("⚠️ Storage test failed: %v", err)
		result.TestPassed = false
	} else {
		result.TestPassed = testPassed
		if testPassed {
			log.Println("✅ Standard NFS storage test passed")
		} else {
			log.Println("⚠️ Storage test failed, but setup completed")
		}
	}

	return result, nil
}

// Cleanup removes all created resources
func (m *Manager) Cleanup(ctx context.Context) error {
	if m.config.DryRun {
		log.Println("[DRY-RUN] Would clean up all resources")
		return nil
	}

	log.Println("🧹 Starting cleanup...")

	// Cleanup Kubernetes resources
	if m.k8sManager != nil {
		log.Println("🗑️ Cleaning up Kubernetes resources...")
		if err := m.k8sManager.Cleanup(ctx); err != nil {
			log.Printf("⚠️ Kubernetes cleanup failed: %v", err)
		}
	}

	// Cleanup AWS resources
	if m.awsManager != nil {
		log.Println("🗑️ Cleaning up AWS resources...")
		if err := m.awsManager.Cleanup(ctx); err != nil {
			log.Printf("⚠️ AWS cleanup failed: %v", err)
		}
	}

	return nil
}

// dryRunSetup simulates the setup process without making changes
func (m *Manager) dryRunSetup(ctx context.Context) (*SetupResult, error) {
	log.Println("[DRY-RUN] Simulating shared storage setup...")

	result := &SetupResult{
		EFSFilesystemID:  "fs-dry-run-example",
		StorageClassName: m.config.StorageClassName,
		MountTargets:     []string{"fsmt-12345", "fsmt-67890"},
		SecurityGroupID:  "sg-dryrun123",
		TestPassed:       true,
	}

	log.Println("[DRY-RUN] Would perform the following operations:")
	log.Printf("  📁 Create EFS filesystem: %s", result.EFSFilesystemID)
	log.Printf("  🔗 Create %d mount targets", len(result.MountTargets))
	log.Printf("  🛡️ Create security group: %s", result.SecurityGroupID)
	log.Printf("  💾 Create StorageClass: %s", result.StorageClassName)
	log.Printf("  🧪 Test credentials")

	return result, nil
}

// autoDetectConfig detects cluster and AWS configuration
func autoDetectConfig(ctx context.Context, config *Config) error {
	detector := &k8s.ClusterDetector{}

	// Auto-detect cluster name if not provided
	if config.ClusterName == "" {
		clusterName, err := detector.DetectClusterName(ctx)
		if err != nil {
			return fmt.Errorf("failed to detect cluster name: %w", err)
		}
		config.ClusterName = clusterName
		log.Printf("🔍 Auto-detected cluster name: %s", clusterName)
	}

	// Auto-detect AWS region if not provided
	if config.AWSRegion == "" {
		region, err := detector.DetectAWSRegion(ctx)
		if err != nil {
			return fmt.Errorf("failed to detect AWS region: %w", err)
		}
		config.AWSRegion = region
		log.Printf("🔍 Auto-detected AWS region: %s", region)
	}

	return nil
}

// setDefaults sets default values for configuration
func setDefaults(config *Config) {
	if config.StorageClassName == "" {
		config.StorageClassName = "sbd-efs-sc"
	}

	if config.EFSName == "" {
		config.EFSName = fmt.Sprintf("sbd-efs-%s", config.ClusterName)
	}

	if config.PerformanceMode == "" {
		config.PerformanceMode = "generalPurpose"
	}

	if config.ThroughputMode == "" {
		config.ThroughputMode = "provisioned"
	}

	if config.ProvisionedThroughput == 0 {
		config.ProvisionedThroughput = 10
	}

	if config.EFSCSIRoleName == "" {
		config.EFSCSIRoleName = fmt.Sprintf("EFS_CSI_DriverRole_%s", config.ClusterName)
	}
}

// printConfig prints the configuration for dry-run mode
func printConfig(config *Config) {
	log.Printf("  AWS Region: %s", config.AWSRegion)
	log.Printf("  Cluster Name: %s", config.ClusterName)
	log.Printf("  EFS Name: %s", config.EFSName)
	log.Printf("  Storage Class: %s", config.StorageClassName)
	log.Printf("  Performance Mode: %s", config.PerformanceMode)
	log.Printf("  Throughput Mode: %s", config.ThroughputMode)
	if config.ThroughputMode == "provisioned" {
		log.Printf("  Provisioned Throughput: %d MiB/s", config.ProvisionedThroughput)
	}
}
