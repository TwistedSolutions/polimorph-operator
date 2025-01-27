package checkpoint

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/google/uuid"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networking "k8s.io/api/networking/v1"
)

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
	http.HandleFunc("/checkpoint", checkpointConfigHandler(k8sClient))
	go func() {
		port := os.Getenv("CHECKPOINT_API_PORT")
		if port == "" {
			port = "8080"
		}
		log.Info("Starting Checkpoint API", "port", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
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

		ctx := context.Background()

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

		// Marshal the policies to checkpoint-compatible checkpointObject list
		var objects []checkpointObject
		for _, policy := range policies.Items {
			// Generate a unique ID for the policy
			id := generateUIDFromData(policy.Name + policy.Namespace)
			name := policy.Name
			description := "Network Policy for " + name
			var ranges []string
			for _, egressRule := range policy.Spec.Egress {
				for _, to := range egressRule.To {
					if to.IPBlock != nil {
						ranges = append(ranges, to.IPBlock.CIDR)
					}
				}
			}
			objects = append(objects, checkpointObject{
				Name:        name,
				ID:          id,
				Description: description,
				Ranges:      ranges,
			})
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
