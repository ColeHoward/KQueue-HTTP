package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ColeHoward/KQueue-HTTP/internal/types"
    "github.com/ColeHoward/KQueue-HTTP/internal/socket"
	"golang.org/x/sys/unix"
)

var (
	connBuffers = make(map[int]*bytes.Buffer)
    sockets     = make(map[int]*socket.Socket)
    sockMu        sync.RWMutex
	mutex         sync.RWMutex 
	bufferPool  = &sync.Pool{
		New: func() any {
			buff := make([]byte, 8192)
			return buff
		},
	}
)

type ServerConfig struct {
	ListenPort     int
	BufferSize     int
	MaxConnections int
	MaxRequestSize int
	NumWorkers     int
}

var (
	activeConnections int64
	maxConnections    int64 = 10_000
	numWorkers        int   = 100
)

func writeHTTPResponse(fd int, status int, body string) error {
	response := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Length: %d\r\n\r\n%s", status, http.StatusText(status), len(body), body)
	_, err := unix.Write(fd, []byte(response))
	return err
}

// parse HTTP request from provided data
func parseHTTPRequest(data []byte) (*http.Request, int, error) {
	reader := bufio.NewReader(bytes.NewReader(data))

	// parse HTTP request
	req, err := http.ReadRequest(reader)
	if err != nil {
		// don't have full request yet
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, 0, nil
		}

		return nil, 0, err
	}

	// determine how many bytes were consumed by subtracting unread bytes from total
	consumed := len(data) - reader.Buffered()

	return req, consumed, nil
}

// reads data from a client connection, then pushes it through provided channel
func readClientData(sock *socket.Socket, out chan<- types.Request, maxRequestSize int) {

	tempBuffer := bufferPool.Get().([]byte)
	defer bufferPool.Put(tempBuffer)

    n, err := sock.Read(tempBuffer)

	if err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return
		}
		fmt.Printf("Read error on fd %d: %v\n", sock.FD, err)
		closeClientConnection(sock)
		return
	}
	if n == 0 {
		closeClientConnection(sock)
		return
	}

	newData := tempBuffer[:n]

	mutex.Lock()
	buf, exists := connBuffers[sock.FD]
	if !exists {
		buf = &bytes.Buffer{}
		connBuffers[sock.FD] = buf
	}
	buf.Write(newData)

	if buf.Len() > maxRequestSize {
		mutex.Unlock()
		writeHTTPResponse(sock.FD, 413, "Request Entity Too Large")
		closeClientConnection(sock)
		return
	}

	// try to parse HTTP request
	for {
		req, consumed, err := parseHTTPRequest(buf.Bytes())
		if err != nil {
			mutex.Unlock()
			fmt.Printf("Error parsing HTTP request on fd %d: %v\n", sock.FD, err)
			writeHTTPResponse(sock.FD, 400, "Bad Request")
			closeClientConnection(sock)
			return
		}

		if req == nil || (req.ContentLength > 0 && int64(buf.Len()-consumed) < req.ContentLength) {
			break
		}

		// full request received, push it to channel
		requestData := make([]byte, consumed+int(req.ContentLength))
		copy(requestData, buf.Bytes()[:consumed+int(req.ContentLength)])

		select {
		case out <- types.Request{FD: sock.FD, Data: requestData}:
			buf.Next(consumed + int(req.ContentLength)) // remove only the processed bytes
		default:
			// channel is full
			mutex.Unlock()
			writeHTTPResponse(sock.FD, 503, "Server is too busy")
			closeClientConnection(sock)
			return
		}
	}

	mutex.Unlock()
}

func closeClientConnection(sock *socket.Socket) {

    sock.Close()
	mutex.Lock()
	delete(connBuffers, sock.FD)
	mutex.Unlock()

	atomic.AddInt64(&activeConnections, -1)
}

func registerClientConnection(sock *socket.Socket, kq int) error {
	if atomic.LoadInt64(&activeConnections) >= atomic.LoadInt64(&maxConnections) {
        sock.Close()
		return fmt.Errorf("connection limit reached")
	}

	atomic.AddInt64(&activeConnections, 1)

	sock.SetDefaultClientOptions()

	mutex.Lock()
	connBuffers[int(sock.FD)] = &bytes.Buffer{}
	mutex.Unlock()

	// register new connection for read events
	connEvent := unix.Kevent_t{
		Ident:  uint64(sock.FD),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD | unix.EV_CLEAR,
	}

    // get updates on k-event from kqueue
	if _, err := unix.Kevent(kq, []unix.Kevent_t{connEvent}, nil, nil); err != nil {
		fmt.Printf("Failed to register connection fd %d: %v\n", sock.FD, err)
		closeClientConnection(sock)
		return err
	}

	return nil
}

// main logic for server
func runKqueueServer(ctx context.Context, serverSocket *socket.Socket, config ServerConfig, processChan chan<- types.Request) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("failed to create kqueue: %v", err)
	}
	defer unix.Close(kq)

	// register listener event with kqueue
	listenEvent := unix.Kevent_t{
		Ident:  uint64(serverSocket.FD),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD,
	}
	events := make([]unix.Kevent_t, 1024)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{listenEvent}, nil, nil); err != nil {
		return fmt.Errorf("failed to register listener socket: %v", err)
	}

	// handle context cancellation
	go func() {
		<-ctx.Done()
		unix.Close(kq)
		fmt.Println("Context cancelled, shutting down server...")
		mutex.Lock()
		for fd := range connBuffers {
			unix.Close(fd)
			delete(connBuffers, fd)
		}
		mutex.Unlock()
	}()

    var timeout = &unix.Timespec{
			Sec:  1,
			Nsec: 0,
	    }
	// main event loop
	for {
		num_events, err := unix.Kevent(kq, nil, events, timeout) // wait for events on kqueue

		if err != nil {
			if err == unix.EINTR {
                // retry if read interrupted
				continue
			}
            if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("kevent error: %v", err)
		}
		for i := range num_events {
			ev := events[i]
			// check if the event is on the listener socket (a new connection)
			if int(ev.Ident) == serverSocket.FD {
				connFd, _, err := unix.Accept(serverSocket.FD)
				if err != nil {
					fmt.Printf("Accept error: %v\n", err)
					continue
				}
                newConnection := socket.Socket{FD: connFd, Mu: sync.RWMutex{}, IsClosed: false}
                sockets[connFd] = &newConnection
				registerClientConnection(&newConnection, kq)

			} else {
                sock := sockets[int(ev.Ident)]
                // check if event indicates an error or EOF condition
                if ev.Flags&unix.EV_EOF != 0 || ev.Flags&unix.EV_ERROR != 0 {
                    closeClientConnection(sock)
                    continue
                }

				// for existing connections, read and process incoming data
				readClientData(sock, processChan, config.MaxRequestSize)
			}
		}
	}
}

func processRequest(req types.Request, requestHandler types.Handler) {
	// Process with handler
	statusCode, responseBody, err := requestHandler.Handle(req)

	time.Sleep(1 * time.Millisecond)
	if err != nil {
		fmt.Printf("Error handling request: %v\n", err)
		writeHTTPResponse(req.FD, 500, "Internal Server Error")
	} else {
		writeHTTPResponse(req.FD, statusCode, responseBody)
	}
}

func startWorkerPool(dataChan <-chan types.Request, requestHandler types.Handler) {
	for i := range numWorkers {
		go func(workerID int) {
			for req := range dataChan { 
				// fmt.Printf("Worker %d handling request from fd %d\n", workerID, req.fd)
				processRequest(req, requestHandler)
			}
		}(i)
	}
}

func StartServer(config ServerConfig, requestHandler types.Handler) {
	atomic.StoreInt64(&maxConnections, int64(config.MaxConnections))
	dataChan := make(chan types.Request, maxConnections)

	startWorkerPool(dataChan, requestHandler)

	ctx, cancel := context.WithCancel(context.Background())

	// setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, unix.SIGTERM)

	// monitor for shutdown signals
	go func() {
		<-sigChan
		fmt.Printf("Shutdown signal received, closing server...\n")
		cancel()
		// give server a chance to shut down
		time.Sleep(2 * time.Second)

		// force exit
		fmt.Println("Forcing shutdown...")
		os.Exit(0)
	}()

	fmt.Println("setting up listener socket on port\n", config.ListenPort)
	serverSocket, err := socket.CreateServerSocket(config.ListenPort)
	if err != nil {
		fmt.Println("Error setting up listener socket:", err)
		os.Exit(1)
	}
	defer serverSocket.Close()
	fmt.Printf("Listening on port %d using kqueue-based event loop", config.ListenPort)

	if err := runKqueueServer(ctx, &serverSocket, config, dataChan); err != nil {
		fmt.Println("Error running kqueue server:", err)
		os.Exit(1)
	}

	fmt.Println("Server shutdown complete")
}
