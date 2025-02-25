package main

import (
	"github.com/ColeHoward/Inferno/internal/server"
)

func main() {
	port := 8080
	bufferSize := 1024
	dataChan := make(chan server.ClientRequest, 100)
	serverConfig := server.ServerConfig{
		ListenPort:        port,
		BufferSize:        bufferSize,
		MaxConnections:    10_000,
		ReadTimeoutSec:    10,
		ConnectionTimeout: 10,
		MaxRequestSize:    1024,
	}

	server.StartServer(serverConfig, dataChan)
}
