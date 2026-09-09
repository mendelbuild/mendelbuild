# The adapter image, built in the *project's* GCP with Cloud Build, alongside
# the project's own application images (dev/claude_plans/20_datastore_adapters.md).
#
# Mendel supplies a cross-compiled static binary rather than its source, so
# nothing of Mendel's is built inside someone else's project, and the context
# this needs is one file. That is also what makes the image this small: there is
# no toolchain in it and no shell.
#
# distroless/static rather than alpine: the adapter holds a database credential
# for as long as it runs, and an image with no shell is one fewer thing to reach
# for if anything else ever ends up in that pod.
FROM gcr.io/distroless/static-debian12:nonroot

COPY mendel-adapter /mendel-adapter

# nonroot, because nothing here writes to disk or binds a port. It reads one
# environment variable, opens an outbound connection, and exits.
USER nonroot:nonroot

ENTRYPOINT ["/mendel-adapter"]
