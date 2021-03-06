/*
A simple routing proxy in Go.  Accepts incoming connections on ports 80 and 443.

Connections on port 80 are assumed to be HTTP.  A hostname is extracted from each using
the HTTP "Host" header.
Connections on port 443 are assumed to be TLS.  A hostname is extracted from the 
server name indication in the ClientHello bytes.  Currently non-TLS SSL connections 
and TLS connections without SNIs are dropped messily.

Once a hostname has been extracted from the incoming connection, the proxy looks up
a set of backends on a redis server, which is assumed to be running on 127.0.0.1:6379.
The key for the set is hostnames:<the hostname from the connection>:backends.
If there is no set stored in redis for the backend, it will check 
hostnames:httpDefault:backends for HTTP connections, or hostnames:httpsDefault:backends
for HTTPS.  If these latter two lookups fail or return empty sets, it will drop 
the connection.

A backend is then selected at random from the list that was supplied by redis, and
the whole client connection is sent down to the appropriate port on that backend.  
The proxy will keep proxying data back and forth until one of the endpoints closes
the connection.
*/

package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
)


func copyAndClose(dst io.WriteCloser, src io.Reader) {
	io.Copy(dst, src)
	dst.Close()
}


func handleHTTPSConnection(downstream net.Conn) {
	firstByte := make([]byte, 1)
	_, error := downstream.Read(firstByte)
	if error != nil {
		fmt.Println("Couldn't read first byte :-(")
		return
	}
	if firstByte[0] != 0x16 {
		fmt.Println("Not TLS :-(")
	}

	versionBytes := make([]byte, 2)
	_, error = downstream.Read(versionBytes)
	if error != nil {
		fmt.Println("Couldn't read version bytes :-(")
		return
	}
	if versionBytes[0] < 3 || (versionBytes[0] == 3 && versionBytes[1] < 1) {
		fmt.Println("SSL < 3.1 so it's still not TLS")
		return
	}

	restLengthBytes := make([]byte, 2)
	_, error = downstream.Read(restLengthBytes)
	if error != nil {
		fmt.Println("Couldn't read restLength bytes :-(")
		return
	}

	restLength := (int(restLengthBytes[0]) << 8) + int(restLengthBytes[1])

	rest := make([]byte, restLength)
	_, error = downstream.Read(rest)
	if error != nil {
		fmt.Println("Couldn't read rest of bytes")
		return
	}

	current := 0

	handshakeType := rest[0]
	current += 1
	if handshakeType != 0x1 {
		fmt.Println("Not a ClientHello")
		return
	}


	// Skip over another length
	current += 3
	// Skip over protocolversion
	current += 2
	// Skip over random number
	current += 4 + 28
	// Skip over session ID
	sessionIDLength := int(rest[current])
	current += 1
	current += sessionIDLength

	cipherSuiteLength := (int(rest[current]) << 8) + int(rest[current+1])
			fmt.Println(rest)
	current += 2
	current += cipherSuiteLength

	compressionMethodLength := int(rest[current])
	current += 1
	current += compressionMethodLength

	if current > restLength {
		fmt.Println("no extensions")
		return
	}

	// Skip over extensionsLength
	// extensionsLength := (int(rest[current]) << 8) + int(rest[current + 1])
	current += 2

	hostname := ""
	for current < restLength && hostname == "" {
		extensionType := (int(rest[current]) << 8) + int(rest[current+1])
		current += 2

		extensionDataLength := (int(rest[current]) << 8) + int(rest[current+1])
		current += 2

		if extensionType == 0 {

			// Skip over number of names as we're assuming there's just one
			current += 2

			nameType := rest[current]
			current += 1
			if nameType != 0 {
				fmt.Println("Not a hostname")
				return
			}
			nameLen := (int(rest[current]) << 8) + int(rest[current+1])
			current += 2
			hostname = string(rest[current : current+nameLen])
		}

		current += extensionDataLength
	}
	if hostname == "" {
		fmt.Println("No hostname")
		return
	}
	


	upstream, error := net.Dial("tcp", hostname +":443")
	if error != nil {
		log.Fatal(error)
		return
	}

	upstream.Write(firstByte)
	upstream.Write(versionBytes)
	upstream.Write(restLengthBytes)
	upstream.Write(rest)

	go copyAndClose(upstream, downstream)
	go copyAndClose(downstream, upstream)
}

func reportDone(done chan int) {
	done <- 1
}

func doProxy(done chan int, port int, handle func(net.Conn)) {
	defer reportDone(done)

	listener, error := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(port))
	if error != nil {
		fmt.Println("Couldn't start listening", error)
		return
	}
	fmt.Println("Started proxy on", port, "-- listening...")
	for {
		connection, error := listener.Accept()
		if error != nil {
			fmt.Println("Accept error", error)
			return
		}

		go handle(connection)
	}
}

func main() {

	httpsDone := make(chan int)
	go doProxy(httpsDone, 443, handleHTTPSConnection)

	<-httpsDone
}
