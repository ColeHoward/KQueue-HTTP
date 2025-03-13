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
	"time"

	"github.com/ColeHoward/Inferno/internal/types"
	"golang.org/x/sys/unix"
)

var (
	connBuffers = make(map[int]*bytes.Buffer)
	mutex       sync.RWMutex 
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

func createListenerSocket(port int) (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("socket error: %v", err)
	}

	// allow socket reuse to avoid "address already in use" issues
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("setsockopt error: %v", err)
	}

	socket_address := &unix.SockaddrInet4{Port: port}
	copy(socket_address.Addr[:], net.ParseIP("0.0.0.0").To4())
	if err := unix.Bind(fd, socket_address); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("bind error: %v", err)
	}

	if err := unix.Listen(fd, 512); err != nil {
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
func readClientData(ev unix.Kevent_t, out chan<- types.Request, maxRequestSize int) {
	fd := int(ev.Ident)

	// check if event indicates an error or EOF condition
	if ev.Flags&unix.EV_EOF != 0 || ev.Flags&unix.EV_ERROR != 0 {
		closeClientConnection(fd)
		return
	}

	tempBuffer := bufferPool.Get().([]byte)
	defer bufferPool.Put(tempBuffer)

	n, err := unix.Read(fd, tempBuffer)
	if err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return
		}
		fmt.Printf("Read error on fd %d: %v\n", fd, err)
		closeClientConnection(fd)
		return
	}
	if n == 0 {
		closeClientConnection(fd)
		return
	}

	newData := tempBuffer[:n]

	mutex.Lock()
	buf, exists := connBuffers[fd]
	if !exists {
		buf = &bytes.Buffer{}
		connBuffers[fd] = buf
	}
	buf.Write(newData)

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

		if req == nil || (req.ContentLength > 0 && int64(buf.Len()-consumed) < req.ContentLength) {
			break
		}

		// full request received, push it to channel
		requestData := make([]byte, consumed+int(req.ContentLength))
		copy(requestData, buf.Bytes()[:consumed+int(req.ContentLength)])

		select {
		case out <- types.Request{FD: fd, Data: requestData}:
			buf.Next(consumed + int(req.ContentLength)) // remove only the processed bytes
		default:
			// channel is full
			mutex.Unlock()
			writeHTTPResponse(fd, 503, "Server is too busy")
			closeClientConnection(fd)
			return
		}
	}

	mutex.Unlock()
}

func closeClientConnection(fd int) {
	unix.Close(fd)

	mutex.Lock()
	delete(connBuffers, fd)
	mutex.Unlock()

	atomic.AddInt64(&activeConnections, -1)
}

func registerClientConnection(connFd int, kq int) error {
	if atomic.LoadInt64(&activeConnections) >= atomic.LoadInt64(&maxConnections) {
		unix.Close(connFd)
		return fmt.Errorf("connection limit reached")
	}

	atomic.AddInt64(&activeConnections, 1)

	if err := unix.SetNonblock(connFd, true); err != nil {
		closeClientConnection(connFd)
		return nil
	}

	if err := unix.SetsockoptInt(connFd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1); err != nil {
		fmt.Printf("failed to set TCP_NODELAY on fd %d: %v\n", connFd, err)
	}
	if err := unix.SetsockoptTimeval(connFd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5}); err != nil {
		fmt.Printf("failed to set SO_RCVTIMeO on fd %d: %v\n", connFd, err)
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
func runKqueueServer(ctx context.Context, listenerFd int, config ServerConfig, processChan chan<- types.Request) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("failed to create kqueue: %v", err)
	}
	defer unix.Close(kq)

	// register listener event with kqueue
	listenEvent := unix.Kevent_t{
		Ident:  uint64(listenerFd),
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

	// main event loop
	for {
		var timeout = &unix.Timespec{
			Sec:  1,
			Nsec: 0,
		}
		//
		n, err := unix.Kevent(kq, nil, events, timeout) // wait for events on kqueue
		if err != nil {
			if err == unix.EINTR {
                // retry if event read interrupted
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("kevent error: %v", err)
		}
		for i := range n {
			ev := events[i]
			// check if the event is on the listener socket (a new connection)
			if int(ev.Ident) == listenerFd {
				connFd, _, err := unix.Accept(listenerFd)
				if err != nil {
					fmt.Printf("Accept error: %v\n", err)
					continue
				}

				registerClientConnection(connFd, kq)

			} else {
				// for existing connections, read and process incoming data
				readClientData(ev, processChan, config.MaxRequestSize)
			}
		}
	}
}

func processRequest(req types.Request, requestHandler types.Handler) {
    defer closeClientConnection(req.FD)
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
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

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
	listenerFd, err := createListenerSocket(config.ListenPort)
	if err != nil {
		fmt.Println("Error setting up listener socket:", err)
		os.Exit(1)
	}
	defer unix.Close(listenerFd)
	fmt.Printf("Listening on port %d using kqueue-based event loop", config.ListenPort)

	if err := runKqueueServer(ctx, listenerFd, config, dataChan); err != nil {
		fmt.Println("Error running kqueue server:", err)
		os.Exit(1)
	}

	fmt.Println("Server shutdown complete")
}
