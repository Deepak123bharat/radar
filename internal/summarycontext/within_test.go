package summarycontext

import (
	"testing"
	"time"
)

// Search must not wait on a cold build of its per-hit context: after a
// restart it reads custom resources that wait on custom-resource discovery
// for tens of seconds.
func TestWithinDoesNotWaitPastBudget(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	got := Within(50*time.Millisecond, func() *int {
		<-release
		one := 1
		return &one
	})
	if got != nil || time.Since(start) > time.Second {
		t.Fatalf("got %v after %s; want nil within the budget", got, time.Since(start))
	}
	if got := Within(time.Second, func() int { return 7 }); got != 7 {
		t.Fatalf("a result ready in time was dropped: %d", got)
	}
}
