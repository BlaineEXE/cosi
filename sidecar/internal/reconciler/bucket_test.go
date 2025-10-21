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
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cosiapi "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
	cosiproto "sigs.k8s.io/container-object-storage-interface/proto"
	"sigs.k8s.io/container-object-storage-interface/sidecar/internal/test"
)

type fakeProvisionerServer struct {
	cosiproto.UnimplementedProvisionerServer

	createBucketFunc func(context.Context, *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error)
}

func (s *fakeProvisionerServer) DriverCreateBucket(
	ctx context.Context, req *cosiproto.DriverCreateBucketRequest,
) (*cosiproto.DriverCreateBucketResponse, error) {
	return s.createBucketFunc(ctx, req)
}

func TestBucketReconciler_Reconcile(t *testing.T) {
	baseBucket := cosiapi.Bucket{
		ObjectMeta: meta.ObjectMeta{
			Name: "bc-qwerty",
		},
		Spec: cosiapi.BucketSpec{
			DriverName:     "cosi.s3.corp.net",
			DeletionPolicy: cosiapi.BucketDeletionPolicyRetain,
			// Protocols intentionally nil/empty
			// Parameters intentionally nil/empty
			BucketClaimRef: cosiapi.BucketClaimReference{
				Name:      "my-bucket",
				Namespace: "my-ns",
				UID:       "qwerty",
			},
		},
	}

	bucketNsName := types.NamespacedName{Name: "bc-qwerty"}

	ctx := context.Background()
	// ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	// nolog := ctrl.Log.WithName("test-bucket-reconcile")
	nolog := logr.Discard()
	scheme := runtime.NewScheme()
	err := cosiapi.AddToScheme(scheme)
	require.NoError(t, err)

	newClient := func(withObj ...client.Object) client.Client {
		return fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(withObj...).
			WithStatusSubresource(withObj...). // assume all starting objects have status
			Build()
	}
	t.Run("dynamic provisioning, happy path", func(t *testing.T) {
		seenReq := []*cosiproto.DriverCreateBucketRequest{}
		var requestError error
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				seenReq = append(seenReq, dcbr)
				ret := &cosiproto.DriverCreateBucketResponse{
					BucketId: "cosi-" + dcbr.Name,
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "corp-cosi-" + dcbr.Name,
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}
				return ret, requestError
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		rpcClient := cosiproto.NewProvisionerClient(conn)

		b := baseBucket.DeepCopy()
		b.Spec.Protocols = []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3}
		b.Spec.Parameters = map[string]string{
			"maxSize": "10Gi",
		}
		c := newClient(b)

		r := BucketReconciler{
			Client: c,
			Scheme: scheme,
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  rpcClient,
			},
		}
		nctx := logr.NewContext(ctx, nolog)

		res, err := r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.NoError(t, err)
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 1)
		req := seenReq[0]
		assert.Equal(t, "bc-qwerty", req.Name)
		assert.Equal(t,
			[]*cosiproto.ObjectProtocol{{Type: cosiproto.ObjectProtocol_S3}},
			req.Protocols,
		)
		assert.Equal(t,
			map[string]string{"maxSize": "10Gi"},
			req.Parameters,
		)

		// ensure bucket changes
		bucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, bucket)
		require.NoError(t, err)
		assert.Contains(t, bucket.GetFinalizers(), cosiapi.ProtectionFinalizer)
		assert.Equal(t, b.Spec, bucket.Spec) // spec should not change
		assert.True(t, bucket.Status.ReadyToUse)
		assert.Equal(t, "cosi-bc-qwerty", bucket.Status.BucketID)
		assert.Equal(t,
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			bucket.Status.Protocols,
		)
		assert.NotEmpty(t, bucket.Status.BucketInfo)
		assert.Equal(t, "corp-cosi-bc-qwerty", bucket.Status.BucketInfo["COSI_S3_BUCKET_ID"])
		for k := range bucket.Status.BucketInfo {
			assert.True(t, strings.HasPrefix(k, "COSI_S3_"))
		}

		t.Log("run Reconcile() a second time to ensure nothing is modified")

		seenReq = []*cosiproto.DriverCreateBucketRequest{} // empty the seen rpc requests
		res, err = r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.NoError(t, err)
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 1)
		req = seenReq[0]
		assert.Equal(t, "bc-qwerty", req.Name)

		// ensure bucket doesn't change
		secondBucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, secondBucket)
		require.NoError(t, err)
		assert.Equal(t, bucket.Finalizers, secondBucket.Finalizers)
		assert.Equal(t, bucket.Spec, secondBucket.Spec)
		assert.Equal(t, bucket.Status, secondBucket.Status)

		t.Log("run Reconcile() that fails a third time to ensure status error")

		seenReq = []*cosiproto.DriverCreateBucketRequest{} // empty the seen rpc requests
		requestError = fmt.Errorf("fake rpc error")
		res, err = r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.Error(t, err)
		assert.NotErrorIs(t, err, reconcile.TerminalError(nil))
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 1)
		req = seenReq[0]
		assert.Equal(t, "bc-qwerty", req.Name)

		// ensure bucket status has error but no other status changes
		thirdBucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, thirdBucket)
		require.NoError(t, err)
		assert.Equal(t, secondBucket.Finalizers, thirdBucket.Finalizers)
		assert.Equal(t, secondBucket.Spec, thirdBucket.Spec)
		assert.Equal(t, secondBucket.Status.BucketID, thirdBucket.Status.BucketID)
		assert.Equal(t, secondBucket.Status.BucketInfo, thirdBucket.Status.BucketInfo)
		assert.Equal(t, secondBucket.Status.Protocols, thirdBucket.Status.Protocols)
		serr := thirdBucket.Status.Error
		require.NotNil(t, serr)
		assert.NotNil(t, serr.Time)
		assert.NotNil(t, serr.Message)
		assert.Contains(t, *serr.Message, "fake rpc error")

		t.Log("run Reconcile() that passes a fourth time to ensure status error cleared")

		seenReq = []*cosiproto.DriverCreateBucketRequest{} // empty the seen rpc requests
		requestError = nil
		res, err = r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.NoError(t, err)
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 1)
		req = seenReq[0]
		assert.Equal(t, "bc-qwerty", req.Name)

		// ensure bucket status has error but no other status changes
		fourthBucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, fourthBucket)
		require.NoError(t, err)
		assert.Equal(t, secondBucket.Finalizers, fourthBucket.Finalizers)
		assert.Equal(t, secondBucket.Spec, fourthBucket.Spec)
		assert.Equal(t, secondBucket.Status, fourthBucket.Status) // reverts back to 2nd iteration
	})

	t.Run("dynamic provisioning, bucket missing", func(t *testing.T) {
		seenReq := []*cosiproto.DriverCreateBucketRequest{}
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				seenReq = append(seenReq, dcbr)
				ret := &cosiproto.DriverCreateBucketResponse{
					BucketId: "cosi-" + dcbr.Name,
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "corp-cosi-" + dcbr.Name,
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}
				return ret, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		rpcClient := cosiproto.NewProvisionerClient(conn)

		c := newClient()

		r := BucketReconciler{
			Client: c,
			Scheme: scheme,
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  rpcClient,
			},
		}
		nctx := logr.NewContext(ctx, nolog)

		res, err := r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.NoError(t, err)
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 0)
	})

	t.Run("dynamic provisioning, driver name mismatch", func(t *testing.T) {
		seenReq := []*cosiproto.DriverCreateBucketRequest{}
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				seenReq = append(seenReq, dcbr)
				ret := &cosiproto.DriverCreateBucketResponse{
					BucketId: "cosi-" + dcbr.Name,
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "corp-cosi-" + dcbr.Name,
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}
				return ret, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		rpcClient := cosiproto.NewProvisionerClient(conn)

		b := baseBucket.DeepCopy()
		b.Spec.DriverName = "cosi.NOMATCH.corp.net"
		c := newClient(b)

		r := BucketReconciler{
			Client: c,
			Scheme: scheme,
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  rpcClient,
			},
		}
		nctx := logr.NewContext(ctx, nolog)

		res, err := r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.NoError(t, err)
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 0)

		// ensure bucket doesn't change
		bucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, bucket)
		require.NoError(t, err)
		assert.Equal(t, b.Finalizers, bucket.Finalizers)
		assert.Equal(t, b.Spec, bucket.Spec)
		assert.Equal(t, b.Status, bucket.Status)
	})

	t.Run("dynamic provisioning, proto not supported", func(t *testing.T) {
		seenReq := []*cosiproto.DriverCreateBucketRequest{}
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				seenReq = append(seenReq, dcbr)
				ret := &cosiproto.DriverCreateBucketResponse{
					BucketId: "cosi-" + dcbr.Name,
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "corp-cosi-" + dcbr.Name,
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}
				return ret, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		rpcClient := cosiproto.NewProvisionerClient(conn)

		b := baseBucket.DeepCopy()
		b.Spec.Protocols = []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolGcs}
		c := newClient(b)

		r := BucketReconciler{
			Client: c,
			Scheme: scheme,
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  rpcClient,
			},
		}
		nctx := logr.NewContext(ctx, nolog)

		res, err := r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.Error(t, err)
		assert.ErrorIs(t, err, reconcile.TerminalError(nil))
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 0)

		// ensure bucket error
		bucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, bucket)
		require.NoError(t, err)
		assert.Equal(t, b.Finalizers, bucket.Finalizers)
		assert.Equal(t, b.Spec, bucket.Spec)
		assert.False(t, bucket.Status.ReadyToUse) // assume this means no other statuses were set
		serr := bucket.Status.Error
		require.NotNil(t, serr)
		assert.NotNil(t, serr.Time)
		assert.NotNil(t, serr.Message)
		assert.Contains(t, *serr.Message, "GCS")
	})

	t.Run("dynamic provisioning, provisioned bucket supports wrong proto", func(t *testing.T) {
		seenReq := []*cosiproto.DriverCreateBucketRequest{}
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				seenReq = append(seenReq, dcbr)
				ret := &cosiproto.DriverCreateBucketResponse{
					BucketId: "cosi-" + dcbr.Name,
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						Azure: &cosiproto.AzureBucketInfo{}, // bucket.spec wants S3
					},
				}
				return ret, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		rpcClient := cosiproto.NewProvisionerClient(conn)

		b := baseBucket.DeepCopy()
		b.Spec.Protocols = []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3}
		b.Spec.Parameters = map[string]string{
			"maxSize": "10Gi",
		}
		c := newClient(b)

		r := BucketReconciler{
			Client: c,
			Scheme: scheme,
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  rpcClient,
			},
		}
		nctx := logr.NewContext(ctx, nolog)

		res, err := r.Reconcile(nctx, ctrl.Request{NamespacedName: bucketNsName})
		assert.Error(t, err)
		assert.ErrorIs(t, err, reconcile.TerminalError(nil))
		assert.Empty(t, res)

		// ensure the expected RPC call was made
		require.Len(t, seenReq, 1)
		req := seenReq[0]
		assert.Equal(t, "bc-qwerty", req.Name)

		// ensure bucket error
		bucket := &cosiapi.Bucket{}
		err = r.Get(ctx, bucketNsName, bucket)
		require.NoError(t, err)
		assert.Contains(t, bucket.GetFinalizers(), cosiapi.ProtectionFinalizer)
		assert.Equal(t, b.Spec, bucket.Spec)
		assert.False(t, bucket.Status.ReadyToUse) // assume this means no other statuses were set
		serr := bucket.Status.Error
		require.NotNil(t, serr)
		assert.NotNil(t, serr.Time)
		assert.NotNil(t, serr.Message)
		assert.Contains(t, *serr.Message, "protocols are not supported")
		assert.Contains(t, *serr.Message, "S3") // required proto
	})

	// TODO: deletion (dynamic and static, Retain/Delete)
	// TODO: static provisioning
}

func TestBucketReconciler_dynamicProvision(t *testing.T) {
	validClaimRef := cosiapi.BucketClaimReference{
		Name:      "userbucket",
		Namespace: "usernamespace",
		UID:       "qwerty",
	}
	t.Run("valid driver and bucket, successful provision", func(t *testing.T) {
		requestParams := map[string]string{} // record the params sent in the request to verify later

		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				requestParams = dcbr.Parameters
				ret := &cosiproto.DriverCreateBucketResponse{
					BucketId: dcbr.Name,
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "backend-" + dcbr.Name, // example of backend bucket with slight variation from request.Name
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}
				return ret, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		inputParams := map[string]string{
			"key":    "value",
			"option": "setting",
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: inputParams,
			claimRef:   validClaimRef,
		})
		assert.NoError(t, err)
		assert.False(t, bool(retryErr))
		assert.Equal(t, "bc-qwerty", details.bucketId)
		assert.Equal(t, []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3}, details.supportedProtos)
		// If we check the exact results of details.allProtoBucketInfo, we will tie the unit tests
		// to the specific implementation of the S3 bucket info translator, tested elsewhere.
		// Instead, check only COSI_S3_BUCKET_ID which is unlikely to change in the future, and
		// check that all info is prefixed `COSI_S3_`.
		assert.NotEmpty(t, details.allProtoBucketInfo)
		assert.Equal(t, "backend-bc-qwerty", details.allProtoBucketInfo[string(cosiapi.BucketInfoVar_S3_BucketId)])
		for k := range details.allProtoBucketInfo {
			assert.True(t, strings.HasPrefix(k, "COSI_S3_"))
		}
		assert.Equal(t, inputParams, requestParams)
	})

	t.Run("valid driver and bucket, retryable provision error", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				if len(dcbr.Parameters) != 0 {
					t.Errorf("expecting request parameters to be empty")
				}
				return &cosiproto.DriverCreateBucketResponse{}, status.Error(codes.Unknown, "fake unknown err")
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "fake unknown err")
		assert.True(t, bool(retryErr))
		assert.Nil(t, details)
	})

	t.Run("valid driver and bucket, non-retryable provision error", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{}, status.Error(codes.InvalidArgument, "fake invalid arg err")
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "fake invalid arg err")
		assert.False(t, bool(retryErr))
		assert.Nil(t, details)
	})

	t.Run("valid driver, claim ref malformed", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId: "bc-qwerty",
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "backend-" + dcbr.Name, // example of backend bucket with slight variation from request.Name
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   cosiapi.BucketClaimReference{},
		})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "all bucketClaimRef fields must be set")
		assert.False(t, bool(retryErr))
		assert.Nil(t, details)
	})

	t.Run("valid driver, bucket ID missing", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId: "", // MISSING
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{
							Endpoint:        "s3.corp.net",
							BucketId:        "backend-" + dcbr.Name, // example of backend bucket with slight variation from request.Name
							Region:          "us-east-1",
							AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
						},
					},
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "bucket ID missing")
		assert.False(t, bool(retryErr))
		assert.Nil(t, details)
	})

	t.Run("valid driver, proto response nil", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId:  "bc-qwerty",
					Protocols: nil,
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.Error(t, err)
		assert.ErrorContains(t, err, "protocol response missing")
		assert.False(t, bool(retryErr))
		assert.Nil(t, details)
	})

	t.Run("valid driver, empty S3 proto response", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId: "bc-qwerty",
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3: &cosiproto.S3BucketInfo{},
					},
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.s3.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_S3},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.NoError(t, err)
		assert.False(t, bool(retryErr))
		assert.Equal(t, "bc-qwerty", details.bucketId)
		assert.Equal(t, []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3}, details.supportedProtos)
		assert.NotEmpty(t, details.allProtoBucketInfo) // bucket info should be present
		for k, v := range details.allProtoBucketInfo {
			assert.True(t, strings.HasPrefix(k, "COSI_S3_"))
			assert.Empty(t, v) // but all info will be empty string
		}
	})

	t.Run("valid driver, empty Azure proto response", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId: "bc-qwerty",
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						Azure: &cosiproto.AzureBucketInfo{},
					},
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_AZURE},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_AZURE},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.NoError(t, err)
		assert.False(t, bool(retryErr))
		assert.Equal(t, "bc-qwerty", details.bucketId)
		assert.Equal(t, []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolAzure}, details.supportedProtos)
		assert.NotEmpty(t, details.allProtoBucketInfo) // bucket info should be present
		for k, v := range details.allProtoBucketInfo {
			assert.True(t, strings.HasPrefix(k, "COSI_AZURE_"))
			assert.Empty(t, v) // but all info will be empty string
		}
	})

	t.Run("valid driver, empty GCS proto response", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId: "bc-qwerty",
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						Gcs: &cosiproto.GcsBucketInfo{},
					},
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name:               "cosi.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{cosiproto.ObjectProtocol_GCS},
				provisionerClient:  client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_GCS},
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.NoError(t, err)
		assert.False(t, bool(retryErr))
		assert.Equal(t, "bc-qwerty", details.bucketId)
		assert.Equal(t, []cosiapi.ObjectProtocol{cosiapi.ObjectProtocolGcs}, details.supportedProtos)
		assert.NotEmpty(t, details.allProtoBucketInfo) // bucket info should be present
		for k, v := range details.allProtoBucketInfo {
			assert.True(t, strings.HasPrefix(k, "COSI_GCS_"))
			assert.Empty(t, v) // but all info will be empty string
		}
	})

	t.Run("valid driver, empty S3+Azure proto response", func(t *testing.T) {
		fakeServer := fakeProvisionerServer{
			createBucketFunc: func(ctx context.Context, dcbr *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
				return &cosiproto.DriverCreateBucketResponse{
					BucketId: "bc-qwerty",
					Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
						S3:    &cosiproto.S3BucketInfo{},
						Azure: &cosiproto.AzureBucketInfo{},
					},
				}, nil
			},
		}

		cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
		defer cleanup()
		require.NoError(t, err)
		go serve()

		conn, err := test.ClientConn(tmpSock)
		require.NoError(t, err)
		client := cosiproto.NewProvisionerClient(conn)

		r := BucketReconciler{
			DriverInfo: DriverInfo{
				name: "cosi.corp.net",
				supportedProtocols: []cosiproto.ObjectProtocol_Type{
					cosiproto.ObjectProtocol_S3,
					cosiproto.ObjectProtocol_AZURE,
				},
				provisionerClient: client,
			},
		}

		details, retryErr, err := r.dynamicProvision(context.Background(), logr.Discard(), dynamicProvisionParams{
			bucketName: "bc-qwerty",
			requiredProtos: []*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3}, // example of request for S3, returned support for S3+Azure
			},
			parameters: map[string]string{}, // intentionally empty
			claimRef:   validClaimRef,
		})
		assert.NoError(t, err)
		assert.False(t, bool(retryErr))
		assert.Equal(t, "bc-qwerty", details.bucketId)
		assert.ElementsMatch(t,
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
				cosiapi.ObjectProtocolAzure,
			},
			details.supportedProtos,
		)
		assert.NotEmpty(t, details.allProtoBucketInfo) // bucket info should be present
		for k, v := range details.allProtoBucketInfo {
			assert.True(t, strings.HasPrefix(k, "COSI_S3_") || strings.HasPrefix(k, "COSI_AZURE_"))
			assert.Empty(t, v) // but all info will be empty string
		}
	})
}

func Test_parseProtocolBucketInfo(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		pbi                 *cosiproto.ObjectProtocolAndBucketInfo
		wantProtos          []cosiapi.ObjectProtocol
		wantInfoVarPrefixes []string
	}{
		{"no info", &cosiproto.ObjectProtocolAndBucketInfo{}, []cosiapi.ObjectProtocol{}, []string{}},
		{"s3 empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				S3: &cosiproto.S3BucketInfo{},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
			},
			[]string{
				"COSI_S3_",
			},
		},
		{"s3 non-empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				S3: &cosiproto.S3BucketInfo{
					BucketId: "something",
					Endpoint: "cosi.corp.net",
				},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
			},
			[]string{
				"COSI_S3_",
			},
		},
		{"azure empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				Azure: &cosiproto.AzureBucketInfo{},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolAzure,
			},
			[]string{
				"COSI_AZURE_",
			},
		},
		{"azure non-empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				Azure: &cosiproto.AzureBucketInfo{
					StorageAccount: "something",
				},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolAzure,
			},
			[]string{
				"COSI_AZURE_",
			},
		},
		{"GCS empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				Gcs: &cosiproto.GcsBucketInfo{},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolGcs,
			},
			[]string{
				"COSI_GCS_",
			},
		},
		{"GCS non-empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				Gcs: &cosiproto.GcsBucketInfo{
					BucketName: "something",
				},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolGcs,
			},
			[]string{
				"COSI_GCS_",
			},
		},
		{"s3+azure+GCS empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				S3:    &cosiproto.S3BucketInfo{},
				Azure: &cosiproto.AzureBucketInfo{},
				Gcs:   &cosiproto.GcsBucketInfo{},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
				cosiapi.ObjectProtocolAzure,
				cosiapi.ObjectProtocolGcs,
			},
			[]string{
				"COSI_S3_",
				"COSI_AZURE_",
				"COSI_GCS_",
			},
		},
		{"s3+azure+GCS non-empty",
			&cosiproto.ObjectProtocolAndBucketInfo{
				S3: &cosiproto.S3BucketInfo{
					BucketId: "something",
				},
				Azure: &cosiproto.AzureBucketInfo{
					StorageAccount: "acct",
				},
				Gcs: &cosiproto.GcsBucketInfo{
					BucketName: "something",
				},
			},
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
				cosiapi.ObjectProtocolAzure,
				cosiapi.ObjectProtocolGcs,
			},
			[]string{
				"COSI_S3_",
				"COSI_AZURE_",
				"COSI_GCS_",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protos, infoVars := parseProtocolBucketInfo(tt.pbi)
			assert.Equal(t, tt.wantProtos, protos)
			// If we check the exact results of details.allProtoBucketInfo, we will tie the unit
			// tests to the specific implementation of bucket info translators, tested elsewhere.
			// Instead, check only that prefixes match what we expect.
			if len(tt.wantProtos) > 0 {
				assert.NotZero(t, len(infoVars))
			} else {
				assert.Zero(t, len(infoVars))
			}
			for _, p := range tt.wantInfoVarPrefixes {
				found := false
				for k := range infoVars {
					assert.True(t, strings.HasPrefix(k, "COSI_")) // all vars must be prefixed COSI_
					if strings.HasPrefix(k, p) {
						found = true
					}
				}
				assert.Truef(t, found, "prefix %q not found in %v keys", p, infoVars)
			}
		})
	}
}

func Test_objectProtocolListFromApiList(t *testing.T) {
	tests := []struct {
		name    string                   // description of this test case
		apiList []cosiapi.ObjectProtocol // input
		want    []*cosiproto.ObjectProtocol
		wantErr bool
	}{
		{"nil list", nil, []*cosiproto.ObjectProtocol{}, false},
		{"empty list", nil, []*cosiproto.ObjectProtocol{}, false},
		{"S3 only",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			false,
		},
		{"Azure only",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolAzure},
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_AZURE},
			},
			false,
		},
		{"S3 and Azure",
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
				cosiapi.ObjectProtocolAzure,
			},
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
				{Type: cosiproto.ObjectProtocol_AZURE},
			},
			false,
		},
		{"unknown proto",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocol("unknown-proto")},
			nil,
			true,
		},
		{"S3 and unknown proto",
			[]cosiapi.ObjectProtocol{
				cosiapi.ObjectProtocolS3,
				cosiapi.ObjectProtocol("unknown-proto"),
			},
			nil,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := objectProtocolListFromApiList(tt.apiList)
			if tt.wantErr {
				assert.Error(t, gotErr)
			} else {
				assert.NoError(t, gotErr)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_validateDriverSupportsProtocols(t *testing.T) {
	driverSupportsS3 := DriverInfo{
		name: "cosi.s3.mycorp.net",
		supportedProtocols: []cosiproto.ObjectProtocol_Type{
			cosiproto.ObjectProtocol_S3,
		},
	}
	driverSupportsS3andAzure := DriverInfo{
		name: "cosi.azure-s3-meta.mycorp.net",
		supportedProtocols: []cosiproto.ObjectProtocol_Type{
			cosiproto.ObjectProtocol_S3,
			cosiproto.ObjectProtocol_AZURE,
		},
	}
	driverSupportsNothing := DriverInfo{
		name:               "cosi.nil.mycorp.net",
		supportedProtocols: []cosiproto.ObjectProtocol_Type{},
	}

	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		driver   DriverInfo
		required []*cosiproto.ObjectProtocol
		wantErr  bool
	}{
		{"no support, no required",
			driverSupportsNothing,
			[]*cosiproto.ObjectProtocol{},
			false,
		},
		{"no support, S3 required",
			driverSupportsNothing,
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			true,
		},
		{"no support, S3+Azure required",
			driverSupportsNothing,
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
				{Type: cosiproto.ObjectProtocol_AZURE},
			},
			true,
		},
		{"s3 support, no required",
			driverSupportsS3,
			[]*cosiproto.ObjectProtocol{},
			false,
		},
		{"s3 support, S3 required",
			driverSupportsS3,
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			false,
		},
		{"s3 support, S3+Azure required",
			driverSupportsS3,
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
				{Type: cosiproto.ObjectProtocol_AZURE},
			},
			true,
		},
		{"s3+Azure support, no required",
			driverSupportsS3andAzure,
			[]*cosiproto.ObjectProtocol{},
			false,
		},
		{"s3+Azure support, S3 required",
			driverSupportsS3andAzure,
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
			},
			false,
		},
		{"s3+Azure support, S3+Azure required",
			driverSupportsS3andAzure,
			[]*cosiproto.ObjectProtocol{
				{Type: cosiproto.ObjectProtocol_S3},
				{Type: cosiproto.ObjectProtocol_AZURE},
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErr := validateDriverSupportsProtocols(tt.driver, tt.required)
			if tt.wantErr {
				assert.Error(t, gotErr)
			} else {
				assert.NoError(t, gotErr)
			}
		})
	}
}

func Test_validateBucketSupportsProtocols(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		required  []cosiapi.ObjectProtocol
		supported []cosiapi.ObjectProtocol
		wantErr   bool
	}{
		{"no support, no required",
			[]cosiapi.ObjectProtocol{},
			[]cosiapi.ObjectProtocol{},
			false,
		},
		{"no support, S3 required",
			[]cosiapi.ObjectProtocol{},
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			true,
		},
		{"no support, S3+Azure required",
			[]cosiapi.ObjectProtocol{},
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3, cosiapi.ObjectProtocolAzure},
			true,
		},
		{"S3 support, no required",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			[]cosiapi.ObjectProtocol{},
			false,
		},
		{"S3 support, S3 required",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			false,
		},
		{"S3 support, S3+Azure required",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3, cosiapi.ObjectProtocolAzure},
			true,
		},
		{"S3+Azure support, no required",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3, cosiapi.ObjectProtocolAzure},
			[]cosiapi.ObjectProtocol{},
			false,
		},
		{"S3+Azure support, S3 required",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3, cosiapi.ObjectProtocolAzure},
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3},
			false,
		},
		{"S3+Azure support, S3+Azure required",
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3, cosiapi.ObjectProtocolAzure},
			[]cosiapi.ObjectProtocol{cosiapi.ObjectProtocolS3, cosiapi.ObjectProtocolAzure},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErr := validateBucketSupportsProtocols(tt.required, tt.supported)
			if tt.wantErr {
				assert.Error(t, gotErr)
			} else {
				assert.NoError(t, gotErr)
			}
		})
	}
}
