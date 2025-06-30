#!/bin/bash

# EFS Storage Management Script for OpenShift
# This script manages AWS EFS filesystems and Kubernetes StorageClass for SBD operator

set -e

# Disable AWS CLI pager to prevent hanging
export AWS_PAGER=""

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
STORAGE_CLASS_NAME="sbd-efs-sc"
EFS_FILESYSTEM_ID=""
EFS_NAME="sbd-operator-shared-storage"
AWS_REGION="${AWS_REGION:-}"
CLUSTER_NAME=""
PERFORMANCE_MODE="generalPurpose"  # generalPurpose or maxIO
THROUGHPUT_MODE="provisioned"      # provisioned or burstingThroughput
PROVISIONED_THROUGHPUT="100"       # MiB/s (only for provisioned mode)
KUBECTL="${KUBECTL:-kubectl}"
CLEANUP="false"
DRY_RUN="false"
CREATE_EFS="true"
SKIP_CSI_INSTALL="false"

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1" >&2
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" >&2
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Manage EFS filesystems and StorageClass for OpenShift

OPTIONS:
    -s, --storage-class NAME  StorageClass name (default: sbd-efs-sc)
    -f, --filesystem-id ID    EFS filesystem ID (use existing EFS instead of creating new one)
    -n, --efs-name NAME       EFS filesystem name for creation/tagging (default: sbd-operator-shared-storage)
    -r, --region REGION       AWS region (auto-detected if not provided)
    -k, --cluster-name NAME   OpenShift cluster name (auto-detected if not provided)
    --performance-mode MODE   EFS performance mode: generalPurpose|maxIO (default: generalPurpose)
    --throughput-mode MODE    EFS throughput mode: provisioned|burstingThroughput (default: provisioned)
    --provisioned-tp MBPS     Provisioned throughput in MiB/s (default: 100, only for provisioned mode)
    --create-efs              Create new EFS filesystem with proper tags (default behavior)
    --cleanup                 Delete EFS filesystem and StorageClass
    --skip-csi-install        Skip automatic EFS CSI driver installation
    --dry-run                 Show what would be created without actually creating
    -h, --help                Show this help message

EXAMPLES:
    # Create EFS filesystem and StorageClass (default behavior)
    $0

    # Create StorageClass with existing EFS filesystem ID
    $0 --filesystem-id fs-1234567890abcdef0

    # Create StorageClass with custom name and new EFS
    $0 --storage-class my-efs-sc --efs-name my-shared-storage

    # Clean up EFS and StorageClass
    $0 --cleanup --efs-name sbd-operator-shared-storage

PREREQUISITES:
    - kubectl configured for target OpenShift cluster
    - AWS CLI configured with appropriate credentials
    - jq installed for JSON processing

AWS PERMISSIONS REQUIRED:
    - elasticfilesystem:CreateFileSystem, DescribeFileSystems, DeleteFileSystem
    - elasticfilesystem:CreateTags, DescribeTags (optional but recommended)
    - kubectl access to install EFS CSI driver

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -s|--storage-class)
            STORAGE_CLASS_NAME="$2"
            shift 2
            ;;
        -f|--filesystem-id)
            EFS_FILESYSTEM_ID="$2"
            CREATE_EFS="false"  # Disable EFS creation when using existing filesystem
            shift 2
            ;;
        -n|--efs-name)
            EFS_NAME="$2"
            shift 2
            ;;
        -r|--region)
            AWS_REGION="$2"
            shift 2
            ;;
        -k|--cluster-name)
            CLUSTER_NAME="$2"
            shift 2
            ;;
        --performance-mode)
            PERFORMANCE_MODE="$2"
            shift 2
            ;;
        --throughput-mode)
            THROUGHPUT_MODE="$2"
            shift 2
            ;;
        --provisioned-tp)
            PROVISIONED_THROUGHPUT="$2"
            shift 2
            ;;
        --create-efs)
            CREATE_EFS="true"
            shift
            ;;
        --cleanup)
            CLEANUP="true"
            shift
            ;;
        --skip-csi-install)
            SKIP_CSI_INSTALL="true"
            shift
            ;;
        --dry-run)
            DRY_RUN="true"
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Validate inputs
if [[ "$PERFORMANCE_MODE" != "generalPurpose" && "$PERFORMANCE_MODE" != "maxIO" ]]; then
    log_error "Invalid performance mode: $PERFORMANCE_MODE. Must be 'generalPurpose' or 'maxIO'"
    exit 1
fi

if [[ "$THROUGHPUT_MODE" != "provisioned" && "$THROUGHPUT_MODE" != "burstingThroughput" ]]; then
    log_error "Invalid throughput mode: $THROUGHPUT_MODE. Must be 'provisioned' or 'burstingThroughput'"
    exit 1
fi

# Function to check required tools
check_tools() {
    log_info "Checking required tools..."
    
    local missing_tools=()
    
    if ! command -v aws &> /dev/null; then
        missing_tools+=("aws")
    fi
    
    if ! command -v $KUBECTL &> /dev/null; then
        missing_tools+=("$KUBECTL")
    fi
    
    if ! command -v jq &> /dev/null; then
        missing_tools+=("jq")
    fi
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        exit 1
    fi
    
    log_success "All required tools are available"
}

# Function to auto-detect cluster name
detect_cluster_name() {
    if [[ -z "$CLUSTER_NAME" ]]; then
        log_info "Auto-detecting cluster name..."
        
        # Get cluster name from context
        CLUSTER_NAME=$($KUBECTL config current-context 2>/dev/null | cut -d'/' -f2 2>/dev/null || echo "")
        
        # Fallback: try to get from cluster infrastructure
        if [[ -z "$CLUSTER_NAME" ]]; then
            CLUSTER_NAME=$($KUBECTL get infrastructure cluster -o jsonpath='{.status.infrastructureName}' 2>/dev/null || echo "")
        fi
        
        if [[ -n "$CLUSTER_NAME" ]]; then
            log_info "Detected cluster name: $CLUSTER_NAME"
        else
            log_error "Could not auto-detect cluster name. Please specify with --cluster-name"
            exit 1
        fi
    fi
}

# Function to auto-detect AWS region
detect_aws_region() {
    if [[ -n "$AWS_REGION" ]]; then
        log_info "Using specified AWS region: $AWS_REGION"
        return
    fi
    
    log_info "Auto-detecting AWS region..."
    
    # Try to detect region from cluster nodes
    local detected_region=""
    local node_names
    node_names=$($KUBECTL get nodes -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    if [[ -n "$node_names" ]]; then
        for node in $node_names; do
            if [[ "$node" =~ \.([^.]*-(east|west|north|south|southeast|northeast|central)-[0-9]+)\.compute\.internal ]]; then
                detected_region="${BASH_REMATCH[1]}"
                log_info "Detected region from node name pattern: $detected_region"
                break
            fi
        done
    fi
    
    # Fallback: try AWS CLI default region
    if [[ -z "$detected_region" ]]; then
        detected_region=$(aws configure get region 2>/dev/null || echo "")
    fi
    
    # Fallback: try environment variable
    if [[ -z "$detected_region" ]]; then
        detected_region="$AWS_DEFAULT_REGION"
    fi
    
    if [[ -n "$detected_region" ]]; then
        AWS_REGION="$detected_region"
        log_info "Using AWS region: $AWS_REGION"
    else
        log_error "Could not auto-detect AWS region. Please specify with --region or set AWS_REGION"
        exit 1
    fi
}

# Function to install or verify EFS CSI driver
install_or_verify_efs_csi_driver() {
    log_info "Checking EFS CSI driver installation..."
    
    if $KUBECTL get csidriver efs.csi.aws.com &>/dev/null; then
        log_success "EFS CSI driver is already installed"
        return
    fi
    
    if [[ "$SKIP_CSI_INSTALL" == "true" ]]; then
        log_error "EFS CSI driver not found and automatic installation is disabled"
        log_error "Please install it manually or remove --skip-csi-install flag"
        exit 1
    fi
    
    log_info "EFS CSI driver not found. Installing automatically..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would install EFS CSI driver"
        return
    fi
    
    # Install EFS CSI driver
    if $KUBECTL apply -k 'github.com/kubernetes-sigs/aws-efs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.7' >/dev/null 2>&1; then
        log_success "EFS CSI driver installed successfully"
        
        # Wait for driver to be ready
        log_info "Waiting for EFS CSI driver to be ready..."
        local max_attempts=30
        local attempt=0
        while [[ $attempt -lt $max_attempts ]]; do
            if $KUBECTL get csidriver efs.csi.aws.com &>/dev/null; then
                log_success "EFS CSI driver is ready"
                return
            fi
            sleep 5
            ((attempt++))
        done
        log_warning "EFS CSI driver installation may still be in progress"
    else
        log_error "Failed to install EFS CSI driver. Please install manually:"
        log_error "  oc apply -k 'github.com/kubernetes-sigs/aws-efs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.7'"
        exit 1
    fi
}

# Function to create EFS filesystem
create_efs_filesystem() {
    log_info "Creating EFS filesystem..."
    
    # Check if EFS already exists
    local existing_efs
    existing_efs=$(aws efs describe-file-systems \
        --region "$AWS_REGION" \
        --query "FileSystems[?Tags[?Key=='Name' && Value=='$EFS_NAME']].FileSystemId" \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$existing_efs" && "$existing_efs" != "None" ]]; then
        log_info "EFS filesystem '$EFS_NAME' already exists: $existing_efs"
        echo "$existing_efs"
        return
    fi
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create EFS filesystem: $EFS_NAME"
        echo "fs-dryrun"
        return
    fi
    
    # Create EFS filesystem
    local create_args=(
        --region "$AWS_REGION"
        --performance-mode "$PERFORMANCE_MODE"
        --throughput-mode "$THROUGHPUT_MODE"
    )
    
    if [[ "$THROUGHPUT_MODE" == "provisioned" ]]; then
        create_args+=(--provisioned-throughput-in-mibps "$PROVISIONED_THROUGHPUT")
    fi
    
    local efs_id
    efs_id=$(aws efs create-file-system "${create_args[@]}" --query 'FileSystemId' --output text)
    
    if [[ -z "$efs_id" ]]; then
        log_error "Failed to create EFS filesystem"
        exit 1
    fi
    
    log_success "Created EFS filesystem: $efs_id"
    
    # Wait for EFS to be available
    log_info "Waiting for EFS filesystem to be available..."
    local max_attempts=30
    local attempt=0
    while [[ $attempt -lt $max_attempts ]]; do
        local state
        state=$(aws efs describe-file-systems \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query 'FileSystems[0].LifeCycleState' \
            --output text 2>/dev/null || echo "")
        
        if [[ "$state" == "available" ]]; then
            break
        elif [[ "$state" == "error" ]]; then
            log_error "EFS filesystem creation failed"
            exit 1
        fi
        
        log_info "EFS state: $state, waiting... (attempt $((attempt + 1))/$max_attempts)"
        sleep 10
        ((attempt++))
    done
    
    if [[ $attempt -ge $max_attempts ]]; then
        log_error "Timeout waiting for EFS filesystem to become available"
        exit 1
    fi
    
    # Add tags
    tag_efs_filesystem "$efs_id"
    
    echo "$efs_id"
}

# Function to tag EFS filesystem
tag_efs_filesystem() {
    local efs_id="$1"
    
    log_info "Adding tags to EFS filesystem..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would add tags to EFS filesystem: $efs_id"
        return
    fi
    
    # Add tags (ignore permission errors)
    aws efs create-tags \
        --region "$AWS_REGION" \
        --file-system-id "$efs_id" \
        --tags \
            "Key=Name,Value=$EFS_NAME" \
            "Key=Purpose,Value=sbd-operator-rwx-storage" \
            "Key=Cluster,Value=$CLUSTER_NAME" \
            "Key=kubernetes.io/cluster/${CLUSTER_NAME},Value=owned" \
            "Key=CreatedBy,Value=sbd-operator-script" \
            "Key=CreatedDate,Value=$(date -u +%Y-%m-%d)" \
        >/dev/null 2>&1 || \
        log_warning "Could not add tags to EFS filesystem (permission issue), but filesystem was created successfully"
    
    log_success "Tags added to EFS filesystem"
}

# Function to find EFS filesystem by name
find_efs_by_name() {
    local name="$1"
    
    aws efs describe-file-systems \
        --region "$AWS_REGION" \
        --query "FileSystems[?Tags[?Key=='Name' && Value=='$name']].FileSystemId" \
        --output text 2>/dev/null || echo ""
}

# Function to create StorageClass
create_storage_class() {
    local efs_id="$1"
    
    log_info "Creating StorageClass..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create StorageClass: $STORAGE_CLASS_NAME"
        cat << EOF
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: $STORAGE_CLASS_NAME
  labels:
    storage-type: efs-rwx
    cluster: $CLUSTER_NAME
provisioner: efs.csi.aws.com
parameters:
  provisioningMode: efs-ap
  fileSystemId: $efs_id
  directoryPerms: "0755"
allowVolumeExpansion: true
EOF
        return
    fi
    
    # Create StorageClass
    cat << EOF | $KUBECTL apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: $STORAGE_CLASS_NAME
  labels:
    storage-type: efs-rwx
    cluster: $CLUSTER_NAME
provisioner: efs.csi.aws.com
parameters:
  provisioningMode: efs-ap
  fileSystemId: $efs_id
  directoryPerms: "0755"
allowVolumeExpansion: true
EOF
    
    log_success "Created StorageClass: $STORAGE_CLASS_NAME"
}

# Function to cleanup resources
cleanup_resources() {
    log_warning "Cleaning up EFS and StorageClass resources..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would cleanup all resources"
        return
    fi
    
    # Delete StorageClass
    log_info "Deleting StorageClass..."
    $KUBECTL delete storageclass "$STORAGE_CLASS_NAME" --ignore-not-found=true
    
    # Find and delete EFS filesystem
    local efs_id
    efs_id=$(find_efs_by_name "$EFS_NAME")
    
    if [[ -n "$efs_id" && "$efs_id" != "None" ]]; then
        log_info "Deleting EFS filesystem: $efs_id"
        
        # Check for mount targets and warn if they exist
        local mount_targets
        mount_targets=$(aws efs describe-mount-targets \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query 'MountTargets[].MountTargetId' \
            --output text 2>/dev/null || echo "")
        
        if [[ -n "$mount_targets" && "$mount_targets" != "None" ]]; then
            log_warning "EFS filesystem has mount targets. Deleting them first..."
            for mt_id in $mount_targets; do
                log_info "Deleting mount target: $mt_id"
                aws efs delete-mount-target --region "$AWS_REGION" --mount-target-id "$mt_id" >/dev/null 2>&1 || true
            done
            
            # Wait for mount targets to be deleted
            log_info "Waiting for mount targets to be deleted..."
            sleep 15
        fi
        
        # Delete EFS filesystem
        aws efs delete-file-system --region "$AWS_REGION" --file-system-id "$efs_id" >/dev/null 2>&1 || \
            log_warning "Could not delete EFS filesystem (may have dependencies or permission issues)"
        
        log_success "EFS filesystem deletion initiated"
    else
        log_info "No EFS filesystem found with name: $EFS_NAME"
    fi
    
    log_success "Cleanup completed"
}

# Function to display summary
show_summary() {
    local efs_id="$1"
    
    log_success "EFS StorageClass setup completed!"
    echo
    echo "📋 Summary:"
    echo "  StorageClass Name: $STORAGE_CLASS_NAME"
    echo "  EFS Filesystem ID: $efs_id"
    echo "  EFS Name: $EFS_NAME"
    echo "  Cluster: $CLUSTER_NAME"
    echo "  Region: $AWS_REGION"
    echo "  Access Mode: ReadWriteMany (RWX)"
    echo
    echo "🚀 Usage in PVCs:"
    echo "  apiVersion: v1"
    echo "  kind: PersistentVolumeClaim"
    echo "  metadata:"
    echo "    name: sbd-shared-storage"
    echo "  spec:"
    echo "    accessModes:"
    echo "    - ReadWriteMany"
    echo "    storageClassName: $STORAGE_CLASS_NAME"
    echo "    resources:"
    echo "      requests:"
    echo "        storage: 10Gi"
    echo
    echo "🔍 Verify with:"
    echo "  $KUBECTL get storageclass $STORAGE_CLASS_NAME"
    echo
    echo "🗑️  Cleanup with:"
    echo "  ./scripts/setup-shared-storage.sh --cleanup --efs-name $EFS_NAME"
}

# Main execution
main() {
    log_info "Starting EFS StorageClass management for OpenShift"
    
    # Check tools
    check_tools
    
    # Auto-detect cluster and region
    detect_cluster_name
    detect_aws_region
    
    # Handle cleanup
    if [[ "$CLEANUP" == "true" ]]; then
        cleanup_resources
        exit 0
    fi
    
    # Install or verify EFS CSI driver
    install_or_verify_efs_csi_driver
    
    # Determine EFS filesystem ID
    local efs_id=""
    if [[ "$CREATE_EFS" == "true" ]]; then
        efs_id=$(create_efs_filesystem)
    else
        if [[ -n "$EFS_FILESYSTEM_ID" ]]; then
            efs_id="$EFS_FILESYSTEM_ID"
            log_info "Using specified EFS filesystem: $efs_id"
        else
            log_error "EFS filesystem ID is required when not creating new EFS"
            show_usage
            exit 1
        fi
    fi
    
    # Create StorageClass
    create_storage_class "$efs_id"
    
    # Show summary
    if [[ "$DRY_RUN" != "true" ]]; then
        show_summary "$efs_id"
    else
        log_info "[DRY RUN] All operations completed successfully (no actual resources created)"
    fi
}

# Run main function
main "$@" 
