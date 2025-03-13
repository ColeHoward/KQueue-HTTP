package types

// represents a client request
type Request struct {
	FD   int
	Data []byte
}

// defines how requests should be processed
type Handler interface {
	// processes a request and returns the response data
	Handle(req Request) (statusCode int, responseBody string, err error)
}

// function type that implements Handler
type HandlerFunc func(req Request) (statusCode int, responseBody string, err error)

// calls f(req)
func (f HandlerFunc) Handle(req Request) (statusCode int, responseBody string, err error) {
	return f(req)
}
