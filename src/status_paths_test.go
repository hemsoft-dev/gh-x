package main

import "testing"

// The fixtures below are verbatim `git status --porcelain=v2 --branch` output
// from Git 2.53.0 in a disposable repository (issue #275). Rename entries
// separate the new and original paths with a literal tab.

const statusPathsModifiedFixture = "# branch.oid 32b65f2b67ceb054233a6e81cab9beb110d712b7\n" +
	"# branch.head main\n" +
	"1 .M N... 100644 100644 100644 1b6093cc1e0f9a4692f15674eba0dd4acd9145ac 1b6093cc1e0f9a4692f15674eba0dd4acd9145ac a b.txt\n" +
	"1 .M N... 100644 100644 100644 fc08fe1e6cd38a2aad7d27d35f64450186b84d05 fc08fe1e6cd38a2aad7d27d35f64450186b84d05 c b.txt\n"

const statusPathsMixedFixture = "# branch.oid 32b65f2b67ceb054233a6e81cab9beb110d712b7\n" +
	"# branch.head main\n" +
	"1 .M N... 100644 100644 100644 1b6093cc1e0f9a4692f15674eba0dd4acd9145ac 1b6093cc1e0f9a4692f15674eba0dd4acd9145ac a b.txt\n" +
	"1 .M N... 100644 100644 100644 fc08fe1e6cd38a2aad7d27d35f64450186b84d05 fc08fe1e6cd38a2aad7d27d35f64450186b84d05 c b.txt\n" +
	"1 D. N... 100644 000000 000000 3744f204f8fa94cbaa9a273ff33896f81152c322 0000000000000000000000000000000000000000 gone b.txt\n" +
	"2 R. N... 100644 100644 100644 435fc21c7072fbe76fc7d5d9c9df07b82c97ce86 435fc21c7072fbe76fc7d5d9c9df07b82c97ce86 R100 new b.txt\told b.txt\n" +
	"1 .M N... 100644 100644 100644 492ca3279543d9c9fabbbefbc8160ae5e5170821 492ca3279543d9c9fabbbefbc8160ae5e5170821 plain.txt\n" +
	"1 A. N... 000000 100644 100644 0000000000000000000000000000000000000000 2f259b79aa7e263f5829bb6e98096e7ec976d998 staged b.txt\n" +
	"1 .M N... 100644 100644 100644 004f1deb0623c8f817a745a8163136b32ea7bb0b 004f1deb0623c8f817a745a8163136b32ea7bb0b trail.txt \n" +
	"1 .M N... 100644 100644 100644 011b83ce5b409cd036e8a0a1031e6c39afca31b8 011b83ce5b409cd036e8a0a1031e6c39afca31b8 \"\\303\\251 b.txt\"\n" +
	"? untracked b.txt\n"

const statusPathsConflictFixture = "# branch.oid c1a5554a9d10ede91331dd285daaae94dbbdf8b6\n" +
	"# branch.head main\n" +
	"u UU N... 100644 100644 100644 100644 fa11c31e031f3f40bc216e2638e06b0291c06801 88d050b1908057b53d38b42702ebc659e3d7f696 9292e48d0f5d9199ad05538f6f537fa0ffc3d1f0 conf b.txt\n" +
	"u UU N... 100644 100644 100644 100644 ea7660d1b2a7df0587c09ef29bfa48fcd9a20b37 88d050b1908057b53d38b42702ebc659e3d7f696 9292e48d0f5d9199ad05538f6f537fa0ffc3d1f0 conf2 b.txt\n"

func TestParseGitStatusCountsPathsWithSharedLastWord(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   statusSummary
	}{
		{
			name:   "two modified files ending in the same word",
			output: statusPathsModifiedFixture,
			want:   statusSummary{Branch: "main", Modified: 2},
		},
		{
			name:   "ordinary, rename, quoted and trailing-space entries",
			output: statusPathsMixedFixture,
			want:   statusSummary{Branch: "main", Staged: 3, Modified: 5, Deleted: 1, Renamed: 1, Untracked: 1},
		},
		{
			name:   "two unmerged files ending in the same word",
			output: statusPathsConflictFixture,
			want:   statusSummary{Branch: "main", Conflicted: 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseGitStatus(test.output); got != test.want {
				t.Fatalf("parseGitStatus() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestParseGitStatusCountsEachSpacedPathOnce(t *testing.T) {
	tests := []struct {
		name string
		line string
		want statusSummary
	}{
		{"ordinary", "1 .M N... 100644 100644 100644 1b6093cc1e0f9a4692f15674eba0dd4acd9145ac 1b6093cc1e0f9a4692f15674eba0dd4acd9145ac a b.txt", statusSummary{Modified: 1}},
		{"rename", "2 R. N... 100644 100644 100644 435fc21c7072fbe76fc7d5d9c9df07b82c97ce86 435fc21c7072fbe76fc7d5d9c9df07b82c97ce86 R100 new b.txt\told b.txt", statusSummary{Staged: 1, Renamed: 1}},
		{"unmerged", "u UU N... 100644 100644 100644 100644 fa11c31e031f3f40bc216e2638e06b0291c06801 88d050b1908057b53d38b42702ebc659e3d7f696 9292e48d0f5d9199ad05538f6f537fa0ffc3d1f0 conf b.txt", statusSummary{Conflicted: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			once := parseGitStatus(test.line + "\n")
			if once != test.want {
				t.Fatalf("one entry = %+v, want %+v", once, test.want)
			}
			// Git never repeats a path, but the dedupe must still collapse a
			// repeated entry to one count.
			if twice := parseGitStatus(test.line + "\n" + test.line + "\n"); twice != test.want {
				t.Fatalf("repeated entry = %+v, want %+v", twice, test.want)
			}
		})
	}
}

func TestStatusPathKeepsWholePorcelainV2Path(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"ordinary with spaces", "1 .M N... 100644 100644 100644 abc abc a b.txt", "a b.txt"},
		{"ordinary with trailing space", "1 .M N... 100644 100644 100644 abc abc trail.txt ", "trail.txt "},
		{"ordinary with leading space", "1 .M N... 100644 100644 100644 abc abc  lead.txt", " lead.txt"},
		{"ordinary quoted", "1 .M N... 100644 100644 100644 abc abc \"\\303\\251 b.txt\"", "\"\\303\\251 b.txt\""},
		{"rename keeps new path", "2 R. N... 100644 100644 100644 abc abc R100 new b.txt\told b.txt", "new b.txt"},
		{"copy keeps new path", "2 C. N... 100644 100644 100644 abc abc C75 copy b.txt\tsrc b.txt", "copy b.txt"},
		{"unmerged with spaces", "u UU N... 100644 100644 100644 100644 h1 h2 h3 conf b.txt", "conf b.txt"},
		{"untracked with spaces", "? untracked b.txt", "untracked b.txt"},
		{"ordinary without path", "1 .M N... 100644 100644 100644 abc abc", "1 .M N... 100644 100644 100644 abc abc"},
		{"rename without path", "2 R. N... 100644 100644 100644 abc abc R100", "2 R. N... 100644 100644 100644 abc abc R100"},
		{"unmerged without path", "u UU N... 100644 100644 100644 100644 h1 h2 h3", "u UU N... 100644 100644 100644 100644 h1 h2 h3"},
		{"unknown kind", "! ignored b.txt", "! ignored b.txt"},
		{"empty", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := statusPath(test.line); got != test.want {
				t.Fatalf("statusPath(%q) = %q, want %q", test.line, got, test.want)
			}
		})
	}
}

func TestParseGitStatusUnchangedForPathsWithoutSpaces(t *testing.T) {
	output := "# branch.oid abc123\n" +
		"# branch.head main\n" +
		"1 M. N... 100644 100644 100644 abc def staged.go\n" +
		"1 .M N... 100644 100644 100644 abc abc work.go\n" +
		"1 .D N... 100644 100644 000000 abc abc removed.go\n" +
		"2 R. N... 100644 100644 100644 abc abc R100 renamed.go\toriginal.go\n" +
		"u UU N... 100644 100644 100644 100644 h1 h2 h3 conflict.go\n" +
		"? new.go\n"
	want := statusSummary{Branch: "main", Staged: 2, Modified: 2, Deleted: 1, Renamed: 1, Untracked: 1, Conflicted: 1}
	if got := parseGitStatus(output); got != want {
		t.Fatalf("parseGitStatus() = %+v, want %+v", got, want)
	}
}
