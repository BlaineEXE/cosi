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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cosiproto "sigs.k8s.io/container-object-storage-interface/proto"
	"sigs.k8s.io/container-object-storage-interface/sidecar/internal/test"
)

func Test_reconcile(t *testing.T) {
	fakeServer := fakeProvisionerServer{
		createBucketResponse: &cosiproto.DriverCreateBucketResponse{
			BucketId: "bucket-id",
			Protocols: &cosiproto.ObjectProtocolAndBucketInfo{
				S3: &cosiproto.S3BucketInfo{
					Endpoint:        "s3.corp.net",
					BucketId:        "bc-qwerty",
					Region:          "us-east-1",
					AddressingStyle: &cosiproto.S3AddressingStyle{Style: cosiproto.S3AddressingStyle_PATH},
				},
			},
		},
		createBucketErr: nil,
	}

	cleanup, serve, tmpSock, err := test.Server(nil, &fakeServer)
	defer cleanup()
	require.NoError(t, err)
	go serve()

	conn, err := test.ClientConn(tmpSock)
	require.NoError(t, err)
	client := cosiproto.NewProvisionerClient(conn)

	resp, err := client.DriverCreateBucket(context.Background(), &cosiproto.DriverCreateBucketRequest{
		Name:       "bc-qwerty",
		Parameters: map[string]string{},
	})
	assert.NoError(t, err)

	t.Logf("response: %#v", resp)
	t.Logf("protocols: %#v", resp.Protocols)

}

type fakeProvisionerServer struct {
	cosiproto.UnimplementedProvisionerServer

	createBucketSleep    time.Duration
	createBucketResponse *cosiproto.DriverCreateBucketResponse
	createBucketErr      error
}

func (s *fakeProvisionerServer) DriverCreateBucket(
	context.Context, *cosiproto.DriverCreateBucketRequest) (*cosiproto.DriverCreateBucketResponse, error) {
	if s.createBucketSleep > 0 {
		time.Sleep(s.createBucketSleep)
	}
	return s.createBucketResponse, s.createBucketErr
}
