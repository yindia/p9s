package kyverno

import (
	"testing"

	"github.com/yindia/p9s/internal/model"
)

func TestClassicKind(t *testing.T) {
	tests := []struct {
		in   string
		want model.MatchRule
		ok   bool
	}{
		{"Pod", model.MatchRule{Group: "*", Kind: "Pod"}, true},
		{"v1/Pod", model.MatchRule{Group: "", Kind: "Pod"}, true},
		{"v2beta1/HorizontalPodAutoscaler", model.MatchRule{Group: "", Kind: "HorizontalPodAutoscaler"}, true},
		{"apps/v1/Deployment", model.MatchRule{Group: "apps", Kind: "Deployment"}, true},
		{"velero.io/Backup", model.MatchRule{Group: "velero.io", Kind: "Backup"}, true}, // a group, not a version
		{"vpcresources.k8s.aws/v1beta1/SecurityGroupPolicy", model.MatchRule{Group: "vpcresources.k8s.aws", Kind: "SecurityGroupPolicy"}, true},
		{"Pod/exec", model.MatchRule{}, false}, // subresource only
		{"apps/v1/Deployment/scale", model.MatchRule{}, false},
	}
	for _, tt := range tests {
		got, ok := classicKind(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("classicKind(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
