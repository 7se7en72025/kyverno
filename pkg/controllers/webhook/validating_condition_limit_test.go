package webhook

import (
	"fmt"
	"testing"

	policiesv1beta1 "github.com/kyverno/api/api/policies.kyverno.io/v1beta1"
	"github.com/kyverno/kyverno/pkg/config"
	engineapi "github.com/kyverno/kyverno/pkg/engine/api"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestValidatingPolicyWebhookMatchConditionLimit(t *testing.T) {
	for _, tt := range []struct {
		name         string
		sourceCount  int
		group        string
		resource     string
		failure      admissionregistrationv1.FailurePolicyType
		wantOffload  int
		wantWithBoot int
	}{
		{"autogen fits with bootstrap exclusion", 21, "", "pods", admissionregistrationv1.Fail, 63, 64},
		{"autogen exceeds condition limit", 22, "", "pods", admissionregistrationv1.Fail, 0, 1},
		{"autogen exceeds condition limit with Ignore", 22, "", "pods", admissionregistrationv1.Ignore, 0, 0},
		{"Fail needs room for bootstrap exclusion", 64, "apps", "deployments", admissionregistrationv1.Fail, 0, 1},
		{"Ignore can use all slots", 64, "apps", "deployments", admissionregistrationv1.Ignore, 64, 64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conditions := make([]admissionregistrationv1.MatchCondition, tt.sourceCount)
			for i := range conditions {
				conditions[i] = admissionregistrationv1.MatchCondition{
					Name:       fmt.Sprintf("name-%d", i),
					Expression: fmt.Sprintf("object.metadata.name != 'skip-%d'", i),
				}
			}
			policy := &policiesv1beta1.ValidatingPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "check-names"},
				Spec: policiesv1beta1.ValidatingPolicySpec{
					FailurePolicy:   ptr.To(tt.failure),
					MatchConditions: conditions,
					MatchConstraints: &admissionregistrationv1.MatchResources{
						ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
							RuleWithOperations: admissionregistrationv1.RuleWithOperations{
								Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
								Rule: admissionregistrationv1.Rule{
									APIGroups:   []string{tt.group},
									APIVersions: []string{"v1"},
									Resources:   []string{tt.resource},
								},
							},
						}},
					},
				},
			}
			expressionCache := NewExpressionCache()
			expressionCache.AddPolicyExpressions(conditions)
			webhooks := buildWebhookRules(config.NewDefaultConfiguration(false), "", config.ValidatingPolicyWebhookName,
				"/vpol", 0, nil, []engineapi.GenericPolicy{engineapi.NewValidatingPolicy(policy)}, expressionCache)

			require.Len(t, webhooks, 1)
			require.Len(t, webhooks[0].MatchConditions, tt.wantOffload)
			var resources []string
			for _, rule := range webhooks[0].Rules {
				resources = append(resources, rule.Resources...)
			}
			require.Contains(t, resources, tt.resource)
			if tt.resource == "pods" {
				require.Contains(t, resources, "deployments")
				require.Contains(t, resources, "cronjobs")
			}

			excludeBootstrapResourcesFromValidatingWebhooks(webhooks, true)
			require.Len(t, webhooks[0].MatchConditions, tt.wantWithBoot)
			require.Equal(t, tt.failure == admissionregistrationv1.Fail, hasBootstrapExclusion(webhooks[0].MatchConditions))
		})
	}
}
