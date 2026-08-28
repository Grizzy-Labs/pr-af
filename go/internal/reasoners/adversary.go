package reasoners

import (
	"context"
	"os"
	"unicode/utf8"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
)

// AdversaryPhase ports adversary_phase: the skeptical challenger that
// confirms, challenges, or escalates each finding against ground-truth
// evidence. Skepticism escalates to "high" when the AI-generated confidence
// exceeds 0.5 (handled inside the prompt builder).
//
// Output keys (§B.2): results. Missing or unparseable structured output fails
// the adversarial phase.
func AdversaryPhase(ctx context.Context, deps Deps, in AdversaryInput) (map[string]any, error) {
	evMap := evidenceOMaps(in.EvidencePackages)

	// The builder embeds a file reference when the findings JSON (first 20
	// findings) exceeds 10000 characters and a repo path exists; the write is
	// the reasoner's job. Each call gets its own immutable snapshot because
	// adversary batches run concurrently against the same repository.
	summary := adversaryContext(in.Findings, evMap)
	findingsPath := ""
	if utf8.RuneCountInString(summary) > 10000 && in.RepoPath != "" {
		var err error
		findingsPath, err = writeUniqueContextFile(summary, "adversary_findings_*.json", in.RepoPath)
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(findingsPath) }()
	}

	prompt := prompts.AdversaryPrompt(in.Findings, in.AIGeneratedConfidence, in.PrContext, findingsPath, evMap)
	parsed, _, err := harnessx.Run[adversaryPhaseResult](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	results, err := dumpSlice(parsed.Results)
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": results}, nil
}
