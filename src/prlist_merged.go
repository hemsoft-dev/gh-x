package main

import (
	"encoding/json"
	"fmt"
)

// Search through an old repository name can return no matches after a move.
// The repository connection still returns the existing merged pull requests.
const mergedPRFields = `
  number title state isDraft reviewDecision updatedAt mergedAt
  headRefName baseRefName url mergeable
  author { login ... on User { name } }
  latestReviews(first: 100) { nodes { state author { login ... on User { name } } } }
  commits(last: 1) { nodes { commit { id statusCheckRollup {
    contexts(first: 100) { nodes { ` + mergedCheckFields + ` }
      pageInfo { hasNextPage endCursor }
    }
  } } } }
`

type mergedCheckNode struct {
	checkItem
	CheckSuite struct {
		WorkflowRun struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

type mergedPRNode struct {
	pullRequest
	LatestReviews struct {
		Nodes []review `json:"nodes"`
	} `json:"latestReviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				ID                string                   `json:"id"`
				StatusCheckRollup *mergedStatusCheckRollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type mergedPRConnection struct {
	Nodes    []mergedPRNode `json:"nodes"`
	PageInfo mergedPageInfo `json:"pageInfo"`
}

func fetchMergedRepositoryPullRequests(options listOptions) ([]pullRequest, error) {
	acquisition, err := fetchMergedAcquisition(options, false)
	return acquisition.rows, err
}

func fetchMergedPRPage(client *mergedQueryClient, owner, name, cursor string, limit int) (*mergedPRConnection, error) {
	after := "null"
	if cursor != "" {
		after = fmt.Sprintf("%q", cursor)
	}
	query := fmt.Sprintf(`query { repository(owner: %q, name: %q) {
  pullRequests(states: MERGED, orderBy: {field: UPDATED_AT, direction: DESC}, first: %d, after: %s) {
    nodes { %s }
    pageInfo { hasNextPage endCursor }
  }
} }`, owner, name, limit, after, "number updatedAt mergedAt")
	data, err := client.query(query)
	if err != nil {
		return nil, err
	}
	return parseMergedPRPage(data)
}

func parseMergedPRPage(data []byte) (*mergedPRConnection, error) {
	var response struct {
		Data struct {
			Repository *struct {
				PullRequests *mergedPRConnection `json:"pullRequests"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode merged pull requests: %w", err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("merged pull requests: %s", response.Errors[0].Message)
	}
	if response.Data.Repository == nil || response.Data.Repository.PullRequests == nil {
		return nil, fmt.Errorf("merged pull request repository data unavailable")
	}
	return response.Data.Repository.PullRequests, nil
}

func (node mergedPRNode) pullRequestRow() pullRequest {
	pr := node.pullRequest
	pr.LatestReviews = node.LatestReviews.Nodes
	if len(node.Commits.Nodes) > 0 {
		if rollup := node.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil && rollup.Contexts != nil {
			for _, node := range rollup.Contexts.Nodes {
				check := node.checkItem
				check.WorkflowName = node.CheckSuite.WorkflowRun.Workflow.Name
				pr.StatusCheckRollup = append(pr.StatusCheckRollup, check)
			}
		}
	}
	return pr
}
