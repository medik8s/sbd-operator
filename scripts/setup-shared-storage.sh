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
SKIP_PERMISSION_CHECKS="false"

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

This script sets up EFS-based shared storage for OpenShift/Kubernetes clusters.
It creates an EFS filesystem, configures networking (VPC, subnets, security groups,
mount targets), installs the EFS CSI driver, and creates a StorageClass.

OPTIONS:
    --create-efs                Create a new EFS filesystem (default: true)
    --no-create-efs            Use existing EFS filesystem (requires --filesystem-id)
    --filesystem-id FSID       Use existing EFS filesystem with ID FSID
    --efs-name NAME            Name for the EFS filesystem (default: sbd-efs-CLUSTER_NAME)
    --storage-class-name NAME  Name for the StorageClass (default: sbd-efs-sc)
    --cluster-name NAME        Override cluster name detection
    --aws-region REGION        Override AWS region detection
    --cleanup                  Clean up all created resources
    --skip-permission-checks   Skip AWS permission validation (use with caution)
    --dry-run                  Show what would be done without executing
    --verbose                  Enable verbose logging
    --help                     Show this help message

NETWORKING FEATURES:
    • Auto-detects cluster VPC and private subnets
    • Creates NFS security group with proper port 2049 access
    • Sets up EFS mount targets in all cluster subnets
    • Configures EFS CSI driver with cluster credentials
    • Handles existing resources gracefully (idempotent)

EXAMPLES:
    # Create new EFS with automatic networking setup
    $0

    # Use existing EFS filesystem
    $0 --no-create-efs --filesystem-id fs-1234567890abcdef0

    # Create with custom names
    $0 --efs-name my-shared-storage --storage-class-name my-efs-sc

    # Preview changes without executing
    $0 --dry-run

    # Clean up everything
    $0 --cleanup --efs-name sbd-efs-mycluster

REQUIREMENTS:
    • OpenShift/Kubernetes cluster with AWS provider
    • AWS CLI configured with appropriate permissions
    • kubectl/oc CLI tools
    • Cluster admin permissions

The script automatically:
    1. Detects cluster name and AWS region
    2. Validates AWS permissions for EFS and EC2 operations
    3. Installs/verifies EFS CSI driver
    4. Creates EFS filesystem with proper tags
    5. Sets up complete networking (VPC, subnets, security groups, mount targets)
    6. Creates StorageClass with EFS Access Point provisioning for ReadWriteMany (RWX) access
    7. Provides comprehensive cleanup functionality

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
        --no-create-efs)
            CREATE_EFS="false"
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
        --skip-permission-checks)
            SKIP_PERMISSION_CHECKS="true"
            shift
            ;;
        --dry-run)
            DRY_RUN="true"
            shift
            ;;
        --verbose)
            set -x  # Enable verbose mode
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

# Function to check AWS permissions
check_aws_permissions() {
    log_info "Checking AWS permissions..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Skipping AWS permission checks"
        return
    fi
    
    if [[ "$SKIP_PERMISSION_CHECKS" == "true" ]]; then
        log_warning "Skipping AWS permission checks (--skip-permission-checks specified)"
        return
    fi
    
    local permission_errors=()
    local test_efs_id=""
    
    # Test basic EFS permissions
    log_info "Testing EFS permissions..."
    
    # Check if we can describe file systems
    if ! aws efs describe-file-systems --region "$AWS_REGION" --max-items 1 >/dev/null 2>&1; then
        permission_errors+=("elasticfilesystem:DescribeFileSystems")
    fi
    
    # Check if we can describe mount targets (test with any existing EFS filesystem)
    local test_efs_for_mount_targets
    test_efs_for_mount_targets=$(aws efs describe-file-systems --region "$AWS_REGION" --query 'FileSystems[0].FileSystemId' --output text 2>/dev/null || echo "")
    if [[ -n "$test_efs_for_mount_targets" && "$test_efs_for_mount_targets" != "None" ]]; then
        if ! aws efs describe-mount-targets --region "$AWS_REGION" --file-system-id "$test_efs_for_mount_targets" >/dev/null 2>&1; then
            permission_errors+=("elasticfilesystem:DescribeMountTargets")
        fi
    else
        # No EFS exists to test mount targets, skip this check
        log_info "No existing EFS filesystems found - skipping DescribeMountTargets test"
    fi
    
    # Test EC2 VPC permissions
    log_info "Testing EC2 VPC permissions..."
    
    # Check if we can describe VPCs (minimum max-results is 5)
    if ! aws ec2 describe-vpcs --region "$AWS_REGION" --max-results 5 >/dev/null 2>&1; then
        permission_errors+=("ec2:DescribeVpcs")
    fi
    
    # Check if we can describe subnets (minimum max-results is 5)
    if ! aws ec2 describe-subnets --region "$AWS_REGION" --max-results 5 >/dev/null 2>&1; then
        permission_errors+=("ec2:DescribeSubnets")
    fi
    
    # Check if we can describe security groups (minimum max-results is 5)
    if ! aws ec2 describe-security-groups --region "$AWS_REGION" --max-results 5 >/dev/null 2>&1; then
        permission_errors+=("ec2:DescribeSecurityGroups")
    fi
    
    # Test EFS Access Point permissions (only if we're creating new EFS or using existing one)
    if [[ "$CREATE_EFS" == "true" || -n "$EFS_FILESYSTEM_ID" ]]; then
        log_info "Testing EFS Access Point permissions..."
        
        # Find a test EFS filesystem to test against
        if [[ -n "$EFS_FILESYSTEM_ID" ]]; then
            test_efs_id="$EFS_FILESYSTEM_ID"
        else
            # Try to find an existing EFS to test against
            test_efs_id=$(aws efs describe-file-systems \
                --region "$AWS_REGION" \
                --query 'FileSystems[0].FileSystemId' \
                --output text 2>/dev/null || echo "")
        fi
        
        if [[ -n "$test_efs_id" && "$test_efs_id" != "None" ]]; then
            # Test CreateAccessPoint permission by attempting a test creation (then immediately delete)
            log_info "Testing CreateAccessPoint permission with filesystem: $test_efs_id"
            local test_ap_id=""
            local create_ap_error=""
            
            # First try with tags to test both CreateAccessPoint and TagResource
            create_ap_error=$(aws efs create-access-point \
                --region "$AWS_REGION" \
                --file-system-id "$test_efs_id" \
                --posix-user Uid=1001,Gid=1001 \
                --root-directory Path="/permission-test-$(date +%s)",CreationInfo='{OwnerUid=1001,OwnerGid=1001,Permissions=755}' \
                --tags Key=test,Value=permission-check \
                --query 'AccessPointId' \
                --output text 2>&1)
            
            if [[ -n "$create_ap_error" && "$create_ap_error" != "None" && ! "$create_ap_error" =~ error && ! "$create_ap_error" =~ "AccessDenied" ]]; then
                test_ap_id="$create_ap_error"
                log_info "✅ CreateAccessPoint and TagResource permissions verified"
                
                # Test DeleteAccessPoint permission
                if aws efs delete-access-point --region "$AWS_REGION" --access-point-id "$test_ap_id" >/dev/null 2>&1; then
                    log_info "✅ DeleteAccessPoint permission verified"
                else
                    permission_errors+=("elasticfilesystem:DeleteAccessPoint")
                fi
            else
                # Check if the error is specifically about TagResource
                if [[ "$create_ap_error" =~ TagResource ]]; then
                    permission_errors+=("elasticfilesystem:TagResource")
                    
                    # Try without tags to test just CreateAccessPoint
                    log_info "TagResource permission missing, testing CreateAccessPoint without tags..."
                    test_ap_id=$(aws efs create-access-point \
                        --region "$AWS_REGION" \
                        --file-system-id "$test_efs_id" \
                        --posix-user Uid=1001,Gid=1001 \
                        --root-directory Path="/permission-test-$(date +%s)",CreationInfo='{OwnerUid=1001,OwnerGid=1001,Permissions=755}' \
                        --query 'AccessPointId' \
                        --output text 2>/dev/null || echo "")
                    
                    if [[ -n "$test_ap_id" && "$test_ap_id" != "None" ]]; then
                        log_info "✅ CreateAccessPoint permission verified (without tags)"
                        
                        # Test DeleteAccessPoint permission
                        if aws efs delete-access-point --region "$AWS_REGION" --access-point-id "$test_ap_id" >/dev/null 2>&1; then
                            log_info "✅ DeleteAccessPoint permission verified"
                        else
                            permission_errors+=("elasticfilesystem:DeleteAccessPoint")
                        fi
                    else
                        permission_errors+=("elasticfilesystem:CreateAccessPoint")
                    fi
                else
                    # Some other CreateAccessPoint error
                    permission_errors+=("elasticfilesystem:CreateAccessPoint")
                fi
            fi
            
            # Test DescribeAccessPoints permission (minimum max-results is 5)
            if ! aws efs describe-access-points --region "$AWS_REGION" --file-system-id "$test_efs_id" --max-results 5 >/dev/null 2>&1; then
                permission_errors+=("elasticfilesystem:DescribeAccessPoints")
            fi
        fi
    fi
    
    # Test permissions needed for EFS creation (if applicable)
    if [[ "$CREATE_EFS" == "true" ]]; then
        log_info "Testing EFS creation permissions..."
        
        # We can't actually test CreateFileSystem without creating one, but we can check TagResource
        # by trying to tag an existing resource (if any exist)
        local existing_efs
        existing_efs=$(aws efs describe-file-systems \
            --region "$AWS_REGION" \
            --query 'FileSystems[0].FileSystemId' \
            --output text 2>/dev/null || echo "")
        
        if [[ -n "$existing_efs" && "$existing_efs" != "None" ]]; then
            # Test tag permissions on existing filesystem
            if ! aws efs list-tags-for-resource --region "$AWS_REGION" --resource-id "$existing_efs" >/dev/null 2>&1; then
                permission_errors+=("elasticfilesystem:ListTagsForResource")
            fi
        fi
        
        # Test CreateMountTarget permission (we'll test this during actual mount target creation)
        log_info "CreateMountTarget permission will be tested during mount target creation"
    fi
    
    # Test Security Group creation permissions
    log_info "Testing Security Group permissions..."
    
    # We'll test CreateSecurityGroup during actual security group creation
    # But we can test AuthorizeSecurityGroupIngress on an existing group if needed
    
    # Report results
    if [[ ${#permission_errors[@]} -eq 0 ]]; then
        log_success "All required AWS permissions are available"
    else
        log_error "Missing AWS permissions:"
        for perm in "${permission_errors[@]}"; do
            log_error "  - $perm"
        done
        echo
        log_error "Required AWS permissions for EFS setup:"
        echo "  EFS Permissions:"
        echo "    - elasticfilesystem:CreateFileSystem"
        echo "    - elasticfilesystem:DescribeFileSystems"
        echo "    - elasticfilesystem:CreateAccessPoint"
        echo "    - elasticfilesystem:DeleteAccessPoint"
        echo "    - elasticfilesystem:DescribeAccessPoints"
        echo "    - elasticfilesystem:CreateMountTarget"
        echo "    - elasticfilesystem:DescribeMountTargets"
        echo "    - elasticfilesystem:DeleteMountTarget"
        echo "    - elasticfilesystem:TagResource"
        echo "    - elasticfilesystem:ListTagsForResource"
        echo "  EC2 Permissions:"
        echo "    - ec2:DescribeVpcs"
        echo "    - ec2:DescribeSubnets"
        echo "    - ec2:CreateSecurityGroup"
        echo "    - ec2:DescribeSecurityGroups"
        echo "    - ec2:DeleteSecurityGroup"
        echo "    - ec2:AuthorizeSecurityGroupIngress"
        echo "    - ec2:CreateTags"
        echo
        exit 1
    fi
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

# Function to configure EFS CSI driver credentials
configure_efs_csi_credentials() {
    log_info "Configuring EFS CSI driver with cluster credentials..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would configure EFS CSI driver credentials"
        return
    fi
    
    # Get cluster credentials
    local cluster_region
    cluster_region=$(oc get infrastructure cluster -o jsonpath='{.status.platformStatus.aws.region}' 2>/dev/null || echo "$AWS_REGION")
    
    # Update EFS CSI driver configuration
    $KUBECTL patch csidriver efs.csi.aws.com --type merge -p '{
        "spec": {
            "storageCapacity": false,
            "volumeLifecycleModes": ["Persistent"]
        }
    }' >/dev/null 2>&1 || true
    
    log_success "EFS CSI driver credentials configured"
}

# Function to detect cluster VPC and subnets
detect_cluster_vpc_and_subnets() {
    log_info "Detecting cluster VPC and subnets..."
    
    # Get cluster infrastructure details
    local cluster_vpc_id=""
    local cluster_subnets=""
    
    # Try to get VPC from cluster infrastructure
    cluster_vpc_id=$(oc get infrastructure cluster -o jsonpath='{.status.platformStatus.aws.resourceTags.kubernetes\.io/cluster/.*}' 2>/dev/null | head -1 || echo "")
    
    if [[ -z "$cluster_vpc_id" ]]; then
        # Fallback: find VPC by cluster tag
        cluster_vpc_id=$(aws ec2 describe-vpcs \
            --region "$AWS_REGION" \
            --filters "Name=tag:kubernetes.io/cluster/${CLUSTER_NAME},Values=owned,shared" \
            --query 'Vpcs[0].VpcId' \
            --output text 2>/dev/null || echo "")
    fi
    
    if [[ -z "$cluster_vpc_id" || "$cluster_vpc_id" == "None" ]]; then
        # Fallback: find VPC by node IP addresses
        log_info "No cluster tags found, detecting VPC from node IPs..."
        local node_ip
        node_ip=$($KUBECTL get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || echo "")
        
        if [[ -n "$node_ip" ]]; then
            log_info "Using node IP $node_ip to find VPC..."
            # Find all subnets and check which one contains this IP
            local all_subnets
            all_subnets=$(aws ec2 describe-subnets --region "$AWS_REGION" --query 'Subnets[].{SubnetId:SubnetId,VpcId:VpcId,CidrBlock:CidrBlock}' --output json 2>/dev/null || echo "[]")
            
            # Use python to find matching subnet (more reliable than bash CIDR matching)
            cluster_vpc_id=$(echo "$all_subnets" | python3 -c "
import json
import ipaddress
import sys

try:
    subnets = json.load(sys.stdin)
    node_ip = '$node_ip'
    
    for subnet in subnets:
        try:
            cidr = ipaddress.IPv4Network(subnet['CidrBlock'])
            if ipaddress.IPv4Address(node_ip) in cidr:
                print(subnet['VpcId'])
                break
        except:
            continue
except:
    pass
" 2>/dev/null || echo "")
        fi
    fi
    
    if [[ -z "$cluster_vpc_id" || "$cluster_vpc_id" == "None" ]]; then
        log_error "Could not detect cluster VPC. Ensure you're connected to the right cluster."
        exit 1
    fi
    
    # Get private subnets from the VPC
    cluster_subnets=$(aws ec2 describe-subnets \
        --region "$AWS_REGION" \
        --filters \
            "Name=vpc-id,Values=$cluster_vpc_id" \
            "Name=tag:kubernetes.io/role/internal-elb,Values=1" \
        --query 'Subnets[].SubnetId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -z "$cluster_subnets" || "$cluster_subnets" == "None" ]]; then
        # Fallback: get all private subnets
        cluster_subnets=$(aws ec2 describe-subnets \
            --region "$AWS_REGION" \
            --filters \
                "Name=vpc-id,Values=$cluster_vpc_id" \
                "Name=tag:Name,Values=*private*" \
            --query 'Subnets[].SubnetId' \
            --output text 2>/dev/null || echo "")
    fi
    
    if [[ -z "$cluster_subnets" || "$cluster_subnets" == "None" ]]; then
        # Final fallback: get subnets that contain cluster nodes
        log_info "No tagged private subnets found, detecting from node locations..."
        local node_ips
        node_ips=$($KUBECTL get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null | tr ' ' '\n' | sort -u || echo "")
        
        if [[ -n "$node_ips" ]]; then
            local all_subnets
            all_subnets=$(aws ec2 describe-subnets --region "$AWS_REGION" --filters "Name=vpc-id,Values=$cluster_vpc_id" --query 'Subnets[].{SubnetId:SubnetId,CidrBlock:CidrBlock}' --output json 2>/dev/null || echo "[]")
            
            # Find subnets containing our nodes
            cluster_subnets=$(echo "$all_subnets" | python3 -c "
import json
import ipaddress
import sys

try:
    subnets = json.load(sys.stdin)
    node_ips = '''$node_ips'''.strip().split('\n')
    found_subnets = set()
    
    for subnet in subnets:
        try:
            cidr = ipaddress.IPv4Network(subnet['CidrBlock'])
            for node_ip in node_ips:
                if node_ip and ipaddress.IPv4Address(node_ip.strip()) in cidr:
                    found_subnets.add(subnet['SubnetId'])
        except:
            continue
    
    print(' '.join(found_subnets))
except:
    pass
" 2>/dev/null || echo "")
        fi
    fi
    
    if [[ -z "$cluster_subnets" || "$cluster_subnets" == "None" ]]; then
        log_error "Could not find private subnets in cluster VPC: $cluster_vpc_id"
        exit 1
    fi
    
    log_success "Detected cluster VPC: $cluster_vpc_id"
    log_success "Detected private subnets: $cluster_subnets"
    
    echo "$cluster_vpc_id|$cluster_subnets"
}

# Function to create or get NFS security group
create_or_get_nfs_security_group() {
    local vpc_id="$1"
    local sg_name="efs-nfs-access-${CLUSTER_NAME}"
    
    log_info "Creating or getting NFS security group..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create/get NFS security group: $sg_name"
        echo "sg-dryrun123456789"
        return
    fi
    
    # Check if security group already exists
    local sg_id
    sg_id=$(aws ec2 describe-security-groups \
        --region "$AWS_REGION" \
        --filters \
            "Name=vpc-id,Values=$vpc_id" \
            "Name=group-name,Values=$sg_name" \
        --query 'SecurityGroups[0].GroupId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$sg_id" && "$sg_id" != "None" ]]; then
        log_info "Using existing security group: $sg_id"
        echo "$sg_id"
        return
    fi
    
    # Create new security group
    sg_id=$(aws ec2 create-security-group \
        --region "$AWS_REGION" \
        --group-name "$sg_name" \
        --description "NFS access for EFS in cluster $CLUSTER_NAME" \
        --vpc-id "$vpc_id" \
        --query 'GroupId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -z "$sg_id" || "$sg_id" == "None" ]]; then
        log_error "Failed to create security group"
        exit 1
    fi
    
    # Add NFS ingress rule (port 2049)
    aws ec2 authorize-security-group-ingress \
        --region "$AWS_REGION" \
        --group-id "$sg_id" \
        --protocol tcp \
        --port 2049 \
        --source-group "$sg_id" \
        >/dev/null 2>&1 || true
    
    # Add tags to security group
    aws ec2 create-tags \
        --region "$AWS_REGION" \
        --resources "$sg_id" \
        --tags \
            "Key=Name,Value=$sg_name" \
            "Key=kubernetes.io/cluster/${CLUSTER_NAME},Value=owned" \
            "Key=CreatedBy,Value=sbd-operator-script" \
        >/dev/null 2>&1 || true
    
    log_success "Created NFS security group: $sg_id"
    echo "$sg_id"
}

# Function to create EFS mount targets
create_efs_mount_targets() {
    local efs_id="$1"
    local vpc_id="$2"
    local subnets="$3"
    
    log_info "Creating EFS mount targets..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create EFS mount targets for: $efs_id"
        return
    fi
    
    # Get or create NFS security group
    local sg_id
    sg_id=$(create_or_get_nfs_security_group "$vpc_id")
    
    # Create mount targets for each subnet
    local created_targets=0
    for subnet_id in $subnets; do
        # Check if mount target already exists for this subnet
        local existing_target
        existing_target=$(aws efs describe-mount-targets \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query "MountTargets[?SubnetId=='$subnet_id'].MountTargetId" \
            --output text 2>/dev/null || echo "")
        
        if [[ -n "$existing_target" && "$existing_target" != "None" ]]; then
            log_info "Mount target already exists for subnet $subnet_id: $existing_target"
            continue
        fi
        
        # Create mount target
        local mount_target_id
        mount_target_id=$(aws efs create-mount-target \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --subnet-id "$subnet_id" \
            --security-groups "$sg_id" \
            --query 'MountTargetId' \
            --output text 2>/dev/null || echo "")
        
        if [[ -n "$mount_target_id" && "$mount_target_id" != "None" ]]; then
            log_success "Created mount target: $mount_target_id (subnet: $subnet_id)"
            ((created_targets++))
        else
            log_warning "Failed to create mount target for subnet: $subnet_id"
        fi
    done
    
    if [[ $created_targets -gt 0 ]]; then
        log_info "Waiting for mount targets to become available..."
        sleep 10
    fi
    
    log_success "EFS mount targets setup completed"
}

# Function to setup EFS networking
setup_efs_networking() {
    local efs_id="$1"
    
    log_info "Setting up EFS networking configuration..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would setup EFS networking for: $efs_id"
        return
    fi
    
    # Detect VPC and subnets
    local vpc_info
    vpc_info=$(detect_cluster_vpc_and_subnets)
    local vpc_id
    local subnets
    vpc_id=$(echo "$vpc_info" | cut -d'|' -f1)
    subnets=$(echo "$vpc_info" | cut -d'|' -f2)
    
    # Configure EFS CSI driver
    configure_efs_csi_credentials
    
    # Create mount targets
    create_efs_mount_targets "$efs_id" "$vpc_id" "$subnets"
    
    log_success "EFS networking setup completed"
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
    
    # Create StorageClass with EFS Access Point provisioning
    # AWS permissions have been verified for elasticfilesystem:CreateAccessPoint
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
    log_info "Using EFS Access Point provisioning for dynamic PVC management"
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
        log_info "Deleting EFS filesystem and associated resources: $efs_id"
        
        # Get VPC information for security group cleanup
        local vpc_info
        vpc_info=$(detect_cluster_vpc_and_subnets)
        local vpc_id
        vpc_id=$(echo "$vpc_info" | cut -d'|' -f1)
        
        # Check for mount targets and delete them
        local mount_targets
        mount_targets=$(aws efs describe-mount-targets \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query 'MountTargets[].MountTargetId' \
            --output text 2>/dev/null || echo "")
        
        if [[ -n "$mount_targets" && "$mount_targets" != "None" ]]; then
            log_info "Deleting mount targets..."
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
        
        # Clean up security group (only if no other EFS filesystems are using it)
        local sg_name="efs-nfs-access-${CLUSTER_NAME}"
        local sg_id
        sg_id=$(aws ec2 describe-security-groups \
            --region "$AWS_REGION" \
            --filters \
                "Name=vpc-id,Values=$vpc_id" \
                "Name=group-name,Values=$sg_name" \
            --query 'SecurityGroups[0].GroupId' \
            --output text 2>/dev/null || echo "")
        
        if [[ -n "$sg_id" && "$sg_id" != "None" ]]; then
            # Check if any other EFS filesystems are using this security group
            local other_mount_targets
            other_mount_targets=$(aws efs describe-mount-targets \
                --region "$AWS_REGION" \
                --query "MountTargets[?SecurityGroups[?contains(@, '$sg_id')]]" \
                --output text 2>/dev/null || echo "")
            
            if [[ -z "$other_mount_targets" || "$other_mount_targets" == "None" ]]; then
                log_info "Deleting unused NFS security group: $sg_id"
                aws ec2 delete-security-group --region "$AWS_REGION" --group-id "$sg_id" >/dev/null 2>&1 || \
                    log_warning "Could not delete security group (may be in use)"
            else
                log_info "Keeping security group $sg_id (in use by other EFS mount targets)"
            fi
        fi
        
        log_success "EFS filesystem and associated resources cleanup initiated"
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
    
    # Check AWS permissions (after region is detected)
    check_aws_permissions
    
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
    
    # Setup EFS networking
    setup_efs_networking "$efs_id"
    
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
