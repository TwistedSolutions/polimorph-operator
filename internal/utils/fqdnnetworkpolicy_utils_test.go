package utils

import (
	"os"
	"testing"

	networkingv1alpha1 "github.com/TwistedSolutions/polimorph-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Utils Suite")
}

var _ = Describe("lookupFqdn", func() {
	Context("With DNS_SERVER environment variable set", func() {
		BeforeEach(func() {
			os.Setenv("DNS_SERVER", "8.8.8.8:53")
		})

		AfterEach(func() {
			os.Unsetenv("DNS_SERVER")
		})

		It("should use the DNS server from the environment variable", func() {
			fqdn := "google.com"
			ips, _, err := lookupFqdn(fqdn)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(ips)).Should(BeNumerically(">", 0))
		})
		It("should return an error", func() {
			fqdn := "invalid.fqdn"
			_, _, err := lookupFqdn(fqdn)
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("ParseNetworkPolicy", func() {
	Context("With valid FqdnNetworkPolicy", func() {
		BeforeEach(func() {
			os.Setenv("DNS_SERVER", "8.8.8.8:53")
		})

		AfterEach(func() {
			os.Unsetenv("DNS_SERVER")
		})

		It("should return a NetworkPolicy and no error", func() {
			protocol := v1.ProtocolTCP
			fqdnNetworkPolicy := &networkingv1alpha1.FqdnNetworkPolicy{
				Spec: networkingv1alpha1.FqdnNetworkPolicySpec{
					Egress: []networkingv1alpha1.FqdnNetworkPolicyEgressRule{
						{
							Ports: []networking.NetworkPolicyPort{
								{
									Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 80},
									Protocol: &protocol,
								},
							},
							To: []networkingv1alpha1.FqdnNetworkPolicyPeer{
								{
									FQDN: "google.com",
								},
							},
						},
					},
				},
			}
			netPolicy, ttl, err := ParseNetworkPolicy(fqdnNetworkPolicy)
			Expect(err).NotTo(HaveOccurred())
			Expect(ttl).Should(BeNumerically(">", 0))
			Expect(netPolicy).Should(BeAssignableToTypeOf(&networking.NetworkPolicy{}))
		})
	})

	Context("With invalid FqdnNetworkPolicy", func() {
		BeforeEach(func() {
			os.Setenv("DNS_SERVER", "8.8.8.8:53")
		})

		AfterEach(func() {
			os.Unsetenv("DNS_SERVER")
		})

		It("should return an error", func() {
			fqdnNetworkPolicy := &networkingv1alpha1.FqdnNetworkPolicy{
				Spec: networkingv1alpha1.FqdnNetworkPolicySpec{
					Egress: []networkingv1alpha1.FqdnNetworkPolicyEgressRule{
						{
							Ports: []networking.NetworkPolicyPort{
								{
									Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 80},
								},
							},
							To: []networkingv1alpha1.FqdnNetworkPolicyPeer{
								{
									FQDN: "invalid.fqdn",
								},
							},
						},
					},
				},
			}
			_, _, err := ParseNetworkPolicy(fqdnNetworkPolicy)
			Expect(err).To(HaveOccurred())
		})
	})
})
