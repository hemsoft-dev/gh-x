package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestValidateRuleset(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *ruleset)
		wantErr string
	}{
		{name: "versioned ruleset is valid"},
		{
			name: "unattributed-change approval is omitted",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequireExtraApprovalForUnattributedChanges = nil
			},
			wantErr: "unattributed-change extra approval must remain enabled",
		},
		{
			name: "unattributed-change approval is disabled",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequireExtraApprovalForUnattributedChanges = boolPointer(false)
			},
			wantErr: "unattributed-change extra approval must remain enabled",
		},
		{
			name: "merge methods are omitted",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").AllowedMergeMethods = nil
			},
			wantErr: "merge, squash and rebase methods must remain allowed",
		},
		{
			name: "merge method is removed",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").AllowedMergeMethods = []string{"squash", "rebase"}
			},
			wantErr: "merge, squash and rebase methods must remain allowed",
		},
		{
			name: "status check rule is missing",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = rulesOfType(*config, "pull_request")
			},
			wantErr: "must define one required-status-check rule",
		},
		{
			name: "pull request rule is missing",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = rulesOfType(*config, "required_status_checks")
			},
			wantErr: "must define one pull-request rule",
		},
		{
			name: "deletion rule is missing",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = rulesExcept(*config, "deletion")
			},
			wantErr: "must define one deletion rule that restricts deleting main",
		},
		{
			name: "non-fast-forward rule is missing",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = rulesExcept(*config, "non_fast_forward")
			},
			wantErr: "must define one non-fast-forward rule that blocks force pushes to main",
		},
		{
			name: "deletion rule is duplicated",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = append(config.Rules, rulesetRule{Type: "deletion"})
			},
			wantErr: "must define one deletion rule",
		},
		{
			name: "non-fast-forward rule is duplicated",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = append(config.Rules, rulesetRule{Type: "non_fast_forward"})
			},
			wantErr: "must define one non-fast-forward rule",
		},
		{
			name: "unsupported rule is added",
			mutate: func(_ *testing.T, config *ruleset) {
				config.Rules = append(config.Rules, rulesetRule{Type: "creation"})
			},
			wantErr: "must define only status-check, pull-request, deletion and non-fast-forward rules",
		},
		{
			name: "deletion rule defines parameters",
			mutate: func(t *testing.T, config *ruleset) {
				ruleFor(t, config, "deletion").Parameters = &rulesetParameters{}
			},
			wantErr: "deletion rule must not define parameters",
		},
		{
			name: "non-fast-forward rule defines parameters",
			mutate: func(t *testing.T, config *ruleset) {
				ruleFor(t, config, "non_fast_forward").Parameters = &rulesetParameters{}
			},
			wantErr: "non_fast_forward rule must not define parameters",
		},
		{
			name: "status check parameters are missing",
			mutate: func(t *testing.T, config *ruleset) {
				ruleFor(t, config, "required_status_checks").Parameters = nil
			},
			wantErr: "required-status-check rule must define parameters",
		},
		{
			name: "pull request parameters are missing",
			mutate: func(t *testing.T, config *ruleset) {
				ruleFor(t, config, "pull_request").Parameters = nil
			},
			wantErr: "pull-request rule must define parameters",
		},
		{
			name: "thread resolution is omitted",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequiredReviewThreadResolution = nil
			},
			wantErr: "require resolved review threads",
		},
		{
			name: "thread resolution is disabled",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequiredReviewThreadResolution = boolPointer(false)
			},
			wantErr: "require resolved review threads",
		},
		{
			name: "approval count is omitted",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequiredApprovingReviewCount = nil
			},
			wantErr: "exactly zero approving reviews",
		},
		{
			name: "approval count changes",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequiredApprovingReviewCount = intPointer(1)
			},
			wantErr: "exactly zero approving reviews",
		},
		{
			name: "stale review dismissal is enabled",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").DismissStaleReviewsOnPush = boolPointer(true)
			},
			wantErr: "stale-review dismissal must remain disabled",
		},
		{
			name: "code owner review is enabled",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequireCodeOwnerReview = boolPointer(true)
			},
			wantErr: "code-owner review must remain disabled",
		},
		{
			name: "last push approval is enabled",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "pull_request").RequireLastPushApproval = boolPointer(true)
			},
			wantErr: "last-push approval must remain disabled",
		},
		{
			name: "quality gate changes",
			mutate: func(t *testing.T, config *ruleset) {
				parametersFor(t, config, "required_status_checks").RequiredStatusChecks[0].Context = "Other Gate"
			},
			wantErr: "require Quality Gate from GitHub Actions",
		},
		{
			name: "bypass actor is added",
			mutate: func(_ *testing.T, config *ruleset) {
				config.BypassActors = []json.RawMessage{json.RawMessage(`{"actor_type":"RepositoryRole"}`)}
			},
			wantErr: "must not have bypass actors",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := loadTestRuleset(t)
			if test.mutate != nil {
				test.mutate(t, &config)
			}

			err := validateRuleset(config)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRuleset() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateRuleset() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func loadTestRuleset(t *testing.T) ruleset {
	t.Helper()
	data, err := os.ReadFile("../rulesets/main.json")
	if err != nil {
		t.Fatalf("read ruleset: %v", err)
	}
	var config ruleset
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse ruleset: %v", err)
	}
	return config
}

func ruleFor(t *testing.T, config *ruleset, ruleType string) *rulesetRule {
	t.Helper()
	for i := range config.Rules {
		if config.Rules[i].Type == ruleType {
			return &config.Rules[i]
		}
	}
	t.Fatalf("missing %s rule", ruleType)
	return nil
}

func parametersFor(t *testing.T, config *ruleset, ruleType string) *rulesetParameters {
	t.Helper()
	parameters := ruleFor(t, config, ruleType).Parameters
	if parameters == nil {
		t.Fatalf("%s rule has no parameters", ruleType)
	}
	return parameters
}

func rulesExcept(config ruleset, ruleType string) []rulesetRule {
	remaining := make([]rulesetRule, 0, len(config.Rules))
	for _, rule := range config.Rules {
		if rule.Type != ruleType {
			remaining = append(remaining, rule)
		}
	}
	return remaining
}

// The versioned file must carry the protections as type-only objects, so a
// parameters key (even an empty or null one) cannot hide in the JSON source.
func TestVersionedProtectionsAreTypeOnly(t *testing.T) {
	data, err := os.ReadFile("../rulesets/main.json")
	if err != nil {
		t.Fatalf("read ruleset: %v", err)
	}
	var raw struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse ruleset: %v", err)
	}
	found := map[string]int{}
	for _, rule := range raw.Rules {
		var ruleType string
		if err := json.Unmarshal(rule["type"], &ruleType); err != nil {
			t.Fatalf("parse rule type: %v", err)
		}
		if ruleType != "deletion" && ruleType != "non_fast_forward" {
			continue
		}
		found[ruleType]++
		if len(rule) != 1 {
			t.Errorf("%s rule has keys beyond type: %d keys", ruleType, len(rule))
		}
	}
	if found["deletion"] != 1 || found["non_fast_forward"] != 1 {
		t.Fatalf("protection rule counts = %v, want one deletion and one non_fast_forward", found)
	}
}

// Removing either protection from the file must stop the same entry point
// that `go run .github/scripts/validate-main-ruleset.go` uses.
func TestRepositoryValidationRejectsRulesetWithoutProtection(t *testing.T) {
	for ruleType, wantErr := range map[string]string{
		"deletion":         "must define one deletion rule",
		"non_fast_forward": "must define one non-fast-forward rule",
	} {
		t.Run(ruleType, func(t *testing.T) {
			config := loadTestRuleset(t)
			config.Rules = rulesExcept(config, ruleType)
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatalf("encode ruleset: %v", err)
			}
			root := t.TempDir()
			copyRepositoryInputs(t, root)
			writeTestFile(t, root+"/.github/rulesets/main.json", data)
			t.Chdir(root)
			if err := validateRepository(); err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("validateRepository() error = %v, want substring %q", err, wantErr)
			}
		})
	}
}

func copyRepositoryInputs(t *testing.T, root string) {
	t.Helper()
	for _, path := range []string{
		".github/workflows/ci.yml",
		".github/workflows/auto-release.yml",
		".github/scripts/verify-authoritative-run.sh",
		".github/quality-tools.env",
	} {
		data, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		writeTestFile(t, root+"/"+path, data)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(path[:strings.LastIndex(path, "/")], 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func TestIsPinnedAction(t *testing.T) {
	tests := []struct {
		name           string
		uses           string
		expectedAction string
		want           bool
	}{
		{
			name:           "valid 40-character hex commit SHA",
			uses:           "github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa938",
			expectedAction: "github/codeql-action/init",
			want:           true,
		},
		{
			name:           "mutable major tag is rejected",
			uses:           "github/codeql-action/init@v4",
			expectedAction: "github/codeql-action/init",
			want:           false,
		},
		{
			name:           "mutable branch is rejected",
			uses:           "github/codeql-action/init@main",
			expectedAction: "github/codeql-action/init",
			want:           false,
		},
		{
			name:           "short SHA is rejected",
			uses:           "github/codeql-action/init@cdf488f",
			expectedAction: "github/codeql-action/init",
			want:           false,
		},
		{
			name:           "mismatched action is rejected",
			uses:           "github/codeql-action/analyze@cdf488f595d80d6e07e03d4674febd5ab45fa938",
			expectedAction: "github/codeql-action/init",
			want:           false,
		},
		{
			name:           "missing delimiter is rejected",
			uses:           "github/codeql-action/init",
			expectedAction: "github/codeql-action/init",
			want:           false,
		},
		{
			name:           "non-hex character is rejected",
			uses:           "github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa93z",
			expectedAction: "github/codeql-action/init",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPinnedAction(tt.uses, tt.expectedAction); got != tt.want {
				t.Errorf("isPinnedAction(%q, %q) = %v, want %v", tt.uses, tt.expectedAction, got, tt.want)
			}
		})
	}
}

func TestActionSHA(t *testing.T) {
	uses := "github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa938"
	want := "cdf488f595d80d6e07e03d4674febd5ab45fa938"
	if got := actionSHA(uses); got != want {
		t.Errorf("actionSHA(%q) = %q, want %q", uses, got, want)
	}
}

func TestActionVersionComment(t *testing.T) {
	tests := []struct {
		name    string
		content string
		action  string
		wantVer string
		wantOk  bool
	}{
		{
			name: "matches pinned action with version comment",
			content: `      - name: Initialize CodeQL
        uses: github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa938 # v4.37.9
`,
			action:  "github/codeql-action/init",
			wantVer: "v4.37.9",
			wantOk:  true,
		},
		{
			name:    "handles CRLF line endings",
			content: "      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6.1.0\r\n",
			action:  "actions/checkout",
			wantVer: "v6.1.0",
			wantOk:  true,
		},
		{
			name: "missing version comment returns false",
			content: `      - name: Initialize CodeQL
        uses: github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa938
`,
			action:  "github/codeql-action/init",
			wantVer: "",
			wantOk:  false,
		},
		{
			name: "unpinned action returns false",
			content: `      - name: Initialize CodeQL
        uses: github/codeql-action/init@v4 # v4.37.9
`,
			action:  "github/codeql-action/init",
			wantVer: "",
			wantOk:  false,
		},
		{
			name: "does not match comment on subsequent line",
			content: `      - name: Initialize CodeQL
        uses: github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa938
        # v4.37.9
`,
			action:  "github/codeql-action/init",
			wantVer: "",
			wantOk:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVer, gotOk := actionVersionComment(tt.content, tt.action)
			if gotOk != tt.wantOk || gotVer != tt.wantVer {
				t.Errorf("actionVersionComment() = (%q, %v), want (%q, %v)", gotVer, gotOk, tt.wantVer, tt.wantOk)
			}
		})
	}
}

func TestVersionedWorkflowValidation(t *testing.T) {
	t.Chdir("../..")
	if err := validateRepository(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryValidationFailureReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := validateRepository(); err == nil || !strings.Contains(err.Error(), "read .github/rulesets/main.json") {
		t.Fatalf("missing configuration error = %v", err)
	}
}
