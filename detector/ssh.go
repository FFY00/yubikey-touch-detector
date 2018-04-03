package detector

import (
	"net"
	"os"
	"path"
	"strings"

	log "github.com/sirupsen/logrus"
)

// WatchSSH watches for hints that YubiKey is maybe waiting for a touch on a SSH auth request
func WatchSSH(requestGPGCheck chan bool, exits map[string]chan bool) {
	socketFile := os.Getenv("SSH_AUTH_SOCK")
	if socketFile == "" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			log.Error("Cannot watch SSH, neither $SSH_AUTH_SOCK nor $XDG_RUNTIME_DIR are defined.")
			return
		}
		socketFile = path.Join(runtimeDir, "gnupg/S.gpg-agent.ssh")
	}

	if _, err := os.Stat(socketFile); err != nil {
		log.Errorf("Cannot watch SSH, the socket '%v' does not exist: %v\n", socketFile, err)
		return
	}

	originalSocketFile := socketFile + ".original"
	if _, err := os.Stat(originalSocketFile); err == nil {
		log.Errorf("Cannot watch SSH, '%v' already exists\n", originalSocketFile)
		return
	}

	if err := os.Rename(socketFile, originalSocketFile); err != nil {
		log.Error("Cannot move original SSH socket file to setup a proxy: ", err)
		return
	}

	proxySocket, err := net.Listen("unix", socketFile)
	if err != nil {
		log.Error("Cannot establish a proxy SSH socket: ", err)
		if err := os.Rename(originalSocketFile, socketFile); err != nil {
			log.Error("Cannot restore original SSH socket: ", err)
		}
		return
	}
	log.Debug("SSH watcher is successfully established")

	exit := make(chan bool)
	exits["detector/ssh"] = exit
	go func() {
		<-exit
		if err := proxySocket.Close(); err != nil {
			log.Error("Cannot cleanup proxy SSH socket: ", err)
		}
		if err := os.Rename(originalSocketFile, socketFile); err != nil {
			log.Error("Cannot restore original SSH socket: ", err)
		}
		exit <- true
	}()

	for {
		proxyConnection, err := proxySocket.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				log.Error("Cannot accept incoming proxy connection: ", err)
			}
			return
		}
		originalConnection, err := net.Dial("unix", originalSocketFile)
		if err != nil {
			log.Error("Cannot establish connection to original socket: ", err)
			proxyConnection.Close()
			return
		}

		go proxyUnixSocket(proxyConnection, originalConnection, requestGPGCheck)
		go proxyUnixSocket(originalConnection, proxyConnection, requestGPGCheck)
	}
}

func proxyUnixSocket(reader net.Conn, writer net.Conn, requestGPGCheck chan bool) {
	defer (func() {
		reader.Close()
		writer.Close()
	})()

	buf := make([]byte, 10240)
	for {
		nr, err := reader.Read(buf)
		if err != nil {
			return
		}

		data := buf[0:nr]
		if _, err = writer.Write(data); err != nil {
			return
		}

		select {
		case requestGPGCheck <- true:
		default:
		}
	}
}
