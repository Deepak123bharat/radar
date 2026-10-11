package search

import (
	"context"
	"errors"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// NamespacedSearchKinds lists the sensitive typed kinds requiring exact caller RBAC.
var NamespacedSearchKinds = []struct{ Kind, Group, Resource string }{
	{"Secret", "", "secrets"},
	{"Role", "rbac.authorization.k8s.io", "roles"},
	{"RoleBinding", "rbac.authorization.k8s.io", "rolebindings"},
}

func (r *Result) addGap(kind, group string, reason readreason.Code, namespaces []string) {
	r.Partial = true
	for i := range r.Unsearched {
		existing := &r.Unsearched[i]
		if existing.Kind == kind && existing.Group == group && existing.Reason == reason {
			existing.Namespaces = mergeNamespaces(existing.Namespaces, namespaces)
			return
		}
	}
	r.Unsearched = append(r.Unsearched, UnsearchedKind{kind, group, reason, mergeNamespaces(nil, namespaces)})
}

func mergeNamespaces(into, add []string) []string {
	for _, ns := range add {
		if !slices.Contains(into, ns) {
			into = append(into, ns)
		}
	}
	slices.Sort(into)
	return into
}

// ScopeNamespaces sets the namespace fields of o for q. visible is what the
// caller may search after any namespace selection (a saved pick or
// --namespace-scope): nil means every namespace, empty means none. ceiling is
// the caller's visibility without that selection. A selection narrower than
// the ceiling is reported only for queries without ns: terms; with terms, its
// effect shows up as the requested namespaces it excluded.
func (o *Options) ScopeNamespaces(q Query, visible, ceiling []string) {
	o.Namespaces = visible
	o.ExcludedNamespaces = nil
	o.ScopedNamespaces = nil
	if len(q.NSFilter) > 0 {
		o.Namespaces = make([]string, 0, len(q.NSFilter))
		for _, ns := range q.NSFilter {
			switch {
			case visible == nil || slices.Contains(visible, ns):
				if !slices.Contains(o.Namespaces, ns) {
					o.Namespaces = append(o.Namespaces, ns)
				}
			case !slices.Contains(o.ExcludedNamespaces, ns):
				o.ExcludedNamespaces = append(o.ExcludedNamespaces, ns)
			}
		}
	}
	o.NamespaceExcluded = o.Namespaces != nil && len(o.Namespaces) == 0
	if !o.NamespaceExcluded && len(q.NSFilter) == 0 && !sameNamespaces(visible, ceiling) {
		o.ScopedNamespaces = slices.Sorted(slices.Values(visible))
	}
}

func sameNamespaces(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// listErrorReason classifies a failed collector list or warm-up. The typed
// cache reports a probe-disabled kind as a "forbidden:" error, and
// ErrUnknownKind marks a kind Radar reads directly instead of watching.
func listErrorReason(err error) readreason.Code {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || strings.HasPrefix(err.Error(), "forbidden:") {
		return readreason.CollectorAccessDenied
	}
	if errors.Is(err, k8s.ErrUnknownKind) {
		return readreason.CollectionNotSupported
	}
	if errors.Is(err, k8s.ErrDynamicNotReady) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return readreason.KindSyncPending
	}
	return readreason.ReadError
}

func dynamicObservationReason(observation k8score.DynamicResourceObservation) readreason.Code {
	switch observation.State {
	case k8score.DynamicObservationSynced:
		return ""
	case k8score.DynamicObservationDenied:
		return readreason.CollectorAccessDenied
	case k8score.DynamicObservationSyncing:
		if observation.ReasonCode == "sync_stalled" {
			return readreason.KindSyncFailed
		}
		return readreason.KindSyncPending
	case k8score.DynamicObservationDeferred:
		if observation.ReasonCode == "scope_probe_incomplete" {
			return readreason.KindSyncPending
		}
		return readreason.KindNotWatched
	case k8score.DynamicObservationUnsupported:
		return readreason.CollectionNotSupported
	default:
		return readreason.KindNotWatched
	}
}
