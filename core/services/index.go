package services

import (
	"html/template"
	"net/http"
	"sort"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// sortServed orders services by name, so the index reads the same way twice in
// a row.
func sortServed(list []Served) {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}

// indexPage renders what the host is forwarding. A webui is a link because a
// browser is what reaches it; anything else is an address to hand a client. The
// type decides that and nothing else — routing is identical for both.
var indexPage = template.Must(template.New("index").Parse(`<!doctype html>
<meta charset="utf-8">
<title>dabs services</title>
<h1>dabs services</h1>
{{if not .}}<p>No box is publishing a service.</p>{{end}}
<ul>
{{range .}}<li>
{{if .IsWebUI}}<a href="{{.URL}}">{{.Name}}</a>{{else}}{{.Name}}{{end}}
— {{.Addr}} — {{.Type}}{{if .Instance}} — {{.Instance}}{{end}}
</li>
{{end}}</ul>
`))

// IsWebUI reports whether the service is one a browser opens.
func (s Served) IsWebUI() bool { return s.Type == forwarder.TypeWebUI }

// ServeHTTP renders the index of everything the server currently forwards.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexPage.Execute(w, s.Serving())
}
