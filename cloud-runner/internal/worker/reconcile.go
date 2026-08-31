package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

type workspacePackageManifest struct {
	Name                 string                     `json:"name"`
	Scripts              map[string]json.RawMessage `json:"scripts"`
	Dependencies         map[string]json.RawMessage `json:"dependencies"`
	DevDependencies      map[string]json.RawMessage `json:"devDependencies"`
	PeerDependencies     map[string]json.RawMessage `json:"peerDependencies"`
	OptionalDependencies map[string]json.RawMessage `json:"optionalDependencies"`
}

type workspacePackage struct {
	root     string
	manifest workspacePackageManifest
}

var packageDependencyDirective = regexp.MustCompile(`(?i)\b((?:do|must)\s+not\s+)?depends?\s+on\s+(@[a-z0-9._-]+/[a-z0-9._-]+)\b`)
var playwrightCommand = regexp.MustCompile(`(?i)(?:\bplaywright\s+test\b|\btest:e2e\b)`)
var playwrightConfigArgument = regexp.MustCompile(`\s+--config(?:=|\s+)(?:"([^"]+)"|'([^']+)'|([^\s]+))`)
var pnpmFilterArgument = regexp.MustCompile(`(?:^|\s)(?:--filter|-F)(?:=|\s+)(?:"([^"]+)"|'([^']+)'|([^\s]+))`)

func ensureWorkspaceDependencyOwnership(repo string, plan []ticket) ([]ticket, bool, error) {
	packages, err := discoverWorkspacePackages(repo)
	if err != nil {
		return nil, false, err
	}
	byName := make(map[string]workspacePackage, len(packages))
	for _, item := range packages {
		if item.manifest.Name != "" {
			byName[item.manifest.Name] = item
		}
	}
	lockfiles := existingWorkspaceLockfiles(repo)
	changed := false
	for index := range plan {
		requested := map[string]bool{}
		for _, constraint := range plan[index].ArchitectureConstraints {
			for _, match := range packageDependencyDirective.FindAllStringSubmatch(constraint, -1) {
				if match[1] == "" {
					requested[match[2]] = true
				}
			}
		}
		if len(requested) == 0 {
			continue
		}
		owners := owningWorkspacePackages(plan[index].OwnedPaths, packages)
		for _, owner := range owners {
			for dependency := range requested {
				if _, workspaceDependency := byName[dependency]; !workspaceDependency || manifestDeclares(owner.manifest, dependency) {
					continue
				}
				var added bool
				plan[index].OwnedPaths, added = appendUnique(plan[index].OwnedPaths, filepath.ToSlash(filepath.Join(owner.root, "package.json")))
				changed = changed || added
				for _, lockfile := range lockfiles {
					plan[index].OwnedPaths, added = appendUnique(plan[index].OwnedPaths, lockfile)
					changed = changed || added
				}
			}
		}
	}
	return plan, changed, nil
}

func ensurePlaywrightUsesPlaywrightConfig(repo string, plan []ticket) ([]ticket, bool, error) {
	packages, err := discoverWorkspacePackages(repo)
	if err != nil {
		return nil, false, err
	}
	byName := make(map[string]string, len(packages))
	for _, item := range packages {
		byName[item.manifest.Name] = item.root
	}
	changed := false
	for index := range plan {
		if plan[index].Verification == nil {
			continue
		}
		for _, checks := range [][]string{plan[index].Verification.IterationChecks, plan[index].Verification.TicketGate} {
			for checkIndex, command := range checks {
				updated, corrected, err := removeViteConfigFromPlaywrightCommand(repo, byName, command)
				if err != nil {
					return nil, false, fmt.Errorf("ticket %s verification command: %w", plan[index].Key, err)
				}
				if corrected {
					checks[checkIndex], changed = updated, true
				}
			}
		}
	}
	return plan, changed, nil
}

func ensureRequiredFixtureOwnership(repo string, plan []ticket) ([]ticket, bool, error) {
	packages, err := discoverWorkspacePackages(repo)
	if err != nil {
		return nil, false, err
	}
	changed := false
	for index := range plan {
		requiresFixture := false
		for _, constraint := range plan[index].ArchitectureConstraints {
			normalized := strings.ToLower(constraint)
			if strings.Contains(normalized, "full-stack fixture") || strings.Contains(normalized, "full stack fixture") {
				requiresFixture = true
				break
			}
		}
		if !requiresFixture {
			continue
		}
		for _, owner := range owningWorkspacePackages(plan[index].OwnedPaths, packages) {
			for _, relative := range []string{"e2e/full-stack-fixture.mjs", "e2e/full-stack-fixture.ts"} {
				candidate := filepath.ToSlash(filepath.Join(owner.root, filepath.FromSlash(relative)))
				if info, statErr := os.Stat(filepath.Join(repo, filepath.FromSlash(candidate))); statErr == nil && !info.IsDir() {
					var added bool
					plan[index].OwnedPaths, added = appendUnique(plan[index].OwnedPaths, candidate)
					changed = changed || added
					break
				} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					return nil, false, statErr
				}
			}
		}
	}
	return plan, changed, nil
}

func ensureBackgroundServiceEntrypointOwnership(repo string, plan []ticket) ([]ticket, bool, error) {
	packages, err := discoverWorkspacePackages(repo)
	if err != nil {
		return nil, false, err
	}
	changed := false
	for index := range plan {
		for _, owned := range plan[index].OwnedPaths {
			cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(owned)))
			if filepath.ToSlash(filepath.Dir(cleaned)) != ".railway" || filepath.Ext(cleaned) != ".json" {
				continue
			}
			service := strings.TrimSuffix(filepath.Base(cleaned), filepath.Ext(cleaned))
			if service != "worker" {
				continue
			}
			for _, candidate := range packages {
				packageName := candidate.manifest.Name
				if slash := strings.LastIndex(packageName, "/"); slash >= 0 {
					packageName = packageName[slash+1:]
				}
				if filepath.Base(candidate.root) != service && packageName != service {
					continue
				}
				if _, hasStart := candidate.manifest.Scripts["start"]; hasStart {
					continue
				}
				var added bool
				plan[index].OwnedPaths, added = appendUnique(plan[index].OwnedPaths, filepath.ToSlash(filepath.Join(candidate.root, "package.json")))
				changed = changed || added
				plan[index].OwnedPaths, added = appendUnique(plan[index].OwnedPaths, filepath.ToSlash(filepath.Join(candidate.root, "src", "server.ts")))
				changed = changed || added
			}
		}
	}
	return plan, changed, nil
}

func removeViteConfigFromPlaywrightCommand(repo string, packageRoots map[string]string, command string) (string, bool, error) {
	if !playwrightCommand.MatchString(command) {
		return command, false, nil
	}
	match := playwrightConfigArgument.FindStringSubmatchIndex(command)
	if match == nil {
		return command, false, nil
	}
	configPath := firstRegexpGroup(command, match, 1, 2, 3)
	if configPath == "" || filepath.IsAbs(configPath) {
		return command, false, nil
	}
	root := repo
	if filter := pnpmFilterArgument.FindStringSubmatch(command); len(filter) > 0 {
		if packageRoot := packageRoots[firstNonEmpty(filter[1:]...)]; packageRoot != "" {
			root = filepath.Join(repo, filepath.FromSlash(packageRoot))
		}
	}
	resolved := filepath.Join(root, filepath.FromSlash(configPath))
	body, err := os.ReadFile(resolved)
	if errors.Is(err, os.ErrNotExist) && root != repo {
		resolved = filepath.Join(repo, filepath.FromSlash(configPath))
		body, err = os.ReadFile(resolved)
	}
	if errors.Is(err, os.ErrNotExist) {
		return command, false, nil
	}
	if err != nil {
		return command, false, err
	}
	text := string(body)
	if !strings.Contains(text, `from "vite"`) && !strings.Contains(text, `from 'vite'`) {
		return command, false, nil
	}
	return strings.TrimSpace(command[:match[0]] + command[match[1]:]), true, nil
}

func firstRegexpGroup(value string, indexes []int, groups ...int) string {
	for _, group := range groups {
		start, end := indexes[group*2], indexes[group*2+1]
		if start >= 0 && end >= start {
			return value[start:end]
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func discoverWorkspacePackages(repo string) ([]workspacePackage, error) {
	var result []workspacePackage
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".harness", "node_modules":
				if path != repo {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != "package.json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var manifest workspacePackageManifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return fmt.Errorf("decode workspace manifest %s: %w", path, err)
		}
		if manifest.Name == "" {
			return nil
		}
		root, err := filepath.Rel(repo, filepath.Dir(path))
		if err != nil {
			return err
		}
		result = append(result, workspacePackage{root: filepath.ToSlash(root), manifest: manifest})
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].root < result[j].root })
	return result, err
}

func existingWorkspaceLockfiles(repo string) []string {
	var result []string
	for _, name := range []string{"pnpm-lock.yaml", "package-lock.json", "yarn.lock", "bun.lock", "bun.lockb"} {
		if info, err := os.Stat(filepath.Join(repo, name)); err == nil && !info.IsDir() {
			result = append(result, name)
		}
	}
	return result
}

func owningWorkspacePackages(paths []string, packages []workspacePackage) []workspacePackage {
	owners := map[string]workspacePackage{}
	for _, owned := range paths {
		owned = filepath.ToSlash(filepath.Clean(filepath.FromSlash(owned)))
		best := workspacePackage{}
		for _, candidate := range packages {
			if candidate.root == "." || owned == candidate.root || strings.HasPrefix(owned, candidate.root+"/") {
				if len(candidate.root) > len(best.root) {
					best = candidate
				}
			}
		}
		if best.manifest.Name != "" {
			owners[best.root] = best
		}
	}
	keys := make([]string, 0, len(owners))
	for root := range owners {
		keys = append(keys, root)
	}
	sort.Strings(keys)
	result := make([]workspacePackage, 0, len(keys))
	for _, root := range keys {
		result = append(result, owners[root])
	}
	return result
}

func manifestDeclares(manifest workspacePackageManifest, dependency string) bool {
	for _, group := range []map[string]json.RawMessage{
		manifest.Dependencies,
		manifest.DevDependencies,
		manifest.PeerDependencies,
		manifest.OptionalDependencies,
	} {
		if _, ok := group[dependency]; ok {
			return true
		}
	}
	return false
}

func appendUnique(values []string, addition string) ([]string, bool) {
	for _, value := range values {
		if value == addition {
			return values, false
		}
	}
	return append(values, addition), true
}

func (r *Runner) reconcileWorkspaceDependencyOwnership(ctx context.Context) error {
	planPath := filepath.Join(r.runDir, "artifacts", "ticket-plan.json")
	body, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan []ticket
	if err := json.Unmarshal(body, &plan); err != nil {
		return fmt.Errorf("decode ticket plan for package ownership: %w", err)
	}
	plan, changed, err := ensureWorkspaceDependencyOwnership(r.repo, plan)
	if err != nil {
		return err
	}
	plan, verificationChanged, err := ensurePlaywrightUsesPlaywrightConfig(r.repo, plan)
	if err != nil {
		return err
	}
	plan, fixtureChanged, err := ensureRequiredFixtureOwnership(r.repo, plan)
	if err != nil {
		return err
	}
	plan, entrypointChanged, err := ensureBackgroundServiceEntrypointOwnership(r.repo, plan)
	if err != nil {
		return err
	}
	if !changed && !verificationChanged && !fixtureChanged && !entrypointChanged {
		return nil
	}
	body, err = json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	productPath := filepath.Join(r.runDir, "agent-output", "product.json")
	productBody, err := os.ReadFile(productPath)
	if err != nil {
		return err
	}
	var product map[string]any
	if err := json.Unmarshal(productBody, &product); err != nil {
		return fmt.Errorf("decode product output for package ownership: %w", err)
	}
	var ticketValues []any
	if err := json.Unmarshal(body, &ticketValues); err != nil {
		return err
	}
	product["tickets"] = ticketValues
	productBody, err = json.MarshalIndent(product, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(r.runDir, "product-package-ownership-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(append(productBody, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := r.harness(ctx, r.repo, "validate-agent-output", "--agent", "product", "--input", temporaryPath); err != nil {
		return fmt.Errorf("validate package ownership reconciliation: %w", err)
	}
	if err := os.WriteFile(productPath, append(productBody, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return r.refreshTicketContextsForPlan(plan)
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
