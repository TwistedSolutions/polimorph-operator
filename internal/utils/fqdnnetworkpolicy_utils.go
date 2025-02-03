package utils

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	networkingv1alpha1 "github.com/TwistedSolutions/polimorph-operator/api/v1alpha1"
	"github.com/miekg/dns"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	TTL = "fqdnnetworkpolicies.twistedsolutions.se/ttl"
)

// Parse a NetworkPolicy CR from the FqdnNetworkPolicy with DNS lookups on FQDN or endpoint queries
func ParseNetworkPolicy(
	fqdnnetworkpolicy *networkingv1alpha1.FqdnNetworkPolicy) (*networking.NetworkPolicy, uint32, error) {

	var ttl uint32
	var err error

	egress := []networking.NetworkPolicyEgressRule{}
	for _, e := range fqdnnetworkpolicy.Spec.Egress {
		peers := []networking.NetworkPolicyPeer{}
		for _, peer := range e.To {
			var ips []string
			if peer.FQDN != "" {
				ips, ttl, err = lookupFqdn(peer.FQDN)
				if err != nil {
					log.Log.Error(err, "Failed to lookup FQDN", "FQDN", peer.FQDN)
					return nil, 0, err
				}
			}
			for _, ip := range ips {
				cidr := ip
				if !strings.Contains(cidr, "/") {
					cidr = cidr + "/32"
				}
				peer := networking.NetworkPolicyPeer{
					IPBlock: &networking.IPBlock{
						CIDR: cidr,
					},
				}
				peers = append(peers, peer)
			}
		}
		rule := networking.NetworkPolicyEgressRule{
			Ports: e.Ports,
			To:    peers,
		}
		egress = append(egress, rule)
	}

	net := &networking.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fqdnnetworkpolicy.Name,
			Namespace: fqdnnetworkpolicy.Namespace,
			Annotations: map[string]string{
				TTL: strconv.FormatUint(uint64(ttl), 10),
			},
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: fqdnnetworkpolicy.Spec.PodSelector,
			Egress:      egress,
			PolicyTypes: []networking.PolicyType{
				"Egress",
			},
		},
	}

	return net, ttl, nil
}

func lookupFqdn(fqdn string) ([]string, uint32, error) {
	dnsServer, set := os.LookupEnv("DNS_SERVER")
	if !set {
		dnsServer = "kube-dns.kube-system.svc.cluster.local:53" // Default Kubernetes DNS FQDN
	}

	c := new(dns.Client)
	m := new(dns.Msg)

	m.SetQuestion(dns.Fqdn(fqdn), dns.TypeA)
	r, _, err := c.Exchange(m, dnsServer)
	if err != nil {
		return nil, 0, err
	}
	if r.Rcode != dns.RcodeSuccess {
		return nil, 0, fmt.Errorf("no dns records found for fqdn: %s", fqdn)
	}

	var ips []string
	var lowestTTL uint32
	for i, ans := range r.Answer {
		switch a := ans.(type) {
		case *dns.A:
			ips = append(ips, a.A.String())
			if i == 0 || a.Hdr.Ttl < lowestTTL {
				lowestTTL = a.Hdr.Ttl
			}
		case *dns.CNAME:
			if i == 0 || a.Hdr.Ttl < lowestTTL {
				lowestTTL = a.Hdr.Ttl
			}
		}
	}

	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("no a records found for fqdn: %s", fqdn)
	}

	sort.Slice(ips, func(i, j int) bool {
		ip1 := net.ParseIP(ips[i])
		ip2 := net.ParseIP(ips[j])
		return bytes.Compare(ip1, ip2) < 0
	})

	return ips, lowestTTL, nil
}

/* func queryEndpoint(endpoint string, jsonPaths []string) ([]string, error) {
	resp, err := http.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch endpoint %s, status code: %d", endpoint, resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal(body, &jsonData); err != nil {
		return nil, err
	}

	var ips []string
	for _, path := range jsonPaths {
		values, err := extractValuesFromJSON(jsonData, path)
		if err != nil {
			return nil, err
		}
		ips = append(ips, values...)
	}

	return ips, nil
}

func extractValuesFromJSON(jsonData map[string]interface{}, path string) ([]string, error) {
	values, err := jsonpath.Read(jsonData, path)
	if err != nil {
		return nil, fmt.Errorf("error extracting values from path %s: %v", path, err)
	}

	ipList, ok := values.([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid data format at path %s, expected list of strings", path)
	}

	ips := make([]string, len(ipList))
	for i, v := range ipList {
		ip, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("invalid value at path %s, expected string", path)
		}
		ips[i] = ip
	}

	return ips, nil
} */
