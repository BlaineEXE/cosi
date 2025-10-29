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

package test

import (
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	cosiproto "sigs.k8s.io/container-object-storage-interface/proto"
)

// Bootstrap a COSI RPC server for unit testing. It is better to create a realistic server for
// testing rather than stubbing out fake client calls so that RPC over-the-wire serialization
// effects are fully accounted for in unit tests.
//
// cleanupFunc() cleans up the `tmpSockUri` directory and stops the server as needed.
// It is best to always call the cleanup function after every invocation, even after errors.
//
// startServerFunc() starts the bootstrapped server. It should usually run in a goroutine.
//
// tmpSockUri is the temporary unix socket URI (unix://<path>/cosi.sock) needed by clients.
func Server(fakeIdentity cosiproto.IdentityServer, fakeProvisioner cosiproto.ProvisionerServer) (
	cleanupFunc func(),
	startServerFunc func(),
	tmpSockUri string,
	err error,
) {
	cleanupFunc = func() {}
	startServerFunc = func() {}

	// os.tempDir() path is really long sometimes (e.g., on macos)
	// unix socket has ~100 char limit, so need to keep tmpdir location short
	tmpDir, err := os.MkdirTemp("/tmp", "tempDir")
	if err != nil {
		return cleanupFunc, startServerFunc, "", err
	}
	cleanupTmp := func() { _ = os.RemoveAll(tmpDir) }
	cleanupFunc = cleanupTmp

	sockPath := tmpDir + "/cosi.sock"
	tmpSockUri = "unix://" + sockPath

	server := grpc.NewServer()
	if fakeIdentity != nil {
		cosiproto.RegisterIdentityServer(server, fakeIdentity)
	}
	if fakeProvisioner != nil {
		cosiproto.RegisterProvisionerServer(server, fakeProvisioner)
	}
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		cleanupFunc()
		cleanupFunc = func() {} // just cleaned up
		return cleanupFunc, startServerFunc, tmpSockUri, err
	}
	cleanupFunc = func() {
		cleanupTmp()
		server.Stop()
	}
	startServerFunc = func() {
		err = server.Serve(listener)
		if err != nil {
			panic(err) // only panics if the server doesn't stop via cleanupFunc()
		}
	}

	return cleanupFunc, startServerFunc, tmpSockUri, nil
}

// ClientConn creates a simple RPC client connection for unit testing.
func ClientConn(tmpSockUri string) (*grpc.ClientConn, error) {
	return grpc.NewClient(tmpSockUri,
		// nothing fancy for unit tests
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}
