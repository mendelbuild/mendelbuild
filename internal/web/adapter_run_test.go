package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/experiment"
)

// The instruction a probe is started with, built the way startProbe builds it.
//
// Worth its own test because the ordering is easy to get wrong and the failure
// is invisible until a cluster is involved: the token is minted by creating the
// invocation, so an instruction assembled before that has no token, does not
// validate, and every probe fails for a reason that has nothing to do with the
// datastore.
func TestAProbeInstructionIsOnlyValidOnceTheTokenExists(t *testing.T) {
	withoutToken := ProbeInstructionFor("inv-1", "https://mendel.example/adapters/report", "", "DATABASE_URL")
	if withoutToken.Validate() == "" {
		t.Error("an instruction with no token names somewhere to report and no way to authenticate; " +
			"a job carrying it would be turned away")
	}

	whole := ProbeInstructionFor("inv-1", "https://mendel.example/adapters/report", "tok", "DATABASE_URL")
	if why := whole.Validate(); why != "" {
		t.Errorf("a complete probe instruction was refused: %s", why)
	}
}

// What Mendel keeps is the question, not the credential. The stored instruction
// exists so a report can be checked against what was asked, and none of that
// checking needs the token -- so keeping it would be storing a credential for no
// purpose, in the one place designed to be read back later.
func TestTheStoredInstructionHasNoToken(t *testing.T) {
	instruction := ProbeInstructionFor("inv-1", "https://mendel.example/adapters/report",
		"minted-per-invocation", "DATABASE_URL")

	stored := redactedInstruction(instruction)
	if strings.Contains(string(stored), "minted-per-invocation") {
		t.Fatalf("the token was stored: %s", stored)
	}

	// And what is left still answers the question the stored copy exists for:
	// which phase and which invocation a report has to match.
	var back experiment.Instruction
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Phase != instruction.Phase || back.InvocationID != instruction.InvocationID {
		t.Errorf("redaction lost what a report is checked against: %+v", back)
	}
	if back.DatastoreEnv != "DATABASE_URL" {
		t.Error("redaction should take the credential and nothing else")
	}

	// The original is untouched -- it is about to be handed to the job.
	if instruction.Token == "" {
		t.Error("redacting for storage must not empty the instruction being sent")
	}
}
