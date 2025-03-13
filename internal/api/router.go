package api

import (
	"strings"

	"github.com/ColeHoward/Inferno/internal/types"
)

type Router struct {
	routes         map[string]types.Handler
	defaultHandler types.Handler
}

func NewRouter() *Router {
	return &Router{
		routes: make(map[string]types.Handler),
		defaultHandler: types.HandlerFunc(func(req types.Request) (int, string, error) {
			return 404, "Not Found", nil
		}),
	}
}

// adds a handler for a specific path
func (r *Router) RegisterRoute(path string, f func(req types.Request) (int, string, error)) {
	h := types.HandlerFunc(f)
	r.routes[path] = h
}

func (r *Router) SetDefaultHandler(h types.Handler) {
	r.defaultHandler = h
}

// processes a request by finding the appropriate handler
func (r *Router) Handle(req types.Request) (int, string, error) {
	// ASSUMES REQUEST IS WELL-FORMED
	data := string(req.Data)
	firstLine := strings.Split(data, "\r\n")[0]
	parts := strings.Split(firstLine, " ")

	if len(parts) < 2 {
		return 400, "Bad Request", nil
	}

	path := parts[1]

	if handler, ok := r.routes[path]; ok {
		return handler.Handle(req)
	}

	for pattern, handler := range r.routes {
		if strings.HasSuffix(pattern, "/*") &&
			strings.HasPrefix(path, pattern[:len(pattern)-2]) {
			return handler.Handle(req)
		}
	}

	// No match found
	return r.defaultHandler.Handle(req)
}
