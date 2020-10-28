// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package server_test

import (
	"context"
	"testing"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/kv/kvserver"
	"github.com/cockroachdb/cockroach/pkg/testutils"
	"github.com/cockroachdb/cockroach/pkg/testutils/skip"
	"github.com/cockroachdb/cockroach/pkg/testutils/testcluster"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/require"
)

// TestAddNewStoresToExistingNodes tests database behavior with
// multiple stores per node, in particular when new stores are
// added while nodes are shut down. This test starts a cluster with
// three nodes, shuts down all nodes and adds a store to each node,
// and ensures nodes start back up successfully. See #39415.
func TestAddNewStoresToExistingNodes(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	skip.UnderStress(t, "too many new stores and nodes for stress")

	ctx := context.Background()

	n1s1, n1cleanup1 := testutils.TempDir(t)
	defer n1cleanup1()
	n2s1, n2cleanup1 := testutils.TempDir(t)
	defer n2cleanup1()
	n3s1, n3cleanup1 := testutils.TempDir(t)
	defer n3cleanup1()

	numNodes := 3
	tcArgs := base.TestClusterArgs{
		ServerArgsPerNode: map[int]base.TestServerArgs{
			// NB: on my local (beefy) machine, upreplication
			// takes ~6s. This is pretty hefty compared to ~1s
			// with ephemeral stores. But - we need the real
			// stores here. At the time of writing, we perform
			// ~100 change replicas txns, all in all, and
			// 0.06s for a replication change does seem ok.
			0: {StoreSpecs: []base.StoreSpec{{Path: n1s1}}},
			1: {StoreSpecs: []base.StoreSpec{{Path: n2s1}}},
			2: {StoreSpecs: []base.StoreSpec{{Path: n3s1}}},
		},
	}

	tc := testcluster.StartTestCluster(t, numNodes, tcArgs)
	// NB: it's important that this test wait for full replication. Otherwise,
	// with only a single voter on the range that allocates store IDs, it can
	// pass erroneously. StartTestCluster already calls it, but we call it
	// again explicitly.
	if err := tc.WaitForFullReplication(); err != nil {
		log.Fatalf(ctx, "while waiting for full replication: %v", err)
	}
	clusterID := tc.Server(0).ClusterID()
	tc.Stopper().Stop(ctx)

	// Add an additional store to each node.
	n1s2, n1cleanup2 := testutils.TempDir(t)
	defer n1cleanup2()
	n2s2, n2cleanup2 := testutils.TempDir(t)
	defer n2cleanup2()
	n3s2, n3cleanup2 := testutils.TempDir(t)
	defer n3cleanup2()

	tcArgs = base.TestClusterArgs{
		// We need ParallelStart since this is an existing cluster. If
		// we started sequentially, then the first node would hang forever
		// waiting for the KV layer to become available, but that only
		// happens when the second node also starts.
		ParallelStart:   true,
		ReplicationMode: base.ReplicationManual, // saves time
		ServerArgsPerNode: map[int]base.TestServerArgs{
			0: {
				StoreSpecs: []base.StoreSpec{
					{Path: n1s1}, {Path: n1s2},
				},
			},
			1: {
				StoreSpecs: []base.StoreSpec{
					{Path: n2s1}, {Path: n2s2},
				},
			},
			2: {
				StoreSpecs: []base.StoreSpec{
					{Path: n3s1}, {Path: n3s2},
				},
			},
		},
	}

	// Start all nodes with additional stores.
	tc = testcluster.StartTestCluster(t, numNodes, tcArgs)
	defer tc.Stopper().Stop(ctx)

	// Sanity check that we're testing what we wanted to test and didn't accidentally
	// bootstrap three single-node clusters (who knows).
	for _, srv := range tc.Servers {
		require.Equal(t, clusterID, srv.ClusterID())
	}

	// Ensure all nodes have 2 stores available.
	testutils.SucceedsSoon(t, func() error {
		for _, server := range tc.Servers {
			var storeCount = 0

			err := server.GetStores().(*kvserver.Stores).VisitStores(
				func(s *kvserver.Store) error {
					storeCount++
					return nil
				},
			)
			if err != nil {
				return errors.Errorf("failed to visit all nodes, got %v", err)
			}

			if storeCount != 2 {
				return errors.Errorf("expected two stores to be available on node %v, got %v stores instead", server.NodeID(), storeCount)
			}
		}

		return nil
	})
}