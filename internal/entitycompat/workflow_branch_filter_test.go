package entitycompat

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// pullRequestWorkflowFiles are the exactly six workflows that must trigger on
// pull requests whose base branch is Misaki-develop.
//
// A push-only workflow (docker-branch.yml) and schedule/workflow_dispatch-only
// workflows (dropin-frontend-e2e.yml, queue-bench-smoke.yml) are deliberately
// excluded: they must NOT be required to carry Misaki-develop.
var pullRequestWorkflowFiles = []string{
	".github/workflows/ci.yml",
	".github/workflows/diff-e2e.yml",
	".github/workflows/docker.yml",
	".github/workflows/dropin-e2e.yml",
	".github/workflows/playwright.yml",
	".github/workflows/upstream-backend-e2e.yml",
}

// TestWorkflowBranchFiltersIncludeMisakiDevelop guards CI coverage for PRs
// whose base is Misaki-develop (PR #2 was reported as NO CHECK).
//
// GitHub Actions ignores pull_request triggers whose `branches` filter does
// not match the PR's base branch. All pull_request-triggered workflows must
// include `Misaki-develop` in `on.pull_request.branches`. The test isolates
// the `on.pull_request` block and never inspects `on.push.branches`, so a
// push-only branch entry cannot produce a false pass.
func TestWorkflowBranchFiltersIncludeMisakiDevelop(t *testing.T) {
	for _, rel := range pullRequestWorkflowFiles {
		path := filepath.Join("..", "..", rel)
		got := pullRequestBranches(t, path)
		if !containsString(got, "Misaki-develop") {
			t.Errorf("%s: on.pull_request.branches = %v; want Misaki-develop", rel, got)
		}
	}
}

// pullRequestBranches parses a GitHub Actions workflow YAML with yaml.v3 and
// returns the `on.pull_request.branches` list. It returns nil when the
// workflow has no pull_request trigger, or no branches key under it.
func pullRequestBranches(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	on, ok := doc["on"].(map[string]interface{})
	if !ok {
		return nil
	}
	pr, ok := on["pull_request"].(map[string]interface{})
	if !ok {
		return nil
	}
	branches, ok := pr["branches"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(branches))
	for _, b := range branches {
		if s, ok := b.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
