package reasoners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

type concurrentContextHarness struct {
	mu      sync.Mutex
	paths   []string
	ready   chan struct{}
	arrived int
}

type failingContextHarness struct {
	path string
}

func contextFilePathFromPrompt(prompt string) (string, error) {
	const prefix = "Full findings with ground-truth evidence written to: "
	start := strings.Index(prompt, prefix)
	if start < 0 {
		return "", fmt.Errorf("prompt is missing context-file prefix")
	}
	return strings.SplitN(prompt[start+len(prefix):], "\n", 2)[0], nil
}

func (h *concurrentContextHarness) Harness(
	ctx context.Context,
	prompt string,
	_ map[string]any,
	dest any,
	_ harness.Options,
) (*harness.Result, error) {
	path, err := contextFilePathFromPrompt(prompt)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	h.paths = append(h.paths, path)
	h.arrived++
	if h.arrived == 2 {
		close(h.ready)
	}
	h.mu.Unlock()
	select {
	case <-h.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	title := "first"
	if strings.Contains(string(content), `"title": "second"`) {
		title = "second"
	}
	payload := `{"results":[{"finding_title":"` + title + `","verdict":"confirmed"}]}`
	if err := json.Unmarshal([]byte(payload), dest); err != nil {
		return nil, err
	}
	return &harness.Result{Parsed: dest, Result: payload}, nil
}

func (h *failingContextHarness) Harness(
	_ context.Context,
	prompt string,
	_ map[string]any,
	_ any,
	_ harness.Options,
) (*harness.Result, error) {
	path, err := contextFilePathFromPrompt(prompt)
	if err != nil {
		return nil, err
	}
	h.path = path
	return nil, errors.New("harness failed")
}

func TestAdversaryPhaseUsesDistinctContextFilesForConcurrentBatches(t *testing.T) {
	repo := t.TempDir()
	h := &concurrentContextHarness{ready: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := strings.Repeat("large evidence ", 1000)
	inputs := []AdversaryInput{
		{Findings: []schemas.ReviewFinding{{Title: "first", Body: body}}, RepoPath: repo},
		{Findings: []schemas.ReviewFinding{{Title: "second", Body: body}}, RepoPath: repo},
	}

	type outcome struct {
		expectedTitle string
		result        map[string]any
		err           error
	}
	outcomes := make(chan outcome, len(inputs))
	for _, input := range inputs {
		input := input
		expectedTitle := input.Findings[0].Title
		go func() {
			result, err := AdversaryPhase(ctx, Deps{Harness: h}, input)
			outcomes <- outcome{expectedTitle: expectedTitle, result: result, err: err}
		}()
	}
	for range inputs {
		var outcome outcome
		select {
		case outcome = <-outcomes:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		results := outcome.result["results"].([]any)
		findingTitle := results[0].(map[string]any)["finding_title"]
		if findingTitle != outcome.expectedTitle {
			t.Fatalf("finding title = %q, want %q", findingTitle, outcome.expectedTitle)
		}
	}

	h.mu.Lock()
	paths := append([]string(nil), h.paths...)
	h.mu.Unlock()
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("context paths = %v, want two distinct files", paths)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("context snapshot %q was not removed after the harness call: %v", path, err)
		}
	}
}

func TestAdversaryPhaseRemovesContextFileAfterHarnessFailure(t *testing.T) {
	repo := t.TempDir()
	h := &failingContextHarness{}
	body := strings.Repeat("large evidence ", 1000)

	_, err := AdversaryPhase(context.Background(), Deps{Harness: h}, AdversaryInput{
		Findings: []schemas.ReviewFinding{{Title: "failure", Body: body}},
		RepoPath: repo,
	})
	if err == nil || !strings.Contains(err.Error(), "harness failed") {
		t.Fatalf("AdversaryPhase error = %v", err)
	}
	if h.path == "" {
		t.Fatal("harness did not receive a context-file path")
	}
	if _, statErr := os.Stat(h.path); !os.IsNotExist(statErr) {
		t.Fatalf("context snapshot %q was not removed after harness failure: %v", h.path, statErr)
	}
}
