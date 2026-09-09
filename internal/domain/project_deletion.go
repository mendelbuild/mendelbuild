package domain

// A ProjectDeletionBlocker is one live thing that stops a project being
// retired.
//
// Retiring a project marks a row. It does not reach out to Fly, to Cloud Run or
// to a cluster and stop anything, so a project with a demo still serving would
// go on serving it -- and the hosting meter would go on charging for it against
// a project that no page in the app still lists. Mendel refuses instead and
// names what is running, rather than hiding a bill nobody can find.
//
// The same shape as the experiment gates: a hand-written sentence saying what
// would make it true, rendered by the confirmation page and by the refusal
// alike, so the two cannot drift apart.
type ProjectDeletionBlocker struct {
	// Name is what is still live, phrased for a reader.
	Name string

	// Missing is the one sentence naming what has to happen first.
	Missing string

	// Path is where to go and do it. Empty when there is nowhere to send them,
	// which is a fact worth rendering rather than a link worth inventing.
	Path string
}
