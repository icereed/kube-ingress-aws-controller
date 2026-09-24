package main

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zalando-incubator/kube-ingress-aws-controller/aws"
	"github.com/zalando-incubator/kube-ingress-aws-controller/certs"
	certsfake "github.com/zalando-incubator/kube-ingress-aws-controller/certs/fake"
	"github.com/zalando-incubator/kube-ingress-aws-controller/kubernetes"
)

// Two shared stacks carry the same certificates and both have expired
// certificate TTL tags (e.g. left over from an SSL policy change followed by a
// controller upgrade). Existing ingresses are served by the "live" stack, which
// sorts after the old one by name. The live stack must be kept and refreshed,
// new ingresses must join it, and only the idle stack may be deleted.
func TestExpiredCertTTLStacksWithIngresses(t *testing.T) {
	firstRun = false
	t.Cleanup(func() { firstRun = true })

	certFinder := certsfake.NewCert([]*certs.CertificateSummary{
		certs.NewCertificate("wildcard", &x509.Certificate{DNSNames: []string{"app.example.org", "app-1120.example.org"}}, nil),
	})
	expired := time.Now().UTC().Add(-24 * time.Hour)
	stack := func(name, dns string) *aws.StackLBState {
		return &aws.StackLBState{
			Stack: &aws.Stack{
				Name:             name,
				DNSName:          dns,
				LoadBalancerType: aws.LoadBalancerTypeApplication,
				CertificateARNs:  map[string]time.Time{"wildcard": expired},
			},
			LBState: &aws.LoadBalancerState{},
		}
	}
	ingress := func(name, host, lbHostname string) *kubernetes.Ingress {
		return &kubernetes.Ingress{
			ResourceType:     kubernetes.TypeRouteGroup,
			Namespace:        "tstools",
			Name:             name,
			Shared:           true,
			LoadBalancerType: aws.LoadBalancerTypeApplication,
			Hostnames:        []string{host},
			Hostname:         lbHostname,
		}
	}

	model := buildManagedModel(certFinder, 5, time.Hour,
		[]*kubernetes.Ingress{
			ingress("new-mr-env", "app-1120.example.org", ""),
			ingress("app", "app.example.org", "live-lb.elb.amazonaws.com"),
		},
		[]*aws.StackLBState{
			stack("kube-ingress-aws-controller-cluster-bbbb", "live-lb.elb.amazonaws.com"),
			stack("kube-ingress-aws-controller-cluster-aaaa", "old-lb.elb.amazonaws.com"),
		},
		nil, "")

	byName := map[string]*loadBalancer{}
	for _, lb := range model {
		if lb.stack != nil {
			byName[lb.stack.Name] = lb
		}
	}
	live := byName["kube-ingress-aws-controller-cluster-bbbb"]
	old := byName["kube-ingress-aws-controller-cluster-aaaa"]
	require.NotNil(t, live)
	require.NotNil(t, old)

	require.Len(t, live.ingresses["wildcard"], 2, "both ingresses must land on the live stack")
	require.Empty(t, old.ingresses["wildcard"])

	require.NotEqual(t, delete, live.Status(), "a stack with ingresses must not be deleted")
	// Status() returns update for complete stacks (UPDATE_COMPLETE in the
	// field); the stack status is unexported, so assert the precondition.
	require.False(t, live.inSync(), "expired TTL tags must trigger a stack update")
	require.True(t, live.CertificateARNs()["wildcard"].IsZero(), "certificate in use must get a zero TTL")
	require.Equal(t, delete, old.Status(), "the idle stack is still cleaned up")
}
