package web

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/experiment"
)

// Where a datastore adapter reports what it found.
//
// This is the one endpoint in Mendel that a process Mendel did not write posts
// to from the public internet, so it is worth being explicit about what it
// trusts. Four things, in order:
//
//  1. **The token, and only the token, says who this is.** §13 D10 settled that
//     for metrics ingest and the reasoning is identical: a body that names its
//     own project can name any project. The invocation is looked up by the
//     token's hash, and the project comes from the invocation.
//
//  2. **The instruction as asked is the reference**, not a reconstruction. The
//     stored instruction is what the result is checked against, so a report
//     answering a different phase or an earlier run is refused rather than
//     folded in.
//
//  3. **A report is checked before it is believed.** `Result.Validate` is the
//     conformance suite's posture at the other end of the wire — the adapter may
//     be something Mendel generated, and a malformed report is a fact about the
//     adapter rather than about the datastore.
//
//  4. **Nothing here concludes anything.** The result is recorded; whether the
//     project can run experiments is decided later, by code that reads it. An
//     endpoint that both accepts a claim and acts on it is one bug away from
//     acting on someone else's claim.
//
// Deliberately not authenticated as a user. The caller is a job in the project's
// own cluster, which has no session and no business having one.

// maxAdapterReport bounds what will be read from an untrusted poster. An
// admission report carries shapes and identities, which are small; anything
// larger is a mistake or an attempt, and reading it to find out which is the
// thing being declined.
const maxAdapterReport = 4 << 20 // 4 MiB

// handleAdapterReport accepts one result from one invocation.
func (s *Server) handleAdapterReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	token := bearerToken(r)
	if token == "" {
		// No detail: a caller with no token learns only that it needs one.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	inv, err := s.db.AdapterInvocationByToken(ctx, token)
	if err != nil || inv == nil {
		// Unknown and expired are answered identically on purpose, so a
		// presented token cannot be used to learn whether it was ever real.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdapterReport))
	if err != nil {
		http.Error(w, "could not read the report", http.StatusBadRequest)
		return
	}

	var result experiment.Result
	if err := json.Unmarshal(body, &result); err != nil {
		s.declineAdapterReport(ctx, w, inv.ID, "the report is not readable as JSON: "+err.Error())
		return
	}

	var asked experiment.Instruction
	if err := json.Unmarshal(inv.Instruction, &asked); err != nil {
		// Mendel's own record is unreadable, which is Mendel's problem and not
		// the adapter's. Say so rather than blaming the report.
		log.Printf("adapter report %s: stored instruction is unreadable: %v", inv.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if why := result.Validate(&asked); why != "" {
		s.declineAdapterReport(ctx, w, inv.ID, why)
		return
	}

	if err := s.db.RecordAdapterResult(ctx, inv.ID, string(result.Outcome), body); err != nil {
		// Already reported. Not an error the caller can act on, and answering
		// 409 rather than 500 is what tells a retrying job to stop rather than
		// keep trying.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// declineAdapterReport refuses a report and records why.
//
// Recorded rather than only returned, because the job that sent it is about to
// exit and nobody is reading its response. Storing the refusal as the
// invocation's outcome is what lets a page say the adapter answered badly —
// which is a different thing from the datastore being unsuitable, and where the
// adapter was generated it is what a retry is given to work from.
func (s *Server) declineAdapterReport(ctx context.Context, w http.ResponseWriter, id uuid.UUID, why string) {
	failure, err := json.Marshal(experiment.Result{
		Outcome: experiment.OutcomeFailed,
		Failure: "Mendel refused this adapter's report: " + why,
	})
	if err == nil {
		if recErr := s.db.RecordAdapterResult(ctx, id, string(experiment.OutcomeFailed), failure); recErr != nil {
			log.Printf("adapter report %s: refused (%s), and recording that failed: %v", id, why, recErr)
		}
	}
	http.Error(w, why, http.StatusBadRequest)
}

// bearerToken reads the credential a job was given, and nothing else. A token in
// a query string would be in Mendel's access logs and in whatever sits in front
// of it.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}
