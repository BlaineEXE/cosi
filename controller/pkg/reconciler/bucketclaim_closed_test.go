/*
Copyright 2026 The Kubernetes Authors.

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

package reconciler_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cosiapi "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
	controller "sigs.k8s.io/container-object-storage-interface/controller/pkg/reconciler"
	cositest "sigs.k8s.io/container-object-storage-interface/internal/test"
)

var (
	// valid claim used by dynamic provisioning tests
	baseDynamicClaim = cosiapi.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket",
			Namespace: "my-ns",
			UID:       "dynamicuid",
		},
		Spec: cosiapi.BucketClaimSpec{
			BucketClassName: "s3-class",
			Protocols: []cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
			},
		},
	}

	// valid class compatible with dynamic claim above
	baseClass = cosiapi.BucketClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "s3-class",
		},
		Spec: cosiapi.BucketClassSpec{
			DriverName:     "cosi.s3.internal",
			DeletionPolicy: cosiapi.BucketDeletionPolicyDelete,
			Parameters: map[string]string{
				"maxSize": "100Gi",
				"maxIops": "10",
			},
		},
	}

	// valid claim used by static provisioning tests
	baseStaticClaim = cosiapi.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-bucket", // same name as dynamic so Get() can be reused between tests
			Namespace: "my-ns",
			UID:       "staticuid",
		},
		Spec: cosiapi.BucketClaimSpec{
			ExistingBucketName: "static-bucket",
			Protocols: []cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
			},
		},
	}

	// valid static bucket compatible with static claim above
	baseStaticBucket = cosiapi.Bucket{
		ObjectMeta: metav1.ObjectMeta{
			Name: "static-bucket",
		},
		Spec: cosiapi.BucketSpec{
			DriverName:     cositest.OpinionatedS3BucketClass().Spec.DriverName,
			DeletionPolicy: cosiapi.BucketDeletionPolicyRetain,
			BucketClaimRef: cosiapi.BucketClaimReference{
				Name:      "my-bucket",
				Namespace: "my-ns",
				UID:       "staticuid",
			},
		},
	}
)

func getAllResources(
	bootstrapped *cositest.Dependencies,
) (
	claim *cosiapi.BucketClaim,
	dynamicBucket *cosiapi.Bucket,
	staticBucket *cosiapi.Bucket,
) {
	ctx := bootstrapped.ContextWithLogger
	client := bootstrapped.Client
	var err error

	claim = &cosiapi.BucketClaim{}
	err = client.Get(ctx, cositest.NsName(&baseDynamicClaim), claim)
	if err != nil {
		claim = nil
	}
	dynamicBucket = &cosiapi.Bucket{}
	err = client.Get(ctx, types.NamespacedName{Name: "bc-dynamicuid"}, dynamicBucket)
	if err != nil {
		dynamicBucket = nil
	}
	staticBucket = &cosiapi.Bucket{}
	err = client.Get(ctx, cositest.NsName(&baseStaticBucket), staticBucket)
	if err != nil {
		staticBucket = nil
	}

	return claim, dynamicBucket, staticBucket
}

// Test dynamic provisioning initialization, using base class and claim
func dynamicInitializationTest(t *testing.T) (
	bootstrappedDeps *cositest.Dependencies,
	reconciler *controller.BucketClaimReconciler,
) {
	bootstrapped := cositest.MustBootstrap(t,
		baseDynamicClaim.DeepCopy(),
		baseClass.DeepCopy(),
	)
	r := controller.BucketClaimReconciler{
		Client: bootstrapped.Client,
		Scheme: bootstrapped.Client.Scheme(),
	}
	ctx := bootstrapped.ContextWithLogger

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseDynamicClaim)})
	assert.Error(t, err) // TODO: should be NoError when Bucket watcher is set up
	assert.NotErrorIs(t, err, reconcile.TerminalError(nil))
	assert.Empty(t, res)

	claim, bucket, _ := getAllResources(bootstrapped)

	assert.Contains(t, claim.GetFinalizers(), cosiapi.ProtectionFinalizer)
	status := claim.Status
	assert.Equal(t, "bc-dynamicuid", status.BoundBucketName)
	assert.Equal(t, false, *status.ReadyToUse)
	assert.Empty(t, status.Protocols)
	require.NotNil(t, status.Error)
	assert.NotNil(t, status.Error.Time)
	assert.Contains(t, *status.Error.Message, "waiting for Bucket to be provisioned")

	// intermediate bucket generation is already thoroughly tested elsewhere
	// just test a couple basic fields to ensure it's integrated
	assert.NotContains(t, bucket.GetFinalizers(), cosiapi.ProtectionFinalizer)
	assert.Equal(t, baseClass.Spec.DriverName, bucket.Spec.DriverName)
	assert.Equal(t, baseDynamicClaim.Spec.Protocols, bucket.Spec.Protocols)
	assert.Empty(t, bucket.Status)

	return bootstrapped, &r
}

// Test static provisioning initialization, using base static claim and bucket
func staticInitializationTest(
	t *testing.T,
	presetBcUid bool, // whether to "preset" the BucketClaimRef UID to that of the claim
) (
	bootstrappedDeps *cositest.Dependencies,
	reconciler *controller.BucketClaimReconciler,
) {
	initBucket := baseStaticBucket.DeepCopy()
	if presetBcUid {
		initBucket.Spec.BucketClaimRef.UID = baseStaticClaim.UID
	} else {
		initBucket.Spec.BucketClaimRef.UID = ""
	}
	bootstrapped := cositest.MustBootstrap(t,
		baseStaticClaim.DeepCopy(),
		initBucket,
	)
	r := controller.BucketClaimReconciler{
		Client: bootstrapped.Client,
		Scheme: bootstrapped.Client.Scheme(),
	}
	ctx := bootstrapped.ContextWithLogger

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseStaticClaim)})
	assert.Error(t, err) // TODO: should be NoError when Bucket watcher is set up
	assert.NotErrorIs(t, err, reconcile.TerminalError(nil))
	assert.Empty(t, res)

	claim, dynamicBucket, staticBucket := getAllResources(bootstrapped)

	assert.Contains(t, claim.GetFinalizers(), cosiapi.ProtectionFinalizer)
	assert.Equal(t, "static-bucket", claim.Status.BoundBucketName)
	assert.Equal(t, false, *claim.Status.ReadyToUse)
	assert.Empty(t, claim.Status.Protocols)
	// claim status should record waiting state
	require.NotNil(t, claim.Status.Error)
	assert.Contains(t, *claim.Status.Error.Message, "waiting for Bucket to be provisioned")

	// Bucket claim ref UID must match claim UID
	// set either by controller or by admin
	assert.Equal(t, "staticuid", string(staticBucket.Spec.BucketClaimRef.UID))

	{ // bucket should be otherwise unchanged since it's statically provisioned
		initWithRefUid := initBucket.DeepCopy()
		initWithRefUid.Spec.BucketClaimRef.UID = "staticuid"
		assert.Equal(t, initWithRefUid.ObjectMeta.Finalizers, staticBucket.ObjectMeta.Finalizers)
		assert.Equal(t, initWithRefUid.Spec, staticBucket.Spec)
		assert.Equal(t, initWithRefUid.Status, staticBucket.Status)
	}

	assert.Nil(t, dynamicBucket) // dynamic bucket should not exist

	return bootstrapped, &r
}

// Test BucketClaim initialization.
// Do not test completion that happens after the Bucket is provisioned -- test that below.
func TestBucketClaim_Initialization(t *testing.T) {
	t.Run("successful initialization", func(t *testing.T) {
		type testDef struct {
			name             string
			testInitialFunc  func(t *testing.T) (*cositest.Dependencies, *controller.BucketClaimReconciler)
			getResourcesFunc func(*cositest.Dependencies) (claim *cosiapi.BucketClaim, bucket *cosiapi.Bucket)
		}
		tests := []testDef{
			{
				name:            "dynamic provisioning",
				testInitialFunc: dynamicInitializationTest,
				getResourcesFunc: func(deps *cositest.Dependencies) (*cosiapi.BucketClaim, *cosiapi.Bucket) {
					claim, bucket, _ := getAllResources(deps)
					return claim, bucket
				},
			},
			{
				name: "static provisioning (preset UID)",
				testInitialFunc: func(t *testing.T) (*cositest.Dependencies, *controller.BucketClaimReconciler) {
					return staticInitializationTest(t, true)
				},
				getResourcesFunc: func(deps *cositest.Dependencies) (*cosiapi.BucketClaim, *cosiapi.Bucket) {
					claim, _, bucket := getAllResources(deps)
					return claim, bucket
				},
			},
			{
				name: "static provisioning (no preset UID)",
				testInitialFunc: func(t *testing.T) (*cositest.Dependencies, *controller.BucketClaimReconciler) {
					return staticInitializationTest(t, false)
				},
				getResourcesFunc: func(deps *cositest.Dependencies) (*cosiapi.BucketClaim, *cosiapi.Bucket) {
					claim, _, bucket := getAllResources(deps)
					return claim, bucket
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				t.Run("first reconcile", func(t *testing.T) {
					test.testInitialFunc(t)
				})

				t.Run("subsequent reconcile", func(t *testing.T) {
					bootstrapped, r := test.testInitialFunc(t)
					ctx := bootstrapped.ContextWithLogger

					initClaim, initBucket := test.getResourcesFunc(bootstrapped)

					res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseDynamicClaim)})
					assert.Error(t, err) // TODO: should be NoError when Bucket watcher is set up
					assert.NotErrorIs(t, err, reconcile.TerminalError(nil))
					assert.Empty(t, res)

					claim, bucket := test.getResourcesFunc(bootstrapped)

					// claim should be unchanged since bucket is not ready
					assert.Equal(t, initClaim.Finalizers, claim.Finalizers)
					assert.Equal(t, initClaim.Spec, claim.Spec)
					assert.Equal(t, initClaim.Status, claim.Status)

					assert.Equal(t, initBucket.Spec, bucket.Spec)
					assert.Equal(t, initBucket.Status, bucket.Status)
				})

				t.Run("bucket deleted after init", func(t *testing.T) {
					// note: this is also equivalent to the BucketClaim `boundBucketName` not
					// matching the previous/intended bucket.

					bootstrapped, r := test.testInitialFunc(t)
					ctx := bootstrapped.ContextWithLogger

					initClaim, initBucket := test.getResourcesFunc(bootstrapped)
					require.NoError(t, r.Delete(ctx, initBucket)) // simulate force-delete of bucket while claim is bound

					res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseDynamicClaim)})
					assert.Error(t, err) // TODO: should be NoError when Bucket watcher is set up
					assert.ErrorIs(t, err, reconcile.TerminalError(nil))
					assert.ErrorContains(t, err, "unrecoverable degradation")
					assert.ErrorContains(t, err, "no longer exists")
					assert.Empty(t, res)

					claim, _ := test.getResourcesFunc(bootstrapped)
					assert.Contains(t, claim.GetFinalizers(), cosiapi.ProtectionFinalizer)
					assert.Equal(t, initClaim.Spec, claim.Spec)
					assert.False(t, *claim.Status.ReadyToUse)
					assert.Equal(t, initClaim.Status.BoundBucketName, claim.Status.BoundBucketName) // still bound to missing bucket
					assert.Equal(t, initClaim.Status.Protocols, claim.Status.Protocols)
					require.NotNil(t, claim.Status.Error)
					assert.Contains(t, *claim.Status.Error.Message, "unrecoverable degradation")

					buckets := &cosiapi.BucketList{}
					require.NoError(t, r.List(ctx, buckets))
					assert.Len(t, buckets.Items, 0) // no buckets should be created when claim is degraded
				})
			})
		}
	})

	t.Run("dynamic provisioning", func(t *testing.T) {
		t.Run("err no bucketclass", func(t *testing.T) {
			bootstrapped := cositest.MustBootstrap(t,
				baseDynamicClaim.DeepCopy(),
				// no bucketclass
			)
			r := controller.BucketClaimReconciler{
				Client: bootstrapped.Client,
				Scheme: bootstrapped.Client.Scheme(),
			}
			ctx := bootstrapped.ContextWithLogger

			res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseDynamicClaim)})
			assert.Error(t, err)
			assert.NotErrorIs(t, err, reconcile.TerminalError(nil)) // should be terminal error when bucketclass watcher is set up
			assert.Empty(t, res)

			claim, _, _ := getAllResources(bootstrapped)

			assert.Contains(t, claim.GetFinalizers(), cosiapi.ProtectionFinalizer)
			status := claim.Status
			assert.Empty(t, status.BoundBucketName)
			assert.Equal(t, false, *status.ReadyToUse)
			assert.Empty(t, status.Protocols)
			serr := status.Error
			require.NotNil(t, serr)
			assert.NotNil(t, serr.Time)
			assert.NotNil(t, serr.Message)
			assert.Contains(t, *serr.Message, baseClass.Name)

			bootstrapped.AssertResourceDoesNotExist(t, types.NamespacedName{Name: "bc-dynamicuid"}, &cosiapi.Bucket{})
		})
	})

	t.Run("static provisioning", func(t *testing.T) {
		t.Run("bucket does not exist", func(t *testing.T) {
			bootstrapped := cositest.MustBootstrap(t,
				baseStaticClaim.DeepCopy(),
				// no bucket
			)
			r := controller.BucketClaimReconciler{
				Client: bootstrapped.Client,
				Scheme: bootstrapped.Client.Scheme(),
			}
			ctx := bootstrapped.ContextWithLogger

			res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseStaticClaim)})
			assert.Error(t, err)
			assert.NotErrorIs(t, err, reconcile.TerminalError(nil))
			assert.ErrorContains(t, err, "waiting for statically-provisioned Bucket")
			assert.ErrorContains(t, err, "static-bucket")
			assert.Empty(t, res)

			claim, _, _ := getAllResources(bootstrapped)

			assert.Contains(t, claim.GetFinalizers(), cosiapi.ProtectionFinalizer)
			assert.Equal(t, baseStaticClaim.Spec, claim.Spec)
			require.NotNil(t, claim.Status.Error)
			assert.NotNil(t, claim.Status.Error.Time)
			assert.NotNil(t, claim.Status.Error.Message)
			assert.Contains(t, *claim.Status.Error.Message, "waiting for statically-provisioned Bucket")
			assert.Contains(t, *claim.Status.Error.Message, "static-bucket")
			assert.False(t, ptr.Deref(claim.Status.ReadyToUse, true))
			assert.Empty(t, claim.Status.BoundBucketName)
			assert.Empty(t, claim.Status.Protocols)

			buckets := &cosiapi.BucketList{}
			assert.NoError(t, r.List(ctx, buckets))
			assert.Len(t, buckets.Items, 0) // no bucket should be created
		})

		t.Run("bucket bucketClaimRef mismatch", func(t *testing.T) {
			type bucketClaimRefMismatchTest struct {
				testName      string
				bucketRef     cosiapi.BucketClaimReference
				errorContains []string
			}
			tests := []bucketClaimRefMismatchTest{
				{
					"namespace mismatch",
					cosiapi.BucketClaimReference{
						Namespace: "other-namespace",
						Name:      baseStaticClaim.Name,
						UID:       "",
					},
					[]string{"namespace", "other-namespace"},
				},
				{
					"name mismatch",
					cosiapi.BucketClaimReference{
						Namespace: baseStaticClaim.Namespace,
						Name:      "other-claim-name",
						UID:       "",
					},
					[]string{"name", "other-claim-name"},
				},
				{
					"UID mismatch", // already bound to different claim
					cosiapi.BucketClaimReference{
						Namespace: baseStaticClaim.Namespace,
						Name:      baseStaticClaim.Name,
						UID:       types.UID("other-uid"),
					},
					[]string{"Bucket claim ref UID", "other-uid"},
				},
			}
			for _, tt := range tests {
				t.Run(tt.testName, func(t *testing.T) {
					bucketNoMatch := baseStaticBucket.DeepCopy()
					bucketNoMatch.Spec.BucketClaimRef = tt.bucketRef
					bootstrapped := cositest.MustBootstrap(t,
						baseStaticClaim.DeepCopy(),
						bucketNoMatch,
					)
					r := controller.BucketClaimReconciler{
						Client: bootstrapped.Client,
						Scheme: bootstrapped.Client.Scheme(),
					}
					ctx := bootstrapped.ContextWithLogger

					res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseStaticClaim)})
					assert.Error(t, err)
					assert.ErrorIs(t, err, reconcile.TerminalError(nil))
					for _, s := range tt.errorContains {
						assert.ErrorContains(t, err, s)
					}
					assert.Empty(t, res)

					claim, dynamicBucket, staticBucket := getAllResources(bootstrapped)

					assert.Contains(t, claim.GetFinalizers(), cosiapi.ProtectionFinalizer)
					assert.Equal(t, baseStaticClaim.Spec, claim.Spec)
					require.NotNil(t, claim.Status.Error)
					assert.NotNil(t, claim.Status.Error.Time)
					assert.NotNil(t, claim.Status.Error.Message)
					for _, s := range tt.errorContains {
						assert.Contains(t, *claim.Status.Error.Message, s)
					}
					assert.False(t, ptr.Deref(claim.Status.ReadyToUse, true))
					assert.Empty(t, claim.Status.BoundBucketName)
					assert.Empty(t, claim.Status.Protocols)

					assert.Equal(t, bucketNoMatch, staticBucket)

					assert.Nil(t, dynamicBucket) // no dynamic bucket
				})
			}
		})
	})
}

// Test BucketClaim completion (after Bucket has been provisioned) separately from initialization.
// These tests are separated because they rely on Bucket reconciliation results and represent more
// of an integration between the Bucket reconciler and BucketClaim completion.
func TestBucketClaim_Completion(t *testing.T) {
	t.Run("dynamic provisioning", func(t *testing.T) {
		// t.Run("success", func(t *testing.T) {
		// 	dynamicInitializationTest(t)
		// })

		// t.Run("reconcile after success", func(t *testing.T) {
		// 	bootstrapped, r := dynamicInitializationTest(t)
		// 	ctx := bootstrapped.ContextWithLogger

		// 	initClaim, initBucket, _ := getAllResources(bootstrapped)

		// 	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: cositest.NsName(&baseDynamicClaim)})
		// 	assert.Error(t, err) // TODO: should be NoError when Bucket watcher is set up
		// 	assert.NotErrorIs(t, err, reconcile.TerminalError(nil))
		// 	assert.Empty(t, res)

		// 	claim, bucket, _ := getAllResources(bootstrapped)

		// 	// claim should be unchanged since bucket is not ready
		// 	assert.Equal(t, initClaim.Finalizers, claim.Finalizers)
		// 	assert.Equal(t, initClaim.Spec, claim.Spec)
		// 	assert.Equal(t, initClaim.Status, claim.Status)

		// 	assert.Equal(t, initBucket, bucket)
		// })
	})
}
