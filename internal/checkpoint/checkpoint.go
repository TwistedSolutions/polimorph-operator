package checkpoint

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/google/uuid"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networking "k8s.io/api/networking/v1"
)

var privateNets []*net.IPNet

func init() {
	for _, block := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		_, net, err := net.ParseCIDR(block)
		if err == nil {
			privateNets = append(privateNets, net)
		}
	}
}

type checkpointObject struct {
	Name        string   `json:"name"`
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Ranges      []string `json:"ranges"`
}

type checkpointConfig struct {
	Version     string             `json:"version"`
	Description string             `json:"description"`
	Objects     []checkpointObject `json:"objects"`
}

// IsCheckpointFeatureEnabled checks if the CHECKPOINT_FEATURE is enabled.
func isCheckpointFeatureEnabled() bool {
	value := os.Getenv("CHECKPOINT_FEATURE")
	return value == "true"
}

// generateUIDFromData generates a deterministic UID based on input data.
func generateUIDFromData(data string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(data)).String()
}

// SetupCheckpointAPI initializes the API endpoint for the Checkpoint feature if enabled.
func SetupCheckpointAPI(k8sClient client.Client) {
	if !isCheckpointFeatureEnabled() {
		return
	}

	log := log.Log.WithName("SetupCheckpointAPI")
	mux := http.NewServeMux()
	mux.HandleFunc("/checkpoint", checkpointConfigHandler(k8sClient))

	port := os.Getenv("CHECKPOINT_API_PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("Starting Checkpoint API", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "Failed to start Checkpoint API")
			panic("Failed to start Checkpoint API: " + err.Error())
		}
	}()
}

// CheckpointConfigHandler serves the Checkpoint-compatible JSON configuration.
func checkpointConfigHandler(k8sClient client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := log.FromContext(r.Context())

		// Extract query parameters
		name := r.URL.Query().Get("name")
		namespace := r.URL.Query().Get("namespace")

		// Prepare a list of network policies
		policies := &networking.NetworkPolicyList{}

		ctx := r.Context()

		if namespace != "" {
			if err := k8sClient.List(ctx, policies, client.InNamespace(namespace)); err != nil {
				http.Error(w, "Failed to list NetworkPolicies", http.StatusInternalServerError)
				logger.Error(err, "Failed to list NetworkPolicies")
				return
			}
		} else {
			if err := k8sClient.List(ctx, policies); err != nil {
				http.Error(w, "Failed to list NetworkPolicies", http.StatusInternalServerError)
				logger.Error(err, "Failed to list NetworkPolicies")
				return
			}
		}

		if name != "" {
			var filtered []networking.NetworkPolicy
			for _, policy := range policies.Items {
				if policy.Name == name {
					filtered = append(filtered, policy)
				}
			}
			policies.Items = filtered
		}

		mode := r.URL.Query().Get("mode")
		var objects []checkpointObject

		switch mode {
		case "policy":
			// One checkpointObject per NetworkPolicy (if it has IPBlock ranges)
			for _, policy := range policies.Items {
				var ranges []string
				for _, egressRule := range policy.Spec.Egress {
					for _, to := range egressRule.To {
						if to.IPBlock != nil {
							ranges = append(ranges, to.IPBlock.CIDR)
						}
					}
				}
				if len(ranges) > 0 {
					slices.Sort(ranges)
					objects = append(objects, checkpointObject{
						Name:        policy.Name,
						ID:          generateUIDFromData(policy.Name + policy.Namespace),
						Description: "Network Policy for " + policy.Name,
						Ranges:      ranges,
					})
				}
			}

		case "internalexternal":
			// Aggregate all CIDRs into internal vs. public sets (with deduplication)
			internalSet := make(map[string]struct{})
			publicSet := make(map[string]struct{})
			for _, policy := range policies.Items {
				for _, egressRule := range policy.Spec.Egress {
					for _, to := range egressRule.To {
						if to.IPBlock != nil {
							cidr := to.IPBlock.CIDR
							if isInternalCIDR(cidr) {
								internalSet[cidr] = struct{}{}
							} else {
								publicSet[cidr] = struct{}{}
							}
						}
					}
				}
			}
			var internalRanges, publicRanges []string
			for cidr := range internalSet {
				internalRanges = append(internalRanges, cidr)
			}
			for cidr := range publicSet {
				publicRanges = append(publicRanges, cidr)
			}
			if len(internalRanges) > 0 {
				slices.Sort(internalRanges)
				objects = append(objects, checkpointObject{
					Name:        "Internal IPs",
					ID:          generateUIDFromData("InternalIPs"),
					Description: "Aggregated internal IPs from network policies",
					Ranges:      internalRanges,
				})
			}
			if len(publicRanges) > 0 {
				slices.Sort(publicRanges)
				objects = append(objects, checkpointObject{
					Name:        "Public IPs",
					ID:          generateUIDFromData("PublicIPs"),
					Description: "Aggregated public IPs from network policies",
					Ranges:      publicRanges,
				})
			}

		case "namespace":
			// Group by namespace and aggregate IPBlock CIDRs per namespace (deduplicated)
			nsMap := make(map[string]map[string]struct{})
			for _, policy := range policies.Items {
				ns := policy.Namespace
				if nsMap[ns] == nil {
					nsMap[ns] = make(map[string]struct{})
				}
				for _, egressRule := range policy.Spec.Egress {
					for _, to := range egressRule.To {
						if to.IPBlock != nil {
							nsMap[ns][to.IPBlock.CIDR] = struct{}{}
						}
					}
				}
			}
			for ns, cidrSet := range nsMap {
				var ranges []string
				for cidr := range cidrSet {
					ranges = append(ranges, cidr)
				}
				if len(ranges) > 0 {
					slices.Sort(ranges)
					objects = append(objects, checkpointObject{
						Name:        ns,
						ID:          generateUIDFromData(ns),
						Description: "Aggregated IPs from network policies in namespace " + ns,
						Ranges:      ranges,
					})
				}
			}

		case "all":
			// Aggregate all CIDRs from all policies into a deduplicated set
			allSet := make(map[string]struct{})
			for _, policy := range policies.Items {
				for _, egressRule := range policy.Spec.Egress {
					for _, to := range egressRule.To {
						if to.IPBlock != nil {
							allSet[to.IPBlock.CIDR] = struct{}{}
						}
					}
				}
			}
			var allRanges []string
			for cidr := range allSet {
				allRanges = append(allRanges, cidr)
			}
			if len(allRanges) > 0 {
				slices.Sort(allRanges)
				objects = append(objects, checkpointObject{
					Name:        "All IPs",
					ID:          generateUIDFromData("AllIPs"),
					Description: "Aggregated IPs from all network policies",
					Ranges:      allRanges,
				})
			}

		default:
			// Fallback (e.g. same as "policy")
			for _, policy := range policies.Items {
				var ranges []string
				for _, egressRule := range policy.Spec.Egress {
					for _, to := range egressRule.To {
						if to.IPBlock != nil {
							ranges = append(ranges, to.IPBlock.CIDR)
						}
					}
				}
				if len(ranges) > 0 {
					slices.Sort(ranges)
					objects = append(objects, checkpointObject{
						Name:        policy.Name,
						ID:          generateUIDFromData(policy.Name + policy.Namespace),
						Description: "Network Policy for " + policy.Name,
						Ranges:      ranges,
					})
				}
			}
		}

		// Create a Checkpoint-compatible configuration
		config := checkpointConfig{
			Version:     "1.0",
			Description: "Network Policies",
			Objects:     objects,
		}

		// Marshal and return the configuration as JSON
		data, err := json.Marshal(config)
		if err != nil {
			http.Error(w, "Failed to marshal configuration", http.StatusInternalServerError)
			logger.Error(err, "Failed to marshal configuration")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

func isInternalCIDR(cidr string) bool {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	for _, privateNet := range privateNets {
		if privateNet.Contains(ipnet.IP) {
			return true
		}
	}
	return false
}
