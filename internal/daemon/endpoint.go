package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rceman/game-forge/internal/config"
)

// The daemon owns ONE durable loopback port so an MCP client configured
// against http://127.0.0.1:<port>/mcp stays valid across daemon restarts.
// The first successful bind picks an available port in the range below and
// persists it under durable state; later incarnations rebind exactly it and
// fail loudly rather than silently moving the endpoint.
const (
	portMin  = 50000
	portMax  = 59999
	portScan = 512 // bounded first-start probe count
)

// endpointFile is the durable, non-secret endpoint configuration.
// It never carries PIDs or tokens — those live in run/daemon.json.
type endpointFile struct {
	Port int `json:"port"`
}

// EndpointPath returns the durable endpoint config path.
func EndpointPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "endpoint.json"), nil
}

// LoadEndpointPort returns the persisted daemon port, or 0 when none has been
// chosen yet.
func LoadEndpointPort() (int, error) {
	path, err := EndpointPath()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var f endpointFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("parse endpoint config %s: %w", path, err)
	}
	return f.Port, nil
}

// persistEndpointPort atomically records the bound port.
func persistEndpointPort(port int) error {
	path, err := EndpointPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(endpointFile{Port: port})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "endpoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err == nil {
		_ = tmp.Chmod(0o600)
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ClearEndpoint drops the persisted port. It is the explicit rebind step —
// the next daemon start then selects and persists a fresh port. Never called
// automatically: a busy port is a configuration problem the user resolves.
func ClearEndpoint() error {
	path, err := EndpointPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// bindEndpoint returns the listener for the daemon's stable port. With a
// persisted port it binds exactly that port or fails — an occupied port must
// be resolved by the explicit `daemon rebind`, never silently moved. Without
// one it scans the port range from a randomized start so installs do not all
// compete for the same first port, then persists the winner.
func bindEndpoint() (net.Listener, int, error) {
	if port, err := LoadEndpointPort(); err != nil {
		return nil, 0, err
	} else if port != 0 {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, 0, fmt.Errorf("configured Game Forge port %d is occupied: %w\nrun: game-forge daemon rebind", port, err)
		}
		return ln, port, nil
	}
	start := randomOffset(portMax - portMin + 1)
	for i := 0; i < portScan; i++ {
		port := portMin + (start+i)%(portMax-portMin+1)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		// Persist only after the bind is established: a crash between bind
		// and persist must not leave a port recorded that nothing owns.
		if err := persistEndpointPort(port); err != nil {
			ln.Close()
			return nil, 0, fmt.Errorf("persist endpoint port: %w", err)
		}
		return ln, port, nil
	}
	return nil, 0, fmt.Errorf("no free port in %d-%d", portMin, portMax)
}

// randomOffset returns a random value in [0, n).
func randomOffset(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// MCPTokenPath returns the durable MCP credential path.
//
// The daemon control API uses an ephemeral per-incarnation bearer token; MCP
// clients are configured once, permanently, so /mcp requires a separate
// durable credential that survives daemon restarts. The two authentication
// domains are deliberately different.
func MCPTokenPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp.token"), nil
}

// LoadMCPToken returns the durable MCP credential without creating it.
func LoadMCPToken() (string, error) {
	path, err := MCPTokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// DurableMCPToken loads the durable MCP credential, creating it on first use
// with owner-only permissions.
func DurableMCPToken() (string, error) {
	path, err := MCPTokenPath()
	if err != nil {
		return "", err
	}
	if tok, err := LoadMCPToken(); err != nil {
		return "", err
	} else if tok != "" {
		return tok, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate MCP token: %w", err)
	}
	tok := hex.EncodeToString(b)
	// O_EXCL loses the create race to another process; the winner's token is
	// then re-read so both parties agree on the stored credential.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return LoadMCPToken()
		}
		return "", err
	}
	if _, err := f.WriteString(tok + "\n"); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return tok, nil
}

// localOrigin reports whether an HTTP Origin header refers to a loopback
// host. Non-browser clients send no Origin and pass; browser-issued requests
// from a remote or non-local origin are rejected so a hostile page cannot
// drive the local MCP endpoint through the user's browser.
func localOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasSuffix(host, ".localhost")
}
