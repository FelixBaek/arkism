// windows-ssh-fixture serves only fresh loopback SSH fixtures. It never reads
// user keys/configuration, executes a shell, or listens on a public interface.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

func signer() ssh.Signer {
	_, k, e := ed25519.GenerateKey(rand.Reader)
	must(e)
	s, e := ssh.NewSignerFromKey(k)
	must(e)
	return s
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	if len(os.Args) != 2 {
		panic("owned fixture directory required")
	}
	root := os.Args[1]
	marker, e := os.ReadFile(filepath.Join(root, "TASK-OWNER"))
	must(e)
	if string(marker) != "arkism-ssh-fixture-v1" {
		panic("owner mismatch")
	}
	a, b := signer(), signer()
	// Generate a dedicated client key; do not use the user's agent or key files.
	_, private, e := ed25519.GenerateKey(rand.Reader)
	must(e)
	client, e := ssh.NewSignerFromKey(private)
	must(e)
	pemKey, e := ssh.MarshalPrivateKey(private, "inert fixture")
	must(e)
	must(os.WriteFile(filepath.Join(root, "client key"), pem.EncodeToMemory(pemKey), 0600))
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	must(e)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	path := strings.ReplaceAll(filepath.Join(root, "client key"), "\\", "/")
	config := fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n User fixture\n Port %d\n IdentityFile \"%s\"\n IdentitiesOnly yes\n IdentityAgent none\n", port, strings.ReplaceAll(path, "%", "%%"))
	must(os.WriteFile(filepath.Join(root, "config"), []byte(config), 0600))
	ready, e := json.Marshal(map[string]any{"port": port, "first": ssh.FingerprintSHA256(a.PublicKey()), "second": ssh.FingerprintSHA256(b.PublicKey())})
	must(e)
	must(os.WriteFile(filepath.Join(root, "ready.tmp"), ready, 0600))
	must(os.Rename(filepath.Join(root, "ready.tmp"), filepath.Join(root, "ready.json")))
	var lock sync.Mutex
	for {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			server := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				lock.Lock()
				f, e := os.OpenFile(filepath.Join(root, "auth.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if e == nil {
					_, _ = f.WriteString("public-key-offered\n")
					_ = f.Close()
				}
				lock.Unlock()
				if string(key.Marshal()) != string(client.PublicKey().Marshal()) {
					return nil, fmt.Errorf("unapproved fixture client")
				}
				return nil, nil
			}}
			if _, e := os.Stat(filepath.Join(root, "rotate")); e == nil {
				server.AddHostKey(b)
			} else {
				server.AddHostKey(a)
			}
			sshConn, channels, requests, e := ssh.NewServerConn(conn, server)
			if e != nil {
				return
			}
			defer sshConn.Close()
			go ssh.DiscardRequests(requests)
			for channel := range channels {
				if channel.ChannelType() != "session" {
					_ = channel.Reject(ssh.UnknownChannelType, "fixture supports sessions only")
					continue
				}
				ch, reqs, e := channel.Accept()
				if e != nil {
					continue
				}
				go func() {
					defer ch.Close()
					for req := range reqs {
						switch req.Type {
						case "exec":
							var command struct{ Command string }
							_ = ssh.Unmarshal(req.Payload, &command)
							if command.Command != "owned-fixture" {
								_ = req.Reply(false, nil)
								return
							}
							_ = req.Reply(true, nil)
							_, _ = io.WriteString(ch, "{\"fixture\":true}\n")
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						default:
							_ = req.Reply(false, nil)
						}
					}
				}()
			}
		}(conn)
	}
}
