package pgstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/experiment"
)

// Provisioning a verification database, Postgres-style.
//
// Everything dialect-specific about making a structural copy lives here, which
// is the division the rest of this package keeps: a second adapter is an
// addition, and one that cannot copy a structure returns ErrCannotProvision
// rather than approximating.
//
// The copy is schema-only and deliberately so. What admission compares is
// shapes, the experiment writes its own rows while it runs, and copying
// production's data into a database Mendel may reset would put the user's
// end-users' data somewhere nobody asked for it to be.
//
// Where this runs is not Mendel. It shells out to pg_dump and psql, which exist
// for one engine, and an adapter runs alongside the datastore it adapts -- in
// the project's own deployment channel, where the application already reaches
// its database. That is what keeps a datastore-specific dependency out of
// Mendel's image, and it is also the only arrangement that works at all for the
// common case of a database on a private address Mendel cannot dial.

// Provisioner makes verification databases on the server a Store is connected to.
type Provisioner struct {
	// admin is a connection to the server, used to CREATE and DROP. It is the
	// application's own connection unless the project supplied a more
	// privileged one, which is what CanProvision is for.
	admin *pgxpool.Pool

	// sourceURL is the database being copied. Needed alongside the pool because
	// pg_dump takes a connection string rather than an open connection.
	sourceURL string
}

// NewProvisioner returns a Provisioner over an open pool and the URL it came
// from.
func NewProvisioner(admin *pgxpool.Pool, sourceURL string) *Provisioner {
	return &Provisioner{admin: admin, sourceURL: sourceURL}
}

func (p *Provisioner) Kind() string { return "postgres" }

// CanProvision attempts a create and a drop rather than reading grants.
//
// The same reasoning §16 applies to installing a cluster controller: a
// privilege is the union of things granted in several places, and the only
// reliable question is the one the server answers. CREATEDB is a role attribute
// rather than a database grant, so `has_database_privilege` cannot be asked
// about it at all -- which makes an attempt both the most reliable check and
// very nearly the only one.
//
// Failing to clean up the probe is not a failure to provision: what was being
// established is whether the create succeeds.
func (p *Provisioner) CanProvision(ctx context.Context) error {
	if p.admin == nil {
		return fmt.Errorf("%w: no connection to the datastore server", experiment.ErrCannotProvision)
	}
	probe := "mendel_verify_probe_" + randomSuffix()

	if _, err := p.admin.Exec(ctx, `CREATE DATABASE `+quoteIdent(probe)); err != nil {
		return fmt.Errorf("this credential cannot create a database on the server, so Mendel has "+
			"nowhere to verify migrations: %w", err)
	}
	_, _ = p.admin.Exec(ctx, `DROP DATABASE `+quoteIdent(probe))
	return nil
}

// Provision creates a database whose structure matches the source and returns a
// disposable Store over it.
//
// The returned drop function is the whole of teardown, which is one of the
// reasons for a database per experiment rather than a shared one: withdrawing an
// experiment's verification state is a DROP DATABASE, with nothing to reverse
// and nothing left for the next experiment to collide with.
func (p *Provisioner) Provision(ctx context.Context, name string) (experiment.Datastore, func(context.Context) error, error) {
	if p.admin == nil || strings.TrimSpace(p.sourceURL) == "" {
		return nil, nil, fmt.Errorf("%w: no connection to copy from", experiment.ErrCannotProvision)
	}
	dbName := VerifyDatabaseName(name)

	if _, err := p.admin.Exec(ctx, `CREATE DATABASE `+quoteIdent(dbName)); err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", dbName, err)
	}
	drop := func(ctx context.Context) error {
		// FORCE closes any connection still open on it. Without it a drop races
		// a pool that has not finished closing, and the failure is a database
		// left behind rather than anything a reader sees.
		_, err := p.admin.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)+` WITH (FORCE)`)
		return err
	}

	targetURL := ReplaceDatabase(p.sourceURL, dbName)
	if err := copySchema(ctx, p.sourceURL, targetURL); err != nil {
		_ = drop(ctx)
		return nil, nil, err
	}

	pool, err := pgxpool.New(ctx, targetURL)
	if err != nil {
		_ = drop(ctx)
		return nil, nil, fmt.Errorf("connect to %s: %w", dbName, err)
	}
	return NewScratch(pool), func(ctx context.Context) error {
		pool.Close()
		return drop(ctx)
	}, nil
}

// copySchema replicates structure and no rows.
//
// Shelling out to pg_dump rather than reading the catalogue and regenerating
// DDL. The catalogue route is a partial reimplementation of pg_dump that gets
// constraints, sequences, defaults and custom types subtly wrong, and being
// subtly wrong here means admission comparing a copy against production and
// finding a difference it invented. Driving a real tool is also the call this
// codebase already makes for deploys: deterministic Go around gcloud and
// flyctl, rather than generated scripts.
func copySchema(ctx context.Context, sourceURL, targetURL string) error {
	dump := exec.CommandContext(ctx, "pg_dump", "--schema-only", "--no-owner", "--no-privileges", sourceURL)
	out, err := dump.Output()
	if err != nil {
		return fmt.Errorf("%w: could not read production's structure with pg_dump (%s). Mendel needs "+
			"it to make a verification database that matches",
			experiment.ErrCannotProvision, commandError(err))
	}

	restore := exec.CommandContext(ctx, "psql", "--quiet", "--no-psqlrc",
		"--set", "ON_ERROR_STOP=1", targetURL)
	restore.Stdin = strings.NewReader(string(out))
	if _, err := restore.Output(); err != nil {
		return fmt.Errorf("applying production's structure to the verification database failed: %s",
			commandError(err))
	}
	return nil
}

// commandError prefers what the tool said on stderr over its exit status, since
// "exit status 1" tells a reader nothing they can act on.
func commandError(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

// VerifyDatabaseName is what a sandbox is called: recognisably Mendel's, tied to
// the experiment, and a legal identifier.
//
// The prefix matters for the same reason `mendel_exp_` does on the objects
// inside one. A person looking at a list of databases on their own server should
// be able to tell at a glance which are theirs and which Mendel made, without
// having to ask.
func VerifyDatabaseName(name string) string {
	safe := unsafeIdentChars.ReplaceAllString(strings.ToLower(name), "_")
	safe = strings.Trim(safe, "_")
	if safe == "" {
		safe = randomSuffix()
	}
	full := "mendel_verify_" + safe
	if len(full) <= 63 {
		return full
	}
	// Postgres truncates identifiers at 63 bytes silently, so two long names
	// sharing a prefix would land in one database and quietly stop being
	// sandboxed from each other. Truncating here is not enough on its own --
	// it moves the collision rather than removing it -- so what is kept is a
	// shortened name plus a digest of the whole one, which two different names
	// cannot share.
	sum := sha256.Sum256([]byte(name))
	tag := hex.EncodeToString(sum[:4])
	return full[:63-len(tag)-1] + "_" + tag
}

var unsafeIdentChars = regexp.MustCompile(`[^a-z0-9_]+`)

// ReplaceDatabase points a connection string at a different database on the
// same server, keeping credentials and options.
func ReplaceDatabase(url, dbName string) string {
	base, query, hasQuery := strings.Cut(url, "?")
	slash := strings.LastIndex(base, "/")
	if slash < 0 {
		return url
	}
	out := base[:slash+1] + dbName
	if hasQuery {
		out += "?" + query
	}
	return out
}

func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "x"
	}
	return hex.EncodeToString(b[:])
}
