package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func setupTest() {
	go main()
	time.Sleep(100 * time.Millisecond)
}

// test a standard HTTP request
func TestServerBasicRequest(t *testing.T) {
	setupTest()

	conn, err := net.Dial("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	// send request
	request := "POST / HTTP/1.1\r\nContent-Length: 13\r\n\r\nHello, world!"
	_, err = conn.Write([]byte(request))
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}

	// read response
	response := make([]byte, 1024)
	n, err := conn.Read(response)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	// verify response
	if !bytes.Contains(response[:n], []byte("200 OK")) {
		t.Errorf("Expected 200 OK response, got: %s", response[:n])
	}
}

// test fragmented requests
func TestServerPartialRequests(t *testing.T) {
	setupTest()

	conn, err := net.Dial("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	parts := []string{
		"POST / HTTP/1.1\r\n",
		"Content-Length: 13\r\n",
		"\r\n",
		"Hello, world!",
	}

	for _, part := range parts {
		_, err := conn.Write([]byte(part))
		if err != nil {
			t.Fatalf("Failed to send request part: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	response := make([]byte, 1024)
	n, err := conn.Read(response)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if !bytes.Contains(response[:n], []byte("200 OK")) {
		t.Errorf("Expected 200 OK response, got: %s", response[:n])
	}
}

// test multiple requests on same connection
func TestServerMultipleRequests(t *testing.T) {
	setupTest()

	conn, err := net.Dial("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	for i := range 3 {
		request := fmt.Sprintf("POST / HTTP/1.1\r\nContent-Length: 14\r\n\r\nHello, world%d!", i)

		_, err := conn.Write([]byte(request))
		if err != nil {
			t.Fatalf("Failed to send request %d: %v", i, err)
		}

		response := make([]byte, 1024)
		n, err := conn.Read(response)
		if err != nil {
			t.Fatalf("Failed to read response %d: %v", i, err)
		}

		if !bytes.Contains(response[:n], []byte("200 OK")) {
			t.Errorf("Request %d: Expected 200 OK response, got: %s", i, response[:n])
		}
	}
}

// test multiple simultaneous connections
func TestServerConcurrentConnections(t *testing.T) {
	setupTest()

	numClients := 5
	done := make(chan bool, numClients)

	for i := 0; i < numClients; i++ {
		go func(clientID int) {
			conn, err := net.Dial("tcp", "127.0.0.1:8080")
			if err != nil {
				t.Errorf("Client %d failed to connect: %v", clientID, err)
				done <- false
				return
			}
			defer conn.Close()

			request := fmt.Sprintf("POST / HTTP/1.1\r\nContent-Length: 14\r\n\r\nHello, world%d!", clientID)
			_, err = conn.Write([]byte(request))
			if err != nil {
				t.Errorf("Client %d failed to send request: %v", clientID, err)
				done <- false
				return
			}

			response := make([]byte, 1024)
			n, err := conn.Read(response)
			if err != nil {
				t.Errorf("Client %d failed to read response: %v", clientID, err)
				done <- false
				return
			}

			if !bytes.Contains(response[:n], []byte("200 OK")) {
				t.Errorf("Client %d: Expected 200 OK response, got: %s", clientID, response[:n])
				done <- false
				return
			}

			done <- true
		}(i)
	}

	// wait for all clients to complete
	for range numClients {
		if !<-done {
			t.Fatal("One or more clients failed")
		}
	}
}

// test malformed requests
func TestServerInvalidRequests(t *testing.T) {
	setupTest()

	testCases := []struct {
		name    string
		request string
		expect  string
	}{
		{
			name:    "Invalid HTTP version",
			request: "POST / HTTP/99\r\nContent-Length: 13\r\n\r\nHello, world!",
			expect:  "400",
		},
		{
			name:    "Missing Content-Length",
			request: "POST / HTTP/1.1\r\n\r\nHello, world!",
			expect:  "400",
		},
		{
			name:    "Invalid Content-Length",
			request: "POST / HTTP/1.1\r\nContent-Length: abc\r\n\r\nHello, world!",
			expect:  "400",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", "127.0.0.1:8080")
			if err != nil {
				t.Fatalf("Failed to connect to server: %v", err)
			}
			defer conn.Close()

			_, err = conn.Write([]byte(tc.request))
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}

			response := make([]byte, 1024)
			n, err := conn.Read(response)
			if err != nil && err != io.EOF {
				t.Fatalf("Failed to read response: %v", err)
			}

			if !bytes.Contains(response[:n], []byte(tc.expect)) {
				t.Errorf("Expected response containing %s, got: %s", tc.expect, response[:n])
			}
		})
	}
}
