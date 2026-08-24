package experiment

import "testing"

func TestAdaptiveRoutingImprovesFailureScenario(t *testing.T) {
	results := RunRoutingComparison(DefaultRoutingConfig())
	roundRobin, adaptive := results[0], results[1]
	if adaptive.Errors >= roundRobin.Errors {
		t.Fatalf("adaptive errors did not improve: RR=%d adaptive=%d", roundRobin.Errors, adaptive.Errors)
	}
	if adaptive.P99Milliseconds >= roundRobin.P99Milliseconds {
		t.Fatalf("adaptive P99 did not improve: RR=%.2f adaptive=%.2f",
			roundRobin.P99Milliseconds, adaptive.P99Milliseconds)
	}
	if adaptive.RemovalDelayRequests == nil || *adaptive.RemovalDelayRequests != 200 {
		t.Fatalf("unexpected removal delay: %+v", adaptive.RemovalDelayRequests)
	}
	if adaptive.RecoveryDelayRequests == nil || *adaptive.RecoveryDelayRequests != 300 {
		t.Fatalf("unexpected recovery delay: %+v", adaptive.RecoveryDelayRequests)
	}
}
