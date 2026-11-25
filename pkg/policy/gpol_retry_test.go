package policy

import (
	"errors"
	"testing"

	"github.com/go-logr/logr"
	policiesv1beta1 "github.com/kyverno/api/api/policies.kyverno.io/v1beta1"
	"github.com/kyverno/kyverno/pkg/clients/dclient"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestGenerateExistingRetriesIncompleteTriggerList(t *testing.T) {
	version := schema.GroupVersion{Version: "v1"}
	pods := version.WithResource("pods")
	configMaps := version.WithResource("configmaps")
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{version})
	mapper.Add(version.WithKind("Pod"), meta.RESTScopeNamespace)
	mapper.Add(version.WithKind("ConfigMap"), meta.RESTScopeNamespace)

	pod := podNamed("trigger-pod")
	configMap := podNamed("trigger-config")
	configMap.SetKind("ConfigMap")
	policy := &policiesv1beta1.GeneratingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "generate-from-existing"},
		Spec: policiesv1beta1.GeneratingPolicySpec{
			MatchConstraints: &admissionregistrationv1.MatchResources{
				ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionregistrationv1.RuleWithOperations{
						Rule: admissionregistrationv1.Rule{
							APIGroups: []string{""}, APIVersions: []string{"v1"},
							Resources: []string{"pods", "configmaps"},
						},
					},
				}},
			},
		},
	}

	tests := []struct {
		name    string
		listErr error
		retry   bool
	}{
		{"throttled", apierrors.NewTooManyRequests("busy", 1), true},
		{"timeout", apierrors.NewTimeoutError("busy", 1), true},
		{"server timeout", apierrors.NewServerTimeout(configMaps.GroupResource(), "list", 1), true},
		{"service unavailable", apierrors.NewServiceUnavailable("busy"), true},
		{"forbidden", apierrors.NewForbidden(configMaps.GroupResource(), "", errors.New("missing list permission")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := dclient.NewFakeClient(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				pods: "PodList", configMaps: "ConfigMapList",
			}, pod.DeepCopy(), configMap.DeepCopy())
			require.NoError(t, err)
			client.SetDiscovery(dclient.NewFakeDiscoveryClient([]schema.GroupVersionResource{pods, configMaps}))
			dynamicClient := client.GetDynamicInterface().(*dynamicfake.FakeDynamicClient)
			listErr := tt.listErr
			dynamicClient.PrependReactor("list", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
				return listErr != nil, nil, listErr
			})
			generator := &captureURGenerator{}
			pc := &policyController{client: client, restMapper: mapper, log: logr.Discard(), urGenerator: generator}

			err = pc.handleGenerateExisting(policy)
			if tt.retry {
				require.ErrorIs(t, err, tt.listErr)
				require.Empty(t, generator.captured, "an incomplete scan should not enqueue the pod")
				listErr = nil
				require.NoError(t, pc.handleGenerateExisting(policy))
				require.Len(t, generator.captured, 1)
				require.Len(t, generator.captured[0].Spec.RuleContext, 2)
			} else {
				require.NoError(t, err)
				require.Len(t, generator.captured, 1)
				require.Len(t, generator.captured[0].Spec.RuleContext, 1)
				require.Equal(t, "trigger-pod", generator.captured[0].Spec.RuleContext[0].Trigger.Name)
			}
		})
	}
}
