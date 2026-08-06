package door

// PauseBeforeOpen makes the relay run fn between registering a publication and
// standing its listener up — the window a Close has to be able to land in
// without leaving a listener, a socket or a descriptor behind. Test-only: it is
// the one way to hold that window open on purpose instead of racing for it.
func (r *Relay) PauseBeforeOpen(fn func()) { r.beforeOpen = fn }

// ReportAfterOpen makes the relay hand fn the result of standing a
// publication's listener up, so a test can look at the registry once that
// attempt is over rather than guessing when it might be. Test-only.
func (r *Relay) ReportAfterOpen(fn func(error)) { r.afterOpen = fn }
