/*
Copyright 2025 The Kubernetes Authors.

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

package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cosiapi "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
	cosiproto "sigs.k8s.io/container-object-storage-interface/proto"
)

// BucketReconciler reconciles a Bucket object
type BucketReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	DriverInfo DriverInfo
}

// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=buckets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=buckets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx, "driverName", r.DriverInfo.name)

	bucket := &cosiapi.Bucket{}
	if err := r.Get(ctx, req.NamespacedName, bucket); err != nil {
		if kerrors.IsNotFound(err) {
			logger.V(1).Info("not reconciling nonexistent Bucket")
			return ctrl.Result{}, nil
		}
		// no resource to add status to or report an event for
		logger.Error(err, "failed to get Bucket")
		return ctrl.Result{}, err
	}

	retryError, err := r.reconcile(ctx, logger, bucket)
	if err != nil {
		// Record any error as a timestamped error in the status.
		bucket.Status.Error = cosiapi.NewTimestampedError(time.Now(), err.Error())
		if updErr := r.Status().Update(ctx, bucket); updErr != nil {
			logger.Error(err, "failed to update Bucket status after reconcile error", "updateError", updErr)
			// If status update fails, we must retry the error regardless of the reconcile return.
			// The reconcile needs to run again to make sure the status is eventually be updated.
			return reconcile.Result{}, err
		}

		if !retryError {
			return reconcile.Result{}, reconcile.TerminalError(err)
		}
		return reconcile.Result{}, err
	}

	// On success, clear any errors in the status.
	if bucket.Status.Error != nil {
		bucket.Status.Error = nil
		if err := r.Status().Update(ctx, bucket); err != nil {
			logger.Error(err, "failed to update BucketClaim status after reconcile success")
			// Retry the reconcile so status can be updated eventually.
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cosiapi.Bucket{}).
		Named("bucket").
		Complete(r)
}

// Type definition enforces readability. true/false in code is hard to read/review/maintain.
type retryErrorType bool

const (
	RetryError      retryErrorType = true  // DO retry the error.
	DoNotRetryError retryErrorType = false // Do NOT retry the error.
	NoError         retryErrorType = false // No error, don't retry. For readability.
)

func (r *BucketReconciler) reconcile(
	ctx context.Context, logger logr.Logger, bucket *cosiapi.Bucket,
) (retryErrorType, error) {
	// TODO: verify bucket drivername
	// TODO: verify requested protocols match driver support

	resp, err := r.DriverInfo.provisionerClient.DriverCreateBucket(ctx,
		&cosiproto.DriverCreateBucketRequest{
			Name:       bucket.Name,
			Parameters: bucket.Spec.Parameters,
		},
	)
	if err != nil {
		// TODO: determine error code to determine if should retry
		return DoNotRetryError, err
	}

	if resp.Protocols == nil {
		logger.Error(err, "driver reported no supported protocols for bucket")
		return DoNotRetryError, fmt.Errorf("driver reported no supported protocols bucket")
	}

	s3 := resp.Protocols.S3
	if s3 == nil {
		logger.V(1).Info("s3 nil")
	}

	return NoError, nil
}
