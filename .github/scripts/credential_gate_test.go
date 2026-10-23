package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCredentialGateRejectsSkippedOrWeakenedScanning(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*workflowJob)
	}{
		{"conditional job", func(job *workflowJob) { job.If = "false" }},
		{"allowed failure", func(job *workflowJob) { job.ContinueOnError = true }},
		{"shallow checkout", func(job *workflowJob) { job.Steps[0].With["fetch-depth"] = "1" }},
		{"conditional scan", func(job *workflowJob) { job.Steps[4].If = "false" }},
		{"allowed scan failure", func(job *workflowJob) { job.Steps[4].ContinueOnError = true }},
		{"skip real fixtures", func(job *workflowJob) { job.Steps[3].Run = "echo skipped" }},
		{"ignored scan status", func(job *workflowJob) { job.Steps[4].Run += " || true" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile("../workflows/ci.yml")
			if err != nil {
				t.Fatal(err)
			}
			var ci workflow
			if err := yaml.Unmarshal(raw, &ci); err != nil {
				t.Fatal(err)
			}
			job := ci.Jobs["credential-scan"]
			if !credentialScanReady(job) {
				t.Fatal("actual credential job did not qualify")
			}
			test.mutate(&job)
			if credentialScanReady(job) {
				t.Fatal("weakened credential job qualified")
			}
			gate := ci.Jobs["gate"]
			gate.Steps[0].Run = strings.Replace(gate.Steps[0].Run, `"${{ needs.credential-scan.result }}" != "success"`, `"success" != "success"`, 1)
			if windowsAggregateGateReady(gate) {
				t.Fatal("aggregate accepted ignored scanner failure")
			}
		})
	}
}
