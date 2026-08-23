package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type architectureReconciliation struct {
	Status            string `json:"status"`
	TicketConstraints []struct {
		TicketKey               string         `json:"ticket_key"`
		Constraints             []string       `json:"constraints"`
		RequiredOwnedPaths      []string       `json:"required_owned_paths"`
		AdditionalDependencies  []string       `json:"additional_dependencies"`
		RequiredFocusedChecks   []string       `json:"required_focused_checks"`
		RequiredIterationChecks []string       `json:"required_iteration_checks"`
		RequiredTicketGates     []string       `json:"required_ticket_gates"`
		RequiredPipelineGates   []pipelineGate `json:"required_pipeline_gates"`
	} `json:"ticket_constraints"`
	ApplicableADRs []string `json:"applicable_adrs"`
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
	if changed {
		if err := os.WriteFile(productPath, updatedProduct, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"), ticketPlan, 0o600); err != nil {
			return err
		}
		if _, err := r.harness(ctx, r.repo, "validate-agent-output", "--agent", "product", "--input", productPath); err != nil {
			return fmt.Errorf("validate architect-reconciled ticket plan: %w", err)
		}
	}
	return r.materializeTicketContexts(updatedProduct, architecture)
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
			"owned_paths":              constraint.RequiredOwnedPaths,
			"depends_on":               constraint.AdditionalDependencies,
			"architecture_constraints": constraint.Constraints,
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
		if _, usesTieredVerification := entry["verification"]; usesTieredVerification ||
			len(constraint.RequiredIterationChecks)+len(constraint.RequiredTicketGates)+len(constraint.RequiredPipelineGates) > 0 {
			legacyTicketChecks, err := stringValues(entry["focused_checks"])
			if err != nil {
				return nil, nil, false, fmt.Errorf("ticket %s focused_checks: %w", constraint.TicketKey, err)
			}
			verification, ok := entry["verification"].(map[string]any)
			if !ok {
				verification = map[string]any{"iteration_checks": []string{}, "ticket_gate": []string{}, "pipeline_gates": []any{}}
				entry["verification"] = verification
			}
			ticketGateAdditions := append([]string(nil), legacyTicketChecks...)
			ticketGateAdditions = append(ticketGateAdditions, constraint.RequiredTicketGates...)
			ticketGateAdditions = append(ticketGateAdditions, constraint.RequiredFocusedChecks...)
			for field, additions := range map[string][]string{
				"iteration_checks": constraint.RequiredIterationChecks,
				"ticket_gate":      ticketGateAdditions,
			} {
				values, err := stringValues(verification[field])
				if err != nil {
					return nil, nil, false, fmt.Errorf("ticket %s verification.%s: %w", constraint.TicketKey, field, err)
				}
				values, added := mergeUniqueStrings(values, additions)
				verification[field] = values
				changed = changed || added
			}
			gates, err := pipelineGateValues(verification["pipeline_gates"])
			if err != nil {
				return nil, nil, false, fmt.Errorf("ticket %s verification.pipeline_gates: %w", constraint.TicketKey, err)
			}
			gates, added := mergePipelineGates(gates, constraint.RequiredPipelineGates)
			verification["pipeline_gates"] = gates
			changed = changed || added
		} else {
			values, err := stringValues(entry["focused_checks"])
			if err != nil {
				return nil, nil, false, fmt.Errorf("ticket %s focused_checks: %w", constraint.TicketKey, err)
			}
			values, added := mergeUniqueStrings(values, constraint.RequiredFocusedChecks)
			entry["focused_checks"] = values
			changed = changed || added
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

type productContextSource struct {
	PRDMarkdown string   `json:"prd_markdown"`
	Tickets     []ticket `json:"tickets"`
	Coverage    []struct {
		Requirement string   `json:"requirement"`
		Tickets     []string `json:"tickets"`
	} `json:"coverage"`
}

type ticketContextPacket struct {
	Key                     string            `json:"key"`
	SchemaVersion           int               `json:"schema_version"`
	Ticket                  ticket            `json:"ticket"`
	RequirementExcerpts     map[string]string `json:"requirement_excerpts"`
	AcceptanceExcerpts      map[string]string `json:"acceptance_criteria_excerpts"`
	SharedPRDContext        map[string]string `json:"shared_prd_context"`
	ArchitectureConstraints []string          `json:"architecture_constraints"`
	ApplicableADRs          []string          `json:"applicable_adrs"`
	ContextSources          map[string]string `json:"context_sources"`
}

var taggedLinePattern = regexp.MustCompile(`(?m)^\s*[-*]\s+([A-Z]+-?[0-9]+):\s*(.+?)\s*$`)

func (r *Runner) materializeTicketContexts(productBody, architectureBody []byte) error {
	var product productContextSource
	if err := json.Unmarshal(productBody, &product); err != nil {
		return fmt.Errorf("decode product context: %w", err)
	}
	var architecture architectureReconciliation
	if err := json.Unmarshal(architectureBody, &architecture); err != nil {
		return fmt.Errorf("decode architecture context: %w", err)
	}
	requirements := taggedMarkdownLines(product.PRDMarkdown, "R")
	acceptance := acceptanceCriterionBlocks(product.PRDMarkdown)
	requirementsByTicket := map[string][]string{}
	for _, coverage := range product.Coverage {
		for _, ticketKey := range coverage.Tickets {
			requirementsByTicket[ticketKey] = append(requirementsByTicket[ticketKey], coverage.Requirement)
		}
	}
	shared := map[string]string{}
	for _, heading := range []string{"## Non-Goals", "## Constraints and Dependencies"} {
		if section := markdownSection(product.PRDMarkdown, heading); section != "" {
			shared[strings.TrimPrefix(heading, "## ")] = section
		}
	}
	packets := make([]ticketContextPacket, 0, len(product.Tickets))
	for _, item := range product.Tickets {
		requirementExcerpts := map[string]string{}
		for _, requirement := range requirementsByTicket[item.Key] {
			if excerpt := requirements[requirement]; excerpt != "" {
				requirementExcerpts[requirement] = excerpt
			}
		}
		acceptanceExcerpts := map[string]string{}
		criteria := append([]string(nil), item.AcceptanceCriteria...)
		criteria = append(criteria, item.SourceAcceptanceCriteria...)
		for _, criterion := range criteria {
			if excerpt := acceptance[criterion]; excerpt != "" {
				acceptanceExcerpts[criterion] = excerpt
			}
		}
		packets = append(packets, ticketContextPacket{
			Key: item.Key, SchemaVersion: 1, Ticket: item,
			RequirementExcerpts: requirementExcerpts, AcceptanceExcerpts: acceptanceExcerpts,
			SharedPRDContext: shared, ArchitectureConstraints: item.ArchitectureConstraints,
			ApplicableADRs: append([]string(nil), architecture.ApplicableADRs...),
			ContextSources: map[string]string{
				"prd": "artifacts/prd.md", "adr": "artifacts/adr.md",
				"architecture": ".harness/ARCHITECTURE.md", "testing": ".harness/TESTING.md",
				"security": ".harness/SECURITY.md", "design": ".harness/DESIGN.md",
			},
		})
	}
	sort.Slice(packets, func(i, j int) bool { return packets[i].Key < packets[j].Key })
	body, err := json.MarshalIndent(packets, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.runDir, "artifacts", "ticket-contexts.json")
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func (r *Runner) refreshTicketContextsForPlan(plan []ticket) error {
	if err := os.MkdirAll(filepath.Join(r.runDir, "artifacts"), 0o700); err != nil {
		return err
	}
	productBody, err := os.ReadFile(filepath.Join(r.runDir, "agent-output", "product.json"))
	if err != nil {
		return err
	}
	var product productContextSource
	if err := json.Unmarshal(productBody, &product); err != nil {
		return fmt.Errorf("decode product context for repair tickets: %w", err)
	}
	product.Tickets = append([]ticket(nil), plan...)
	productBody, err = json.Marshal(product)
	if err != nil {
		return fmt.Errorf("encode product context for repair tickets: %w", err)
	}
	architectureBody, err := os.ReadFile(filepath.Join(r.runDir, "agent-output", "arch.json"))
	if err != nil {
		return err
	}
	return r.materializeTicketContexts(productBody, architectureBody)
}

func (r *Runner) refreshTicketContextsAfterRestore() error {
	archComplete, err := r.stageCompleted("arch")
	if err != nil || !archComplete {
		return err
	}
	planBody, err := os.ReadFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"))
	if err != nil {
		return err
	}
	var plan []ticket
	if err := json.Unmarshal(planBody, &plan); err != nil {
		return fmt.Errorf("decode restored ticket plan: %w", err)
	}
	return r.refreshTicketContextsForPlan(plan)
}

func taggedMarkdownLines(markdown, prefix string) map[string]string {
	result := map[string]string{}
	for _, match := range taggedLinePattern.FindAllStringSubmatch(markdown, -1) {
		if match[1] == prefix || strings.HasPrefix(match[1], prefix+"-") ||
			(strings.HasPrefix(match[1], prefix) && len(match[1]) > len(prefix) && match[1][len(prefix)] >= '0' && match[1][len(prefix)] <= '9') {
			result[match[1]] = strings.TrimSpace(match[2])
		}
	}
	return result
}

func acceptanceCriterionBlocks(markdown string) map[string]string {
	result := map[string]string{}
	lines := strings.Split(markdown, "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "### AC-") {
			continue
		}
		key := strings.TrimSuffix(strings.Fields(line)[1], ":")
		end := index + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "### ") && !strings.HasPrefix(strings.TrimSpace(lines[end]), "## ") {
			end++
		}
		result[key] = strings.TrimSpace(strings.Join(lines[index:end], "\n"))
		index = end - 1
	}
	return result
}

func markdownSection(markdown, heading string) string {
	lines := strings.Split(markdown, "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = index + 1
			continue
		}
		if start >= 0 && strings.HasPrefix(strings.TrimSpace(line), "## ") {
			return strings.TrimSpace(strings.Join(lines[start:index], "\n"))
		}
	}
	if start >= 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n"))
	}
	return ""
}

func stringValues(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
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

func mergeUniqueStrings(values, additions []string) ([]string, bool) {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	changed := false
	for _, addition := range additions {
		if addition != "" && !seen[addition] {
			values = append(values, addition)
			seen[addition], changed = true, true
		}
	}
	return values, changed
}

func pipelineGateValues(value any) ([]pipelineGate, error) {
	if value == nil {
		return []pipelineGate{}, nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var gates []pipelineGate
	if err := json.Unmarshal(body, &gates); err != nil {
		return nil, errors.New("must be a pipeline gate array")
	}
	return gates, nil
}

func mergePipelineGates(values, additions []pipelineGate) ([]pipelineGate, bool) {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value.Stage+"\x00"+value.Command] = true
	}
	changed := false
	for _, addition := range additions {
		key := addition.Stage + "\x00" + addition.Command
		if addition.Command != "" && !seen[key] {
			values = append(values, addition)
			seen[key], changed = true, true
		}
	}
	return values, changed
}
