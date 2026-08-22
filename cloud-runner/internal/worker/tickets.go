package worker

import (
	"errors"
	"fmt"
	"sort"
)

type ticket struct {
	Key                     string              `json:"key"`
	Type                    string              `json:"type,omitempty"`
	Title                   string              `json:"title"`
	Objective               string              `json:"objective"`
	AcceptanceCriteria      []string            `json:"acceptance_criteria"`
	OwnedPaths              []string            `json:"owned_paths"`
	DependsOn               []string            `json:"depends_on"`
	FocusedChecks           []string            `json:"focused_checks,omitempty"`
	Verification            *ticketVerification `json:"verification,omitempty"`
	CommitMessage           string              `json:"commit_message,omitempty"`
	Complexity              string              `json:"complexity,omitempty"`
	ArchitectureConstraints []string            `json:"architecture_constraints,omitempty"`
}

type ticketVerification struct {
	IterationChecks []string       `json:"iteration_checks"`
	TicketGate      []string       `json:"ticket_gate"`
	PipelineGates   []pipelineGate `json:"pipeline_gates"`
}

type pipelineGate struct {
	Stage   string `json:"stage"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

type ticketRun struct {
	ticket   ticket
	worktree string
	result   []byte
	commit   string
	blocker  string
	err      error
}

func ticketWaves(tickets []ticket) ([][]ticket, error) {
	return ticketWavesWithDone(tickets, map[string]bool{})
}

func ticketWavesWithDone(tickets []ticket, done map[string]bool) ([][]ticket, error) {
	remaining := map[string]ticket{}
	known := map[string]bool{}
	for _, item := range tickets {
		if item.Key == "" || known[item.Key] {
			return nil, errors.New("ticket plan has an empty or duplicate key")
		}
		known[item.Key] = true
		if !done[item.Key] {
			remaining[item.Key] = item
		}
	}
	for _, item := range tickets {
		for _, dependency := range item.DependsOn {
			if !known[dependency] {
				return nil, fmt.Errorf("ticket %s depends on unknown ticket %s", item.Key, dependency)
			}
		}
	}
	var waves [][]ticket
	for len(remaining) > 0 {
		var keys []string
		for key, item := range remaining {
			ready := true
			for _, dependency := range item.DependsOn {
				ready = ready && done[dependency]
			}
			if ready {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			return nil, errors.New("ticket dependency graph contains a cycle")
		}
		sort.Strings(keys)
		wave := make([]ticket, 0, len(keys))
		for _, key := range keys {
			wave = append(wave, remaining[key])
			delete(remaining, key)
			done[key] = true
		}
		waves = append(waves, wave)
	}
	return waves, nil
}
