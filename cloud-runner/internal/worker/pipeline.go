package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Pipeline struct {
	Version     int          `yaml:"version"`
	Name        string       `yaml:"name"`
	RunRoot     string       `yaml:"run_root"`
	Stages      []Stage      `yaml:"stages"`
	RepairLoops []RepairLoop `yaml:"repair_loops"`
}

type RepairLoop struct {
	From         string `yaml:"from"`
	To           string `yaml:"to"`
	Through      string `yaml:"through"`
	MaxReentries int    `yaml:"max_reentries"`
}

type Stage struct {
	ID          string         `yaml:"id"`
	Agent       string         `yaml:"agent"`
	Model       string         `yaml:"model"`
	Needs       []string       `yaml:"needs"`
	Mode        string         `yaml:"mode"`
	Parallelism int            `yaml:"parallelism"`
	Inputs      []FileContract `yaml:"inputs"`
	Outputs     []FileContract `yaml:"outputs"`
	Result      FileContract   `yaml:"result"`
	Hooks       Hooks          `yaml:"hooks"`
}

type FileContract struct {
	ID         string `yaml:"id"`
	File       string `yaml:"file"`
	Format     string `yaml:"format"`
	Required   bool   `yaml:"required"`
	FromResult string `yaml:"from_result"`
	Agent      string `yaml:"agent"`
}

type Hooks struct {
	Before    []Hook `yaml:"before"`
	After     []Hook `yaml:"after"`
	OnFailure []Hook `yaml:"on_failure"`
}

type Hook struct {
	ID             string   `yaml:"id" json:"id"`
	Argv           []string `yaml:"argv" json:"argv"`
	Cwd            string   `yaml:"cwd" json:"cwd"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
}

func loadPipeline(path string) (Pipeline, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Pipeline{}, err
	}
	var pipeline Pipeline
	if err := yaml.Unmarshal(body, &pipeline); err != nil {
		return pipeline, err
	}
	if pipeline.Version != 1 || pipeline.RunRoot == "" || len(pipeline.Stages) == 0 {
		return pipeline, errors.New("unsupported or empty harness pipeline")
	}
	seen := map[string]bool{}
	for _, stage := range pipeline.Stages {
		if stage.ID == "" || seen[stage.ID] {
			return pipeline, fmt.Errorf("invalid or duplicate stage %q", stage.ID)
		}
		seen[stage.ID] = true
		if stage.Parallelism < 1 {
			return pipeline, fmt.Errorf("stage %s has invalid parallelism", stage.ID)
		}
		if stage.Mode != "single" && stage.Mode != "ticket_parallel" {
			return pipeline, fmt.Errorf("stage %s has unsupported mode", stage.ID)
		}
	}
	for _, stage := range pipeline.Stages {
		for _, dependency := range stage.Needs {
			if !seen[dependency] {
				return pipeline, fmt.Errorf("stage %s depends on unknown stage %s", stage.ID, dependency)
			}
		}
	}
	positions := map[string]int{}
	for index, stage := range pipeline.Stages {
		positions[stage.ID] = index
	}
	loopSources := map[string]bool{}
	for _, loop := range pipeline.RepairLoops {
		from, fromOK := positions[loop.From]
		to, toOK := positions[loop.To]
		through, throughOK := positions[loop.Through]
		if !fromOK || !toOK || !throughOK {
			return pipeline, fmt.Errorf("repair loop references an unknown stage: from=%s to=%s through=%s", loop.From, loop.To, loop.Through)
		}
		if loopSources[loop.From] {
			return pipeline, fmt.Errorf("stage %s has multiple repair loops", loop.From)
		}
		if loop.MaxReentries < 1 || to > through || through < from || to >= from {
			return pipeline, fmt.Errorf("invalid repair loop from %s to %s through %s", loop.From, loop.To, loop.Through)
		}
		loopSources[loop.From] = true
	}
	return pipeline, nil
}

func runDirectory(repo string, pipeline Pipeline, runID string) string {
	return filepath.Join(repo, filepath.FromSlash(replaceRunID(pipeline.RunRoot, runID)))
}

func replaceRunID(value, runID string) string {
	const placeholder = "{run_id}"
	for index := 0; index+len(placeholder) <= len(value); index++ {
		if value[index:index+len(placeholder)] == placeholder {
			return value[:index] + runID + value[index+len(placeholder):]
		}
	}
	return value
}
