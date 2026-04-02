package logic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type AgentOutputMeta struct {
	Summary          string       `json:"summary"`
	BranchName       string       `json:"branch_name,omitempty"`
	FilesModified    []string     `json:"files_modified,omitempty"`
	TestsRun         *TestResults `json:"tests_run,omitempty"`
	Issues           []Issue      `json:"issues,omitempty"`
	Recommendations  []string     `json:"recommendations,omitempty"`
	CompletionStatus string       `json:"completion_status,omitempty"`
}

type TestResults struct {
	Passed   int      `json:"passed"`
	Failed   int      `json:"failed"`
	Skipped  int      `json:"skipped"`
	Failures []string `json:"failures,omitempty"`
}

type Issue struct {
	Type        string `json:"type"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
}

func ParseAgentOutput(output string) (AgentOutputMeta, string, error) {
	meta, cleaned, err := extractAgentOutputMeta(output)
	if err != nil {
		return AgentOutputMeta{}, strings.TrimSpace(output), err
	}

	cleaned = strings.TrimSpace(cleaned)
	if strings.TrimSpace(meta.Summary) == "" {
		meta.Summary = summarizeOutput(cleaned)
	}
	if strings.TrimSpace(meta.CompletionStatus) == "" {
		meta.CompletionStatus = "partial"
	}

	return meta, cleaned, nil
}

func BuildPlannerContext(mainTask string, lastStep *ExecutedStep, streamContext *StreamContext) string {
	sections := []string{
		fmt.Sprintf("Main task: %s", strings.TrimSpace(mainTask)),
	}

	if streamContext != nil {
		sections = append(sections, fmt.Sprintf("Stream id: %d", streamContext.StreamID))
		if strings.TrimSpace(streamContext.BranchName) != "" {
			sections = append(sections, fmt.Sprintf("Stream branch: %s", strings.TrimSpace(streamContext.BranchName)))
		}
		if len(streamContext.ExecutionHistory) > 0 {
			history := make([]string, 0, len(streamContext.ExecutionHistory))
			for index, step := range streamContext.ExecutionHistory {
				history = append(history, fmt.Sprintf("%d. [%s] %s => %s", index+1, step.Agent, step.Task, step.Summary))
			}
			sections = append(sections, "Execution history:\n"+strings.Join(history, "\n"))
		}
	}

	if lastStep != nil {
		sections = append(sections,
			fmt.Sprintf("Last agent: %s", strings.TrimSpace(lastStep.Agent)),
			fmt.Sprintf("Last agent task: %s", strings.TrimSpace(lastStep.Task)),
			fmt.Sprintf("Last agent summary: %s", strings.TrimSpace(lastStep.Summary)),
		)
		if strings.TrimSpace(lastStep.Output) != "" {
			sections = append(sections, fmt.Sprintf("Last agent output:\n%s", strings.TrimSpace(lastStep.Output)))
		}
	} else {
		sections = append(sections, "Last agent: none yet")
	}

	return strings.Join(sections, "\n\n")
}

func extractAgentOutputMeta(output string) (AgentOutputMeta, string, error) {
	jsonBlockRegex := regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	matches := jsonBlockRegex.FindAllStringSubmatchIndex(output, -1)
	for index := len(matches) - 1; index >= 0; index-- {
		match := matches[index]
		if len(match) < 4 {
			continue
		}
		rawJSON := strings.TrimSpace(output[match[2]:match[3]])
		var meta AgentOutputMeta
		if err := json.Unmarshal([]byte(rawJSON), &meta); err != nil {
			continue
		}
		if !looksLikeAgentOutputMeta(meta) {
			continue
		}

		cleaned := strings.TrimSpace(output[:match[0]] + output[match[1]:])
		return meta, cleaned, nil
	}

	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		var meta AgentOutputMeta
		if err := json.Unmarshal([]byte(trimmed), &meta); err == nil && looksLikeAgentOutputMeta(meta) {
			return meta, "", nil
		}
	}

	return AgentOutputMeta{}, output, fmt.Errorf("agent output metadata not found")
}

func looksLikeAgentOutputMeta(meta AgentOutputMeta) bool {
	return strings.TrimSpace(meta.Summary) != "" ||
		strings.TrimSpace(meta.CompletionStatus) != "" ||
		strings.TrimSpace(meta.BranchName) != "" ||
		len(meta.FilesModified) > 0 ||
		meta.TestsRun != nil ||
		len(meta.Issues) > 0 ||
		len(meta.Recommendations) > 0
}

func summarizeOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "No output provided."
	}

	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}

	return trimmed
}
