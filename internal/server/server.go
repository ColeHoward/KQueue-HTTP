package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	connBuffers = make(map[int]*bytes.Buffer)
	mutex       sync.RWMutex  // only lock for write operations
	bufferPool  = &sync.Pool{ //avoid allocating new buffers each request (less garbage collection)
		New: func() interface{} {
			return make([]byte, 8192)
		},
	}
)

type ClientRequest struct {
	fd   int
	data []byte
}

type ServerConfig struct {
	ListenPort        int
	BufferSize        int
	MaxConnections    int
	ReadTimeoutSec    int
	ConnectionTimeout int
	MaxRequestSize    int
}

// Tracking active connections
var (
	activeConnections int64
	maxConnections    int64 = 10000
)

// creates TCP socket, binds it to specified port, and starts listening
func createListenerSocket(port int) (int, error) {
	// Create a TCP socket.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("socket error: %v", err)
	}

	// Allow socket reuse to avoid "address already in use" issues
	// send packets immediately instead of queuing them
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("setsockopt error: %v", err)
	}

	// bind socket to localhost on specified port
	sa := &unix.SockaddrInet4{Port: port}
	copy(sa.Addr[:], net.ParseIP("127.0.0.1").To4())
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("bind error: %v", err)
	}

	// start listening with a backlog of 10 connections
	if err := unix.Listen(fd, 10); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("listen error: %v", err)
	}

	return fd, nil
}

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
func readClientData(ev unix.Kevent_t, out chan<- ClientRequest, maxRequestSize int) {
	fd := int(ev.Ident)

	// Check if the event indicates an error or EOF condition
	if ev.Flags&unix.EV_EOF != 0 || ev.Flags&unix.EV_ERROR != 0 {
		closeClientConnection(fd)
		return
	}

	// Get buffer from pool instead of allocating new one each time
	tempBuffer := bufferPool.Get().([]byte)
	defer bufferPool.Put(&tempBuffer)

	n, err := unix.Read(fd, tempBuffer)
	if err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			// no data to read
			return
		}
		fmt.Printf("Read error on fd %d: %v\n", fd, err)
		closeClientConnection(fd)
		return
	}
	if n == 0 {
		fmt.Printf("Connection closed, fd: %d\n", fd)
		closeClientConnection(fd)
		return
	}

	newData := tempBuffer[:n]
	fmt.Printf("Data received on fd %d: %s\n", fd, string(newData))

	// append new data to connection's buffer
	mutex.Lock()
	buf, exists := connBuffers[fd]
	if !exists {
		buf = &bytes.Buffer{}
		connBuffers[fd] = buf
	}
	buf.Write(newData)

	// check request size limits
	if buf.Len() > maxRequestSize {
		mutex.Unlock()
		writeHTTPResponse(fd, 413, "Request Entity Too Large")
		closeClientConnection(fd)
		return
	}

	// try to parse HTTP request
	for {
		req, consumed, err := parseHTTPRequest(buf.Bytes())
		if err != nil {
			mutex.Unlock()
			fmt.Printf("Error parsing HTTP request on fd %d: %v\n", fd, err)
			writeHTTPResponse(fd, 400, "Bad Request")
			closeClientConnection(fd)
			return
		}
		if req == nil {
			// incomplete request, wait for more data before parsing
			break
		}

		// ensure entire body has been received before pushing request
		if req.ContentLength > 0 && int64(buf.Len()-consumed) < req.ContentLength {
			// wait for remaining body data
			break
		}

		// full request received, push it to channel
		requestData := make([]byte, consumed+int(req.ContentLength))
		copy(requestData, buf.Bytes()[:consumed+int(req.ContentLength)])

		select {
		case out <- ClientRequest{fd: fd, data: requestData}:
			buf.Next(consumed + int(req.ContentLength)) // remove processed bytes
		default:
			// if channel is full, return 503 and close connection
			mutex.Unlock()
			writeHTTPResponse(fd, 503, "Server is too busy")
			closeClientConnection(fd)
			return
		}
	}

	mutex.Unlock()
}

// close connection and clean up buffers
func closeClientConnection(fd int) {
	unix.Close(fd)

	mutex.Lock()
	delete(connBuffers, fd)
	mutex.Unlock()

	atomic.AddInt64(&activeConnections, -1)
}

func registerClientConnection(connFd int, kq int) error {
	// Check if we're at connection limit
	if atomic.LoadInt64(&activeConnections) >= atomic.LoadInt64(&maxConnections) {
		unix.Close(connFd)
		return fmt.Errorf("connection limit reached")
	}

	atomic.AddInt64(&activeConnections, 1)

	// set new connection to non-blocking mode
	if err := unix.SetNonblock(connFd, true); err != nil {
		closeClientConnection(connFd)
		return nil
	}

	// Add TCP_NODELAY for lower latency
	if err := unix.SetsockoptInt(connFd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1); err != nil {
		fmt.Printf("Failed to set TCP_NODELAY on fd %d: %v\n", connFd, err)
	}

	mutex.Lock()
	connBuffers[connFd] = &bytes.Buffer{}
	mutex.Unlock()

	// register new connection for read events
	connEvent := unix.Kevent_t{
		Ident:  uint64(connFd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD | unix.EV_CLEAR,
	}

	if _, err := unix.Kevent(kq, []unix.Kevent_t{connEvent}, nil, nil); err != nil {
		fmt.Printf("Failed to register connection fd %d: %v\n", connFd, err)
		closeClientConnection(connFd)
		return err
	}

	return nil
}

// main logic for server
func runKqueueServer(ctx context.Context, listenerFd int, config ServerConfig, inferenceChan chan<- ClientRequest) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("failed to create kqueue: %v", err)
	}
	fmt.Println("kqueue created with descriptor:", kq)

	// register listener socket to get notifications for read events
	listenEvent := unix.Kevent_t{
		Ident:  uint64(listenerFd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD,
	}
	events := make([]unix.Kevent_t, 1024)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{listenEvent}, nil, nil); err != nil {
		return fmt.Errorf("failed to register listener socket: %v", err)
	}

	// main event loop
	for {
		select {
		case <-ctx.Done():
			// Clean shutdown path
			return nil
		default:
			n, err := unix.Kevent(kq, nil, events, nil)
			if err != nil {
				if ctx.Err() != nil {
					// intentional shutdown
					return nil
				}
				// actual error
				return fmt.Errorf("kevent error: %v", err)
			}
			for i := range n {
				ev := events[i]
				// check if the event is on the listener socket (i.e. a new connection)
				if int(ev.Ident) == listenerFd {
					// handle new connection
					connFd, _, err := unix.Accept(listenerFd)
					if err != nil {
						fmt.Printf("Accept error: %v\n", err)
						continue
					}

					registerClientConnection(connFd, kq)

				} else {
					// for existing connections, read and process incoming data
					readClientData(ev, inferenceChan, config.MaxRequestSize)
				}
			}
		}
	}
}

// dummy function to handle complete HTTP requests
func handleRequestData(input <-chan ClientRequest) {
	for req := range input {
		result := "result for: " + string(req.data)
		err := writeHTTPResponse(req.fd, 200, result)
		if err != nil {
			fmt.Printf("Write error on fd %d: %v\n", req.fd, err)
		}
	}
}

func StartServer(config ServerConfig, dataChan chan ClientRequest) {
	atomic.StoreInt64(&maxConnections, int64(config.MaxConnections))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("Shutdown signal received, closing server...")
		cancel()
	}()

	// create listener socket on port 8080
	listenerFd, err := createListenerSocket(config.ListenPort)
	if err != nil {
		fmt.Println("Error setting up listener socket:", err)
		os.Exit(1)
	}
	defer unix.Close(listenerFd)
	fmt.Println("Listening on port 8080 using kqueue-based event loop")

	// start request handler
	go handleRequestData(dataChan)

	// run kqueue-based event loop
	if err := runKqueueServer(ctx, listenerFd, config, dataChan); err != nil {
		fmt.Println("Error running kqueue server:", err)
		os.Exit(1)
	}
}
