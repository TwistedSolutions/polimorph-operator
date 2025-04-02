package morph

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	polimorphv1 "github.com/TwistedSolutions/polimorph-operator/api/v1"
	"github.com/miekg/dns"
	"github.com/yalp/jsonpath"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Cache entry structure
type dnsCacheEntry struct {
	ips       map[string]time.Time
	cacheTime time.Duration
}

var (
	cache      = make(map[string]dnsCacheEntry)
	cacheMutex = sync.RWMutex{}
)

func logCacheState(fqdn string) {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()
	if entry, found := cache[fqdn]; found {
		log.Log.V(1).Info("Cache state for FQDN", "fqdn", fqdn, "cache", entry.ips)
	} else {
		log.Log.V(1).Info("No cache entry found for FQDN", "fqdn", fqdn)
	}
}

// Parse a NetworkPolicy CR from the FqdnNetworkPolicy with DNS lookups on FQDN or endpoint queries
func ParseNetworkPolicy(
	polimorphpolicy *polimorphv1.PoliMorphPolicy) (*networking.NetworkPolicy, uint32, error) {

	var ttl uint32
	var err error

	egress := []networking.NetworkPolicyEgressRule{}
	for _, e := range polimorphpolicy.Spec.Egress {
		peers := []networking.NetworkPolicyPeer{}
		for _, peer := range e.To {
			var ips []string
			if peer.FQDN != "" {
				ips, ttl, err = getIPsWithCache(peer.FQDN, time.Duration(*polimorphpolicy.Spec.Cache)*time.Minute)
				if err != nil {
					log.Log.Error(err, "Failed to lookup FQDN", "FQDN", peer.FQDN)
					return nil, 0, err
				}
			} else if peer.Endpoint != "" {
				ips, err = queryEndpoint(peer.Endpoint, peer.JSONPaths)
				if err != nil {
					log.Log.Error(err, "Failed to query endpoint", "Endpoint", peer.Endpoint)
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
			Name:      polimorphpolicy.Name,
			Namespace: polimorphpolicy.Namespace,
		},
		Spec: networking.NetworkPolicySpec{
			PodSelector: polimorphpolicy.Spec.PodSelector,
			Egress:      egress,
			PolicyTypes: []networking.PolicyType{
				"Egress",
			},
		},
	}

	return net, ttl, nil
}

func performDnsLookup(fqdn string) ([]string, uint32, error) {
	dnsServer, set := os.LookupEnv("DNS_SERVER")
	if !set {
		dnsServer = "kube-dns.kube-system.svc.cluster.local:53"
	}

	ipSet := make(map[string]struct{})
	var minTTL uint32

	c := new(dns.Client)
	visited := make(map[string]bool)

	if err := resolveFQDN(c, fqdn, dnsServer, ipSet, &minTTL, visited); err != nil {
		return nil, 0, err
	}

	ips := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	return ips, minTTL, nil
}

func getIPsWithCache(fqdn string, cacheTime time.Duration) ([]string, uint32, error) {
	logCacheState(fqdn)

	// If caching is disabled, resolve DNS directly
	if cacheTime == 0 {
		return performDnsLookup(fqdn)
	}

	cacheMutex.RLock()
	entry, found := cache[fqdn]
	cacheMutex.RUnlock()

	currentTime := time.Now()
	validIPs := make(map[string]bool)

	if found {
		for ip, timestamp := range entry.ips {
			if currentTime.Sub(timestamp) < entry.cacheTime {
				validIPs[ip] = true
			}
		}
	}

	ips, minTTL, err := performDnsLookup(fqdn)
	if err != nil {
		if len(validIPs) > 0 {
			cachedIPs := make([]string, 0, len(validIPs))
			for ip := range validIPs {
				cachedIPs = append(cachedIPs, ip)
			}
			sort.Strings(cachedIPs)
			log.Log.Info("Returning cached IPs due to DNS resolution failure", "fqdn", fqdn, "ips", cachedIPs)
			return cachedIPs, minTTL, nil
		}
		return nil, 0, err
	}

	// Update cache with new IPs
	expiration := time.Now().Add(cacheTime)
	cacheMutex.Lock()
	if _, exists := cache[fqdn]; !exists {
		cache[fqdn] = dnsCacheEntry{ips: make(map[string]time.Time), cacheTime: cacheTime}
	}
	for _, ip := range ips {
		cache[fqdn].ips[ip] = expiration
		validIPs[ip] = true
	}
	cacheMutex.Unlock()

	// Log updated cache state
	logCacheState(fqdn)

	// Merge valid cached IPs with newly resolved IPs
	resultIPs := make([]string, 0, len(validIPs))
	for ip := range validIPs {
		resultIPs = append(resultIPs, ip)
	}
	sort.Strings(resultIPs)

	return resultIPs, minTTL, nil
}

func resolveFQDN(
	c *dns.Client,
	fqdn string,
	server string,
	ipSet map[string]struct{},
	minTTL *uint32,
	visited map[string]bool,
) error {
	if visited[fqdn] {
		return nil
	}
	visited[fqdn] = true

	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), dns.TypeA)
	m.RecursionDesired = true

	resp, _, err := c.Exchange(m, server)
	if err != nil {
		return fmt.Errorf("DNS query failed for %s: %v", fqdn, err)
	}

	if resp.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("DNS query for %s not successful (Rcode=%d)", fqdn, resp.Rcode)
	}

	for _, rr := range append(resp.Answer, resp.Extra...) {
		switch rr.Header().Rrtype {
		case dns.TypeA:
			ipSet[rr.(*dns.A).A.String()] = struct{}{}
			updateMinTTL(minTTL, rr.Header().Ttl)
		case dns.TypeCNAME:
			err = resolveFQDN(c, rr.(*dns.CNAME).Target, server, ipSet, minTTL, visited)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func updateMinTTL(minTTL *uint32, newTTL uint32) {
	if *minTTL == 0 || newTTL < *minTTL {
		*minTTL = newTTL
	}
}

func queryEndpoint(endpoint string, jsonPaths []string) ([]string, error) {
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
}
