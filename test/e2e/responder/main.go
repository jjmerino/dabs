// Command responder is an e2e fixture: the trivial program a box publishes. It
// serves one fixed body on box-local loopback, so a request that comes back
// with that body proves it travelled the whole path — host port, unix socket,
// in-box forwarder, this server — and nothing shorter.
//
// Usage: responder --port <n> --body <text>
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := flag.Int("port", 0, "loopback port to serve")
	body := flag.String("body", "served from the box", "what every request is answered with")
	flag.Parse()
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, *body)
	})
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), nil); err != nil {
		fmt.Fprintln(os.Stderr, "responder:", err)
		os.Exit(1)
	}
}
