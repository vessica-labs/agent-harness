package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type architectureReconciliation struct {
	Status            string `json:"status"`
	TicketConstraints []struct {
		TicketKey              string   `json:"ticket_key"`
		RequiredOwnedPaths     []string `json:"required_owned_paths"`
		AdditionalDependencies []string `json:"additional_dependencies"`
	} `json:"ticket_constraints"`
}

func (r *Runner) reconcileArchitecture(ctx context.Context, architecturePath string) error {
	architecture, err := os.ReadFile(architecturePath)
	if err != nil {
		return err
	}
	productPath := filepath.Join(r.runDir, "agent-output", "product.json")
	product, err := os.ReadFile(productPath)
	if err != nil {
		return err
	}
	updatedProduct, ticketPlan, changed, err := applyArchitectureConstraints(product, architecture)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := os.WriteFile(productPath, updatedProduct, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"), ticketPlan, 0o600); err != nil {
		return err
	}
	if _, err := r.harness(ctx, r.repo, "validate-agent-output", "--agent", "product", "--input", productPath); err != nil {
		return fmt.Errorf("validate architect-reconciled ticket plan: %w", err)
	}
	return nil
}

func applyArchitectureConstraints(productBody, architectureBody []byte) ([]byte, []byte, bool, error) {
	var architecture architectureReconciliation
	if err := json.Unmarshal(architectureBody, &architecture); err != nil {
		return nil, nil, false, err
	}
	if architecture.Status != "ready" {
		return productBody, nil, false, nil
	}
	var product map[string]any
	if err := json.Unmarshal(productBody, &product); err != nil {
		return nil, nil, false, err
	}
	tickets, ok := product["tickets"].([]any)
	if !ok {
		return nil, nil, false, errors.New("product output has no ticket plan")
	}
	byKey := make(map[string]map[string]any, len(tickets))
	for _, raw := range tickets {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, false, errors.New("product ticket is not an object")
		}
		key, _ := entry["key"].(string)
		byKey[key] = entry
	}
	changed := false
	for _, constraint := range architecture.TicketConstraints {
		entry, ok := byKey[constraint.TicketKey]
		if !ok {
			return nil, nil, false, fmt.Errorf("architect constraint references unknown ticket %s", constraint.TicketKey)
		}
		for field, additions := range map[string][]string{
			"owned_paths": constraint.RequiredOwnedPaths,
			"depends_on":  constraint.AdditionalDependencies,
		} {
			values, err := stringValues(entry[field])
			if err != nil {
				return nil, nil, false, fmt.Errorf("ticket %s %s: %w", constraint.TicketKey, field, err)
			}
			seen := map[string]bool{}
			for _, value := range values {
				seen[value] = true
			}
			for _, addition := range additions {
				if addition != "" && !seen[addition] {
					values = append(values, addition)
					seen[addition], changed = true, true
				}
			}
			entry[field] = values
		}
	}
	updatedProduct, err := json.MarshalIndent(product, "", "  ")
	if err != nil {
		return nil, nil, false, err
	}
	ticketPlan, err := json.MarshalIndent(tickets, "", "  ")
	if err != nil {
		return nil, nil, false, err
	}
	return append(updatedProduct, '\n'), append(ticketPlan, '\n'), changed, nil
}

func stringValues(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return append([]string(nil), strings...), nil
		}
		return nil, errors.New("must be a string array")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("must contain only strings")
		}
		result = append(result, text)
	}
	return result, nil
}
