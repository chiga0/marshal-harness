package pi

import "strings"

// Native reporting keeps control metadata out of the model's obligations.
// The same frozen contract selects the parser in the composition root.
func buildNativeProductionPrompt(in ProductionLaunchInput) string {
	var prompt strings.Builder
	prompt.WriteString("Complete the approved business task using the available tools. Work only within the approved working directory and constraints.\n\nObjective:\n")
	prompt.WriteString(in.Objective)
	prompt.WriteString("\n\nConstraints:\n")
	for _, constraint := range in.Constraints {
		prompt.WriteString("- " + constraint + "\n")
	}
	prompt.WriteString("\nDeliver the requested business files. Your final response is a concise, honest report of what was delivered, what you checked, and any limitations, failures, or unfinished work (at most 12000 characters). Do not invent test results. Independent verification will decide business acceptance. Do not construct a Marshal WorkerResult or fill in control-plane identity, timestamps, or evidence fields.\n")
	return prompt.String()
}
