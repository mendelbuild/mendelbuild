package main

import "testing"

// The bug this is here for: `probe <id> --image x --report-to y` was rejected
// with "--image and --report-to are both required", because Go's flag package
// stops at the first non-flag argument and the project id is the first
// argument. Every flag after it was left unparsed.
//
// It survived to a cluster because nothing else here can be tested without one.
func TestTheProjectIdMayComeBeforeTheFlags(t *testing.T) {
	const id = "d25227dc-99b8-4af2-9be1-e983c577474e"

	orders := map[string][]string{
		"id first":     {id, "--image", "img:1", "--report-to", "https://m/adapters/report"},
		"flags first":  {"--image", "img:1", "--report-to", "https://m/adapters/report", id},
		"id in middle": {"--image", "img:1", id, "--report-to", "https://m/adapters/report"},
	}
	for name, args := range orders {
		t.Run(name, func(t *testing.T) {
			opts, err := parseProbeArgs(args)
			if err != nil {
				t.Fatalf("parsing %v: %v", args, err)
			}
			if opts.ProjectID.String() != id {
				t.Errorf("project id = %s, want %s", opts.ProjectID, id)
			}
			if opts.Image != "img:1" || opts.ReportTo != "https://m/adapters/report" {
				t.Errorf("image = %q, report-to = %q", opts.Image, opts.ReportTo)
			}
			if opts.Force {
				t.Error("force is set without --force")
			}
		})
	}
}

// Each refusal names the one thing that is wrong. A probe costs a job in
// someone else's cluster, so being told which half of the line to fix is worth
// more here than in a command that only reads.
func TestAProbeIsRefusedForOneStatedReason(t *testing.T) {
	const id = "d25227dc-99b8-4af2-9be1-e983c577474e"

	cases := map[string][]string{
		"no project":     {"--image", "i", "--report-to", "u"},
		"no image":       {id, "--report-to", "u"},
		"no report-to":   {id, "--image", "i"},
		"not an id":      {"nonsense", "--image", "i", "--report-to", "u"},
		"two projects":   {id, id, "--image", "i", "--report-to", "u"},
		"unknown flag":   {id, "--image", "i", "--report-to", "u", "--wat"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProbeArgs(args); err == nil {
				t.Fatalf("parsing %v was accepted", args)
			}
		})
	}
}

func TestForceIsCarriedThrough(t *testing.T) {
	opts, err := parseProbeArgs([]string{
		"d25227dc-99b8-4af2-9be1-e983c577474e", "--image", "i", "--report-to", "u", "--force",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Force {
		t.Error("--force was given and did not arrive")
	}
}
