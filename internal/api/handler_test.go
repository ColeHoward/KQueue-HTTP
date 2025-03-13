package api

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ColeHoward/Inferno/internal/server"
	"github.com/ColeHoward/Inferno/internal/types"
)

func TestServerWithRouter(t *testing.T) {
	router := NewRouter()

	router.RegisterRoute("/test", func(req types.Request) (int, string, error) {
		return 200, "Integration Test", nil
	})

	config := server.ServerConfig{
		ListenPort:     8082,
		BufferSize:     1024,
		MaxConnections: 10,
		MaxRequestSize: 1024,
	}

	go func() {
		server.StartServer(config, router)
	}()

	// Give the server time to start
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("tcp", "localhost:8082")
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	request := "GET /test HTTP/1.1\r\nHost: localhost\r\n\r\n"
	_, err = conn.Write([]byte(request))
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}

	buffer := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	response := string(buffer[:n])
	fmt.Println(string(response))

	if !contains(response, "200 OK") || !contains(response, "Integration Test") {
		t.Errorf("Unexpected response: %s", response)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
