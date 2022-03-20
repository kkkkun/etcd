// Copyright 2022 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build e2e
// +build e2e

package common

import (
	"fmt"
	"go.etcd.io/etcd/tests/v3/framework"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestDiscovery(t *testing.T) {
	e2e.BeforeTest(t)

	tcs := []struct {
		name                 string
		targetClusterSize    int
		discoveryClusterSize int
		clientTlsType        e2e.ClientConnType
		isClientAutoTls      bool
	}{
		{
			name:                 "ClusterOf1UsingV3Discovery_1endpoint",
			discoveryClusterSize: 1,
			targetClusterSize:    1,
			clientTlsType:        e2e.ClientNonTLS,
			isClientAutoTls:      false,
		},
		{
			name:                 "ClusterOf3UsingV3Discovery_1endpoint",
			discoveryClusterSize: 1,
			targetClusterSize:    3,
			clientTlsType:        e2e.ClientTLS,
			isClientAutoTls:      true,
		},
		{
			name:                 "ClusterOf3UsingV3Discovery_1endpoint",
			discoveryClusterSize: 1,
			targetClusterSize:    5,
			clientTlsType:        e2e.ClientTLS,
			isClientAutoTls:      false,
		},
		{
			name:                 "ClusterOf1UsingV3Discovery_3endpoints",
			discoveryClusterSize: 3,
			targetClusterSize:    1,
			clientTlsType:        e2e.ClientNonTLS,
			isClientAutoTls:      false,
		},
		{
			name:                 "ClusterOf3UsingV3Discovery_3endpoints",
			discoveryClusterSize: 3,
			targetClusterSize:    3,
			clientTlsType:        e2e.ClientTLS,
			isClientAutoTls:      true,
		},
		{
			name:                 "TLSClusterOf5UsingV3Discovery_3endpoints",
			discoveryClusterSize: 3,
			targetClusterSize:    5,
			clientTlsType:        e2e.ClientTLS,
			isClientAutoTls:      false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			testutils.ExecuteWithTimeout(t, 10*time.Second, func() {

				// step 1: start the discovery service
				ds, err := e2e.NewEtcdProcessCluster(t, &e2e.EtcdProcessClusterConfig{
					InitialToken:    "new",
					BasePort:        2000,
					ClusterSize:     tc.discoveryClusterSize,
					ClientTLS:       tc.clientTlsType,
					IsClientAutoTLS: tc.isClientAutoTls,
				})
				if err != nil {
					t.Fatalf("could not start discovery etcd cluster (%v)", err)
				}
				clus := framework.NewClusterByProcess(*ds)
				defer clus.Close()

				// step 2: configure the cluster size
				discoveryToken := "8A591FAB-1D72-41FA-BDF2-A27162FDA1E0"
				configSizeKey := fmt.Sprintf("/_etcd/registry/%s/_config/size", discoveryToken)
				configSizeValStr := strconv.Itoa(tc.targetClusterSize)
				err = clus.Client().Put(configSizeKey, configSizeValStr, config.PutOptions{})
				if err != nil {
					t.Errorf("failed to configure cluster size to discovery serivce, error: %v", err)
				}

				// step 3: start the etcd cluster
				epc, err := bootstrapEtcdClusterUsingV3Discovery(t, ds.EndpointsV3(), discoveryToken, tc.targetClusterSize, tc.clientTlsType, tc.isClientAutoTls)
				if err != nil {
					t.Fatalf("could not start etcd process cluster (%v)", err)
				}
				defer epc.Close()

				// step 4: sanity test on the etcd cluster
				etcdctl := []string{e2e.CtlBinPath, "--endpoints", strings.Join(epc.EndpointsV3(), ",")}
				if err := e2e.SpawnWithExpect(append(etcdctl, "put", "key", "value"), "OK"); err != nil {
					t.Fatal(err)
				}
				if err := e2e.SpawnWithExpect(append(etcdctl, "get", "key"), "value"); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func bootstrapEtcdClusterUsingV3Discovery(t *testing.T, discoveryEndpoints []string, discoveryToken string, clusterSize int, clientTlsType e2e.ClientConnType, isClientAutoTls bool) (*e2e.EtcdProcessCluster, error) {
	// cluster configuration
	cfg := &e2e.EtcdProcessClusterConfig{
		BasePort:           3000,
		ClusterSize:        clusterSize,
		IsPeerTLS:          true,
		IsPeerAutoTLS:      true,
		DiscoveryToken:     discoveryToken,
		DiscoveryEndpoints: discoveryEndpoints,
	}

	// initialize the cluster
	epc, err := e2e.InitEtcdProcessCluster(t, cfg)
	if err != nil {
		t.Fatalf("could not initialize etcd cluster (%v)", err)
		return epc, err
	}

	// populate discovery related security configuration
	for _, ep := range epc.Procs {
		epCfg := ep.Config()

		if clientTlsType == e2e.ClientTLS {
			if isClientAutoTls {
				epCfg.Args = append(epCfg.Args, "--discovery-insecure-transport=false")
				epCfg.Args = append(epCfg.Args, "--discovery-insecure-skip-tls-verify=true")
			} else {
				epCfg.Args = append(epCfg.Args, "--discovery-cacert="+e2e.CaPath)
				epCfg.Args = append(epCfg.Args, "--discovery-cert="+e2e.CertPath)
				epCfg.Args = append(epCfg.Args, "--discovery-key="+e2e.PrivateKeyPath)
			}
		}
	}

	// start the cluster
	return e2e.StartEtcdProcessCluster(epc, cfg)
}
