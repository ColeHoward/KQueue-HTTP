package main

import (
	"context"
	"flag"

	"github.com/ColeHoward/KQueue-HTTP/internal/api"
	"github.com/ColeHoward/KQueue-HTTP/internal/server"
	"github.com/ColeHoward/KQueue-HTTP/internal/types"
)

func main() {
	useKqueue := flag.Bool("kqueue", false, "Run the kqueue-based server instead of the traditional net/http one")
	flag.Parse()

	if *useKqueue {
		router := api.NewRouter()

		router.RegisterRoute("/", func(req types.Request) (int, string, error) {
			return 200, "Hello World!", nil
		})

		router.RegisterRoute("/process", func(req types.Request) (int, string, error) {
			result := "result"
			return 200, result, nil
		})

		serverConfig := server.ServerConfig{
			ListenPort:     8080,
			BufferSize:     1024,
			MaxConnections: 10_000,
			MaxRequestSize: 1024 * 1024,
			NumWorkers:     100,
		}

		server.StartServer(serverConfig, router)
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		server.StartHTTPServer(ctx, 8080)
		defer cancel()
	}
}
