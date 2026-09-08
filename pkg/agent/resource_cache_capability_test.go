package agent

import (
	"context"
	"io"
	"slices"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

// The Agent announces the cache only when it has one, and stops offering the
// Stream to a Server that has not learned about it. Both halves matter for a
// rolling upgrade: without the first, a Server would send a request the Agent
// cannot answer; without the second, an Agent would keep a handler listening
// for a request that can never arrive.
func TestResourceCacheCapabilityHandshake(t *testing.T) {
	t.Parallel()

	withCache := connectionServices{
		resourceHandler: func(
			_ context.Context,
			_ *agentv1.ResourceRequest,
			_ io.Reader,
		) (*agentv1.ResourceResponse, io.Reader, error) {
			return nil, nil, nil
		},
		resourceCacheHandler: func(
			_ context.Context,
			_ *agentv1.ResourceCacheRequest,
		) (*agentv1.ResourceCacheResponse, io.Reader, error) {
			return nil, nil, nil
		},
	}
	announced := agentCapabilities(withCache)
	for _, expected := range []string{
		agentprotocol.CapabilityOverviewSnapshotV1,
		agentprotocol.CapabilityResourceSummaryV1,
	} {
		if !slices.Contains(announced, expected) {
			t.Fatalf("an Agent with a cache did not announce %q: %v", expected, announced)
		}
	}

	withoutCache := connectionServices{resourceHandler: withCache.resourceHandler}
	silent := agentCapabilities(withoutCache)
	for _, unexpected := range []string{
		agentprotocol.CapabilityOverviewSnapshotV1,
		agentprotocol.CapabilityResourceSummaryV1,
	} {
		if slices.Contains(silent, unexpected) {
			t.Fatalf("an Agent without a cache announced %q", unexpected)
		}
	}
}
