// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package topology

import (
	"strings"
	"testing"
)

func store() ClusterStore {
	return ClusterStore{Type: "PostgreSql", Host: "10.0.0.2", Port: 5000, Database: "inbuxa", User: "inbuxa", SecretFile: "pg.pw"}
}

func serverMachine(name string, c *ClusterNode) Machine {
	return Machine{Name: name, Components: []Component{{Kind: "server", Shape: "host"}}, Cluster: c}
}

func TestVersionOneStillValid(t *testing.T) {
	v1 := &Topology{Version: 1, Domain: "example.com", Machines: []Machine{serverMachine("a", nil)}}
	if err := v1.Validate(); err != nil {
		t.Errorf("a version 1 file with no cluster block should still be valid: %v", err)
	}
}

func TestClusterBlockNeedsVersionTwo(t *testing.T) {
	v1 := &Topology{Version: 1, Domain: "example.com", Machines: []Machine{serverMachine("a", &ClusterNode{Store: store()})}}
	if err := v1.Validate(); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Errorf("a cluster block under version 1 should be refused, got %v", err)
	}
}

func TestClusterNodeMustRunServer(t *testing.T) {
	m := Machine{Name: "fe", Components: []Component{{Kind: "webmail", Shape: "container"}}, Cluster: &ClusterNode{Store: store()}}
	// A lone webmail machine also needs a server somewhere; add one so the
	// failure under test is the cluster-on-a-non-server one.
	tp := &Topology{Version: 2, Domain: "example.com", Machines: []Machine{serverMachine("a", nil), m}}
	if err := tp.Validate(); err == nil || !strings.Contains(err.Error(), "runs no mail server") {
		t.Errorf("a cluster block on a machine with no server should be refused, got %v", err)
	}
}

func TestClusteredMultiServer(t *testing.T) {
	ok := &Topology{Version: 2, Domain: "example.com", Machines: []Machine{
		serverMachine("a", &ClusterNode{Store: store()}),
		serverMachine("b", &ClusterNode{Store: store(), Role: "peer"}),
	}}
	if err := ok.Validate(); err != nil {
		t.Errorf("two mail nodes that both carry a cluster block is a clustered install: %v", err)
	}

	mixed := &Topology{Version: 2, Domain: "example.com", Machines: []Machine{
		serverMachine("a", &ClusterNode{Store: store()}),
		serverMachine("b", nil), // a second server with no cluster block
	}}
	if err := mixed.Validate(); err == nil || !strings.Contains(err.Error(), "on every mail node") {
		t.Errorf("two servers with only one cluster block should be refused, got %v", err)
	}
}

func TestClusterStoreValidation(t *testing.T) {
	bad := store()
	bad.Port = 0
	tp := &Topology{Version: 2, Domain: "example.com", Machines: []Machine{serverMachine("a", &ClusterNode{Store: bad})}}
	if err := tp.Validate(); err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("a store with no port should be refused, got %v", err)
	}
}

func TestClusterServiceNeedsSecretFile(t *testing.T) {
	tp := &Topology{Version: 2, Domain: "example.com", Machines: []Machine{
		serverMachine("a", &ClusterNode{Store: store(), Coordinator: &ClusterService{Addr: "h:4223"}}),
	}}
	if err := tp.Validate(); err == nil || !strings.Contains(err.Error(), "coordinator needs a secret_file") {
		t.Errorf("a coordinator with no secret_file should be refused, got %v", err)
	}
}

func TestVersionRange(t *testing.T) {
	tp := &Topology{Version: 3, Domain: "example.com", Machines: []Machine{serverMachine("a", nil)}}
	if err := tp.Validate(); err == nil || !strings.Contains(err.Error(), "versions 1 to 2") {
		t.Errorf("a future version should be refused, got %v", err)
	}
}
