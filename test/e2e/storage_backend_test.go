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

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Storage Backend Detection", func() {
	Context("When detecting storage backend type", func() {
		It("should detect Ceph storage correctly", func() {
			Skip("Unit test for storage backend detection - requires StorageClass creation")
			
			// This test would create test StorageClasses and validate detection
			// In actual e2e runs, this logic is tested as part of storage disruption tests
			
			// Test CephFS detection
			cephStorageClass := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cephfs",
				},
				Provisioner: "cephfs.csi.ceph.com",
			}
			
			err := k8sClient.Create(ctx, cephStorageClass)
			Expect(err).NotTo(HaveOccurred())
			
			defer func() {
				err := k8sClient.Delete(ctx, cephStorageClass)
				Expect(err).NotTo(HaveOccurred())
			}()
			
			// Test detection
			backend, className, err := detectStorageBackend()
			Expect(err).NotTo(HaveOccurred())
			Expect(backend).To(Equal(StorageBackendCeph))
			Expect(className).To(Equal("test-cephfs"))
		})
		
		It("should detect AWS storage correctly", func() {
			Skip("Unit test for storage backend detection - requires StorageClass creation")
			
			// Test AWS EFS detection
			awsStorageClass := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-efs",
				},
				Provisioner: "efs.csi.aws.com",
			}
			
			err := k8sClient.Create(ctx, awsStorageClass)
			Expect(err).NotTo(HaveOccurred())
			
			defer func() {
				err := k8sClient.Delete(ctx, awsStorageClass)
				Expect(err).NotTo(HaveOccurred())
			}()
			
			// Test detection
			backend, className, err := detectStorageBackend()
			Expect(err).NotTo(HaveOccurred())
			Expect(backend).To(Equal(StorageBackendAWS))
			Expect(className).To(Equal("test-efs"))
		})
	})
}) 