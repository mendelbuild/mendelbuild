// Command mendel-adapter runs one phase against a project's datastore and
// reports what it found.
//
// It runs as a Job in the project's own deployment channel, not in Mendel
// (§20 D54). Two consequences shape everything here:
//
//   - **It reaches the datastore the way the application does**, by reading the
//     same environment variable, because it runs where the application runs.
//     Mendel never needs a connection it could not use — a production database
//     on a private address is normal, and dialling it from outside is not.
//
//   - **It only ever talks outbound.** Nothing calls in. The instruction arrives
//     as an environment variable and the answer leaves as one POST, so nothing
//     here needs an address, a port, or an inbound path to a process holding
//     database credentials.
//
// The scaffolding is Mendel's and the adapter is not. A generated adapter
// supplies an `experiment.Datastore` and gets this main, this reporting and this
// error handling unchanged — which keeps the generated surface to the part the
// conformance suite actually checks, and keeps the part that handles a bearer
// token out of it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/experiment/pgstore"
)

// instructionEnv is where the Job's Secret is mounted.
const instructionEnv = "MENDEL_ADAPTER_INSTRUCTION"

// reportTimeout bounds the one call this makes. Generous: the run's own deadline
// is what actually stops it, and giving up on the report early would throw away
// work already done.
const reportTimeout = 30 * time.Second

func main() {
	log.SetFlags(0)
	if err := run(context.Background()); err != nil {
		// The exit code is for whoever reads the Job; Mendel learns from the
		// report, or from its absence.
		log.Fatalf("adapter: %v", err)
	}
}

func run(ctx context.Context) error {
	instruction, err := readInstruction(os.Getenv(instructionEnv))
	if err != nil {
		// Nothing to report against: without an instruction there is no
		// address, no token, and no invocation to answer for. Exiting is all
		// there is, and the Job's logs are the only record -- which is why the
		// manifest keeps them for an hour after it finishes.
		return err
	}

	result := perform(ctx, instruction)
	return report(ctx, instruction, result)
}

// readInstruction parses what the Job was given, and refuses it on the same
// terms Mendel would.
//
// Checked here as well as before the Job was created, because the two ends can
// be different versions: an adapter image built last month meeting an
// instruction written today should say so rather than act on the parts it
// recognises.
func readInstruction(raw string) (*experiment.Instruction, error) {
	if raw == "" {
		return nil, fmt.Errorf("no instruction: %s is empty", instructionEnv)
	}
	var instruction experiment.Instruction
	if err := json.Unmarshal([]byte(raw), &instruction); err != nil {
		return nil, fmt.Errorf("instruction is not readable as JSON: %w", err)
	}
	if why := instruction.Validate(); why != "" {
		return nil, fmt.Errorf("instruction cannot be acted on: %s", why)
	}
	return &instruction, nil
}

// perform does the phase and always produces a Result.
//
// Always, including when everything went wrong, because a failure Mendel hears
// about is worth much more than one it has to infer from silence. Silence is a
// third state Mendel has to handle anyway, but it cannot say *why* — and the
// commonest reason a datastore looks unsuitable is that something unrelated to
// the datastore went wrong.
func perform(ctx context.Context, instruction *experiment.Instruction) *experiment.Result {
	store, err := openDatastore(ctx, instruction)
	if err != nil {
		return experiment.FailedReport(instruction, err.Error())
	}
	defer store.Close()

	switch instruction.Phase {
	case experiment.PhaseProbe:
		return experiment.ProbeReport(instruction, experiment.RunProbe(ctx, store))
	default:
		// An image older than the instruction that asked. Naming the phase is
		// what tells a reader it is the adapter that is behind, rather than the
		// phase that is broken.
		return experiment.FailedReport(instruction,
			fmt.Sprintf("this adapter does not implement the %q phase", instruction.Phase))
	}
}

// datastore is a store plus whatever holding it open costs.
type datastore struct {
	experiment.Datastore
	Close func()
}

// openDatastore connects the way the application does.
//
// The connection string is read from the environment rather than taken from the
// instruction, so a credential Mendel already injected into this pod is not
// copied into a second place to say the same thing.
func openDatastore(ctx context.Context, instruction *experiment.Instruction) (*datastore, error) {
	conn := os.Getenv(instruction.DatastoreEnv)
	if conn == "" {
		return nil, fmt.Errorf("%s is not set in this deployment's environment, so there is no "+
			"datastore to look at. Mendel names the variable because it wrote the application; "+
			"if the application reads a different one, that is what should be named",
			instruction.DatastoreEnv)
	}

	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("could not open the datastore named by %s: %w", instruction.DatastoreEnv, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("could not reach the datastore named by %s: %w", instruction.DatastoreEnv, err)
	}
	// Not disposable: this is the application's own database, and the whole
	// arrangement exists so that nothing is verified against it.
	return &datastore{Datastore: pgstore.New(pool), Close: pool.Close}, nil
}

// report POSTs the result, and is the only thing here that talks to Mendel.
func report(ctx context.Context, instruction *experiment.Instruction, result *experiment.Result) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("could not render the result: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instruction.ReportTo, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not build the report: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The token is the whole of the authentication, and it goes in a header
	// rather than the URL so it is not in anyone's access logs.
	req.Header.Set("Authorization", "Bearer "+instruction.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Mendel to report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// Not retried. Mendel refuses a second report for one invocation, so a
		// retry could only ever be rejected -- and a Job the cluster restarts
		// would run the phase again, which for a phase that is not idempotent
		// is the thing the manifest sets backoffLimit 0 to prevent.
		return fmt.Errorf("Mendel refused the report with %s", resp.Status)
	}
	return nil
}
