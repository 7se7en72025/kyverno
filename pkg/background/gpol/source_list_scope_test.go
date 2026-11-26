package gpol

import (
	"context"
	"testing"

	"github.com/kyverno/kyverno/pkg/background/common"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type scopedCloneListClient struct {
	MockClient
	list func(string, *metav1.LabelSelector) (*unstructured.UnstructuredList, error)
}

func (c *scopedCloneListClient) ListResource(_ context.Context, _, _, namespace string, selector *metav1.LabelSelector) (*unstructured.UnstructuredList, error) {
	return c.list(namespace, selector)
}

func TestCloneSourceEventsListTrackedNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	source := makeUnstructured("1", "", "v1", "ConfigMap", "source", "source-ns", "source-uid", nil)
	unrelated := makeUnstructured("1", "", "v1", "ConfigMap", "unrelated", "other-ns", "other-uid", nil)

	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			for _, deny := range []bool{false, true} {
				name := "both destinations"
				if deny {
					name = "one destination is forbidden"
				}
				t.Run(name, func(t *testing.T) {
					cache := map[types.UID]Resource{}
					var clones []*unstructured.Unstructured
					for _, ref := range []struct{ namespace, name string }{
						{"team-a", "first"}, {"team-a", "second"}, {"team-b", "third"},
					} {
						clone := makeUnstructured("2", "", "v1", "ConfigMap", ref.name, ref.namespace, types.UID(ref.name),
							map[string]string{common.GenerateSourceUIDLabel: string(source.GetUID())})
						clones = append(clones, clone)
						cache[clone.GetUID()] = Resource{Name: ref.name, Namespace: ref.namespace, Labels: clone.GetLabels(), Data: clone}
					}

					var listNamespaces []string
					client := &scopedCloneListClient{list: func(namespace string, selector *metav1.LabelSelector) (*unstructured.UnstructuredList, error) {
						listNamespaces = append(listNamespaces, namespace)
						if deny && namespace == "team-a" {
							return nil, apierrors.NewForbidden(gvr.GroupResource(), "", nil)
						}
						list := &unstructured.UnstructuredList{}
						for _, clone := range clones {
							if (namespace == "" || clone.GetNamespace() == namespace) &&
								clone.GetLabels()[common.GenerateSourceUIDLabel] == selector.MatchLabels[common.GenerateSourceUIDLabel] {
								list.Items = append(list.Items, *clone.DeepCopy())
							}
						}
						return list, nil
					}}
					wm := &WatchManager{client: client, dynamicWatchers: map[schema.GroupVersionResource]*watcher{
						gvr: {metadataCache: cache},
					}}
					handle := wm.handleUpdate
					if operation == "delete" {
						handle = wm.handleDelete
					}
					handle(source, gvr)
					require.ElementsMatch(t, []string{"team-a", "team-b"}, listNamespaces)
					wantWrites := 3
					if deny {
						wantWrites = 1
					}
					if operation == "update" {
						require.Len(t, client.updated, wantWrites)
						require.Empty(t, client.deleted)
					} else {
						require.Len(t, client.deleted, wantWrites)
						require.Empty(t, client.updated)
					}

					listNamespaces = nil
					handle(unrelated, gvr)
					require.Empty(t, listNamespaces)
				})
			}
		})
	}
}
