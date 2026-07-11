# dabs e2e tests

End-to-end tests that drive the real `dabs` **CLI** (not the library). The
**dab under test is built from the current source** at the start of each run,
so a change is exercised e2e with the least latency — an incremental
`go build`, no image rebuild.

**dabs picks the driver, the suite never does.** The tests assert on CLI
behavior only, so they run unchanged wherever dabs runs: Apple `container`
micro-VMs on macOS, bwrap boxes on Linux.

**No double-wrapping.** Isolation is an isolated `$HOME` (a fresh `~/.dabs`,
removed on teardown) plus unique per-box names. Assertions only ever concern
this suite's own `dabs-e2e-*` boxes, so concurrent runs and any other boxes
on the machine can't interfere — no outer sandbox needed.

## Run

On any machine where dabs works (macOS with `container`, or Linux with
`bwrap` + `docker`), and with Go installed:

```bash
go test -tags e2e ./test/e2e
```

`$DABS_UNDER_TEST` overrides the binary under test (e.g. to test an installed/stable
one instead of building from source). The suite is behind `//go:build e2e`,
so a plain `go test ./...` stays hermetic and never touches sandboxes.

## Layout

- `e2e_test.go` — one file: `TestMain` builds the dab under test from source
  and builds the base image; one `test_*` per CLI behavior.
- `dabs.yaml` + `Dockerfile` — the base recipe (`dabs-e2e`) the inner boxes
  come from; `dabs build`/`up` resolve it.

## Notes

- The isolated `$HOME` fully isolates the bwrap driver (its state lives in
  `~/.dabs`). Apple `container` state is global to the machine, so on macOS
  isolation rests on the unique names + own-name-scoped assertions above.
- `TestBuild` exercises the `build` verb, which needs the platform's image
  builder (`container` / `docker`); the rest is build-free.
