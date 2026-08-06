package services

import (
	"html/template"
	"net/http"
	"sort"

	"github.com/jjmerino/dabs/core/door"
)

// sortServed orders services by name, so the index reads the same way twice in
// a row.
func sortServed(list []Served) {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}

// indexPage renders what the host is forwarding. An answering webui is a link
// because a browser is what reaches it; anything else is an address to hand a
// client. The type decides that and nothing else — routing is identical for
// both.
var indexPage = template.Must(template.New("index").Parse(`<!doctype html>
<meta charset="utf-8">
<title>dabs services</title>
<h1>dabs services</h1>
{{if and (not .Served) (not .Conflicts)}}<p>No box is publishing a service.</p>{{end}}
<ul>
{{range .Served}}<li>
{{if .IsLink}}<a href="{{.URL}}">{{.Name}}</a>{{else}}{{.Name}}{{end}}
— {{.Addr}} — {{.Type}}{{if .Down}} — down{{end}}{{if .Instance}} — {{.Instance}}{{end}}
</li>
{{end}}{{range .Conflicts}}<li>
{{.Name}} — conflict: also published by {{.Node}}, not served — {{.Type}}{{if .Instance}} — {{.Instance}}{{end}}
</li>
{{end}}</ul>
`))

// IsLink reports whether the index renders the service as a link: a browsable
// service that is answering. A link says the address leads somewhere, so a
// service nothing answers behind is named, not linked.
func (s Served) IsLink() bool { return s.Type == door.TypeWebUI && !s.Down }

// indexData is what the index page renders: what the host forwards, and what it
// cannot because the name is taken.
type indexData struct {
	Served    []Served
	Conflicts []Service
}

// ServeHTTP renders the index of everything the server currently forwards.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexPage.Execute(w, indexData{Served: s.Serving(), Conflicts: s.Conflicts()})
}
