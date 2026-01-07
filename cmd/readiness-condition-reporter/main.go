package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// HealthResponse represents the health check response structure
type HealthResponse struct {
	Healthy bool   `json:"healthy"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func main() {
	// Get configuration from environment
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		fmt.Fprintf(os.Stderr, "NODE_NAME environment variable not set\n")
		os.Exit(1)
	}

	conditionType := os.Getenv("CONDITION_TYPE")
	if conditionType == "" {
		fmt.Fprintf(os.Stderr, "CONDITION_TYPE environment variable not set\n")
		os.Exit(1)
	}

	checkEndpoint := os.Getenv("CHECK_ENDPOINT")
	if checkEndpoint == "" {
		fmt.Fprintf(os.Stderr, "CHECK_ENDPOINT environment variable not set\n")
		os.Exit(1)
	}

	checkInterval := os.Getenv("CHECK_INTERVAL")
	interval := 30 * time.Second // Default interval
	if checkInterval != "" {
		parsedInterval, err := time.ParseDuration(checkInterval)
		if err == nil {
			interval = parsedInterval
		}
	}

	// Create Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create in-cluster config: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create client: %v\n", err)
		os.Exit(1)
	}

	// Main loop to check health and update condition
	for {
		// Check health
		health, err := checkHealth(checkEndpoint)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
			// Report unhealthy on error
			health = &HealthResponse{
				Healthy: false,
				Reason:  "HealthCheckFailed",
				Message: fmt.Sprintf("Health check failed: %v", err),
			}
		}

		// Update node condition
		err = updateNodeCondition(clientset, nodeName, conditionType, health)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update node condition: %v\n", err)
		}

		// Wait for next check
		time.Sleep(interval)
	}
}

// checkHealth performs an HTTP request to check component health
func checkHealth(endpoint string) (*HealthResponse, error) {
	resp, err := http.Get(endpoint)
	if err != nil {
		return &HealthResponse{
			Healthy: false,
			Reason:  "EndpointConnectionError",
			Message: fmt.Sprintf("Failed to reach endpoint %s: %v", endpoint, err),
		}, nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return &HealthResponse{
			Healthy: true,
			Reason:  "EndpointOK",
			Message: fmt.Sprintf("Endpoint reports ready at %s", endpoint),
		}, nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	bodyString := ""
	if err == nil {
		bodyString = string(bodyBytes)
	}
	return &HealthResponse{
		Healthy: false,
		Reason:  "EndpointNotReady",
		Message: fmt.Sprintf("Endpoint returned non-2xx status at %s: %s", endpoint, bodyString),
	}, nil
}

// updateNodeCondition updates the node condition based on health check
func updateNodeCondition(client kubernetes.Interface, nodeName, conditionType string, health *HealthResponse) error {
	// Get the node
	node, err := client.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Create new condition
	now := metav1.NewTime(time.Now())
	status := corev1.ConditionFalse
	if health.Healthy {
		status = corev1.ConditionTrue
	}

	// Find existing condition to preserve transition time if status hasn't changed
	var transitionTime metav1.Time
	for _, condition := range node.Status.Conditions {
		if string(condition.Type) == conditionType {
			if condition.Status == status {
				transitionTime = condition.LastTransitionTime
			}
			break
		}
	}

	if transitionTime.IsZero() {
		transitionTime = now
	}

	// Create condition
	condition := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(conditionType),
		Status:             status,
		LastHeartbeatTime:  now,
		LastTransitionTime: transitionTime,
		Reason:             health.Reason,
		Message:            health.Message,
	}

	// Update node status
	found := false
	for i, c := range node.Status.Conditions {
		if string(c.Type) == conditionType {
			node.Status.Conditions[i] = condition
			found = true
			break
		}
	}

	if !found {
		node.Status.Conditions = append(node.Status.Conditions, condition)
	}

	_, err = client.CoreV1().Nodes().UpdateStatus(context.TODO(), node, metav1.UpdateOptions{})
	return err
}
