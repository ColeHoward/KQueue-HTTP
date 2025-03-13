package api

import (
	"testing"

	"github.com/ColeHoward/Inferno/internal/types"
)

func TestRouter(t *testing.T) {

	router := NewRouter()

	router.RegisterRoute("/test", func(req types.Request) (int, string, error) {
		return 200, "Test Handler", nil
	})

	req := types.Request{
		FD:   999,
		Data: []byte("GET /test HTTP/1.1\r\nHost: example.com\r\n\r\n"),
	}

	statusCode, body, err := router.Handle(req)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if statusCode != 200 {
		t.Errorf("Expected status code 200, got %d", statusCode)
	}
	if body != "Test Handler" {
		t.Errorf("Expected body 'Test Handler', got '%s'", body)
	}
}
