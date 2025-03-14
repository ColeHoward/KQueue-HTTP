package socket

import (
    "sync"
    "syscall"
    "fmt"
    "net"
)


type Socket struct {
    FD int
    IsClosed bool
    Mu sync.RWMutex
}


func (s *Socket) Read(buffer []byte) (int, error) {
    s.Mu.RLock()
    defer s.Mu.RUnlock()
    if s.IsClosed {
        return -1, fmt.Errorf("error: socket at fd %d is already closed", s.FD)
    }
    num_bytes_read, err := syscall.Read(s.FD, buffer)
    if err != nil {
        return -1, fmt.Errorf("error reading socket at fd %d: %v", s.FD, err)
    }
    return num_bytes_read, nil
}

func (s *Socket) Write(bytes []byte) error {
    s.Mu.Lock()
    defer s.Mu.Unlock()
    if len(bytes) == 0 {
        return nil
    }
     if s.IsClosed {
        return fmt.Errorf("error: socket at fd %d is already closed", s.FD)
    }

    _, err := syscall.Write(s.FD, bytes)
    if err != nil {
        return fmt.Errorf("error writing to socket at fd %d: %v", s.FD, err)
    }
    return nil
}

func (s *Socket) Close() error {
    s.Mu.Lock()
    defer s.Mu.Unlock()
     if s.IsClosed {
        return fmt.Errorf("error: socket at fd %d is already closed", s.FD)
    }

    if err := syscall.Close(s.FD); err != nil {
        return fmt.Errorf("error closing socket at %d: %v", s.FD, err)
    }

    s.IsClosed = true

    return nil
}

func (s *Socket) SetDefaultClientOptions() error {
    s.Mu.Lock()
    defer s.Mu.Unlock()
    if s.IsClosed {
        return fmt.Errorf("error: socket at fd %d is already closed", s.FD)
    }
    // Set non-blocking mode.
    if err := syscall.SetNonblock(s.FD, true); err != nil {
        return err
    }
    // Set TCP_NODELAY to send packets immediately upon reception
    if err := syscall.SetsockoptInt(s.FD, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1); err != nil {
        return err
    }

    return nil
}


func CreateServerSocket(port int) (Socket, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return Socket{}, fmt.Errorf("socket error: %v", err)
	}

	// allow socket reuse to avoid "address already in use" issues
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fd)
		return Socket{}, fmt.Errorf("setsockopt error: %v", err)
	}

	socket_address := &syscall.SockaddrInet4{Port: port}
	copy(socket_address.Addr[:], net.ParseIP("0.0.0.0").To4())
	if err := syscall.Bind(fd, socket_address); err != nil {
		syscall.Close(fd)
		return Socket{}, fmt.Errorf("bind error: %v", err)
	}

	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		syscall.Close(fd)
		return Socket{}, fmt.Errorf("listen error: %v", err)
	}

    return Socket{FD: fd, IsClosed: false, Mu: sync.RWMutex{}}, nil
}


