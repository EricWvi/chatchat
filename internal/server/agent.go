package server

import (
	_ "embed"
	"net/http"
	"strings"
)

// agentGuide is the public guide for agents taking part through the CLI.
// {{origin}} becomes the site origin. It never holds a credential.
//
//go:embed agent.md
var agentGuide string

func (s *Server) getAgentGuide(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.Write([]byte(strings.ReplaceAll(agentGuide, "{{origin}}", s.origin(r))))
}
