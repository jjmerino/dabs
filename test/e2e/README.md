# dabs e2e tests

End-to-end tests that drive the real `dabs` **CLI** (not the library). The
**dab under test is built from the current source** at the start of each run,
so a change is exercised e2e with the least latency — an incremental
`go build`, no image rebuild.

**dabs picks the driver, the suite never does.** The tests assert on CLI
behavior only, so they run unchanged wherever dabs runs: Apple `container`
micro-VMs on macOS, bwrap boxes on Linux.

**The box is the isolation.** The suite REFUSES to run outside its dabs box
(it checks `DABS_NAME` and `/.dockerenv`) — so there is no isolated `$HOME` to
mint and nothing to clean up: every run gets a fresh box with its own `~/.dabs`,
and the box is reaped afterwards. Running `go test` on your host exits without
running anything.

## Run

```bash
./run_e2e.sh
```

That builds the inner base image, builds and boots the box, and runs the suite
inside it. The suite is behind `//go:build e2e`, so a plain `go test ./...`
stays hermetic and never touches sandboxes.

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
