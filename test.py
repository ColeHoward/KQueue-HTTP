import socket
import time

s = socket.create_connection(('127.0.0.1', 8080))

# Break the request into multiple parts
part1 = b"POST / HTTP/1.1\r\n"
part2 = b"Content-Length: 13\r\n"
part3 = b"\r\n"
part4 = b"Hello, world!"

# Send each part with a delay
print("Sending part 1:", part1)
s.sendall(part1)
time.sleep(1)

print("Sending part 2:", part2)
s.sendall(part2)
time.sleep(1) 

print("Sending part 3:", part3)
s.sendall(part3)
time.sleep(1)

print("Sending part 4:", part4)
s.sendall(part4)

# Get the response
print("\nResponse:", s.recv(1024).decode())
s.close()
