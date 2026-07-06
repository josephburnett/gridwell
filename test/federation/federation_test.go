//go:build federation

// Package federation_test is the SPAWN GATE (`make check-federation`): it runs
// the real, separately-compiled binaries — `gridwell` (init + serve) and the
// go-plugin subprocesses including `gridwell-ssh` — through a real ssh tunnel
// and asserts one write/read crossing every hop. The in-process seam tests
// cannot see go-plugin spawn: the pluginmeta sqlite-driver bug (b648691) kept
// every test green while every production spawn failed. This gate closes that
// class (issue #58).
//
// Requires the binaries already built at the repo root (`make build` — the
// make target depends on it); guarded by the `federation` build tag so plain
// `go test ./...` (make check) stays fast.
package federation_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/plugin/sshdial/sshdialtest"
)

// repoRoot walks up from the test binary's source dir to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test dir")
		}
		dir = parent
	}
}

// startServe launches the real `gridwell serve` for a home and returns its
// origin once the banner announces the bound address.
func startServe(t *testing.T, bin, home, bind string) string {
	t.Helper()
	cmd := exec.Command(bin, "serve", "--bind", bind, "--static", "")
	cmd.Env = append(os.Environ(), "GRIDWELL_HOME="+home, "GRIDWELL_PLUGIN_DIR="+filepath.Dir(bin))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = cmd.Stderr // banner goes to stdout in some builds; merge
	stdout, err := cmd.StdoutPipe()
	if err == nil && stdout != nil {
		go io.Copy(io.Discard, stdout)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// The "serving on <addr>" banner is the readiness contract (the desktop
	// sidecar parses this exact line).
	sc := bufio.NewScanner(stderr)
	deadline := time.After(30 * time.Second)
	lines := make(chan string, 64)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("serve for %s exited before announcing", home)
			}
			if i := strings.Index(line, "serving on "); i >= 0 {
				addr := strings.Fields(line[i+len("serving on "):])[0]
				go func() { // keep draining so the child never blocks on stderr
					for range lines {
					}
				}()
				return "http://" + addr
			}
		case <-deadline:
			t.Fatalf("serve for %s never announced", home)
		}
	}
}

// rpc posts one Connect-JSON call and decodes the response.
func rpc(t *testing.T, origin, method string, req any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	hr, err := http.Post(origin+"/gridwell.v1.Gridwell/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer hr.Body.Close()
	data, _ := io.ReadAll(hr.Body)
	if hr.StatusCode != 200 {
		t.Fatalf("%s: HTTP %d: %s", method, hr.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: bad json %q: %v", method, data, err)
	}
	return out
}

func run(t *testing.T, env []string, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
}

func TestFederationSpawn(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// Remote node: two localdb plugins.
	remoteHome := t.TempDir()
	renv := []string{"GRIDWELL_HOME=" + remoteHome}
	run(t, renv, bin, "init", "--kind", "localdb", "--name", "personal")
	run(t, renv, bin, "init", "--kind", "localdb", "--name", "work")
	remoteOrigin := startServe(t, bin, remoteHome, "127.0.0.1:0")
	remoteAddr := strings.TrimPrefix(remoteOrigin, "http://")

	// A real ssh server fronting it (shared helper — the same sshd the seam
	// test uses, here with the PRODUCTION gridwell-ssh dialing it).
	creds := sshdialtest.Server(t, t.TempDir())

	// Local node: one localdb + the ssh mount.
	localHome := t.TempDir()
	lenv := []string{"GRIDWELL_HOME=" + localHome}
	run(t, lenv, bin, "init", "--kind", "localdb", "--name", "home")
	run(t, lenv, bin, "init", "--kind", "ssh", "--name", "rtb",
		"--config", "host="+creds.Addr, "--config", "user=joe",
		"--config", "key="+creds.KeyPath, "--config", "known_hosts="+creds.KnownHostsPath,
		"--config", "addr="+remoteAddr)
	localOrigin := startServe(t, bin, localHome, "127.0.0.1:0")

	// 1. The ssh plugin SPAWNED and mounted the whole node: its root is the
	//    remote's node grid, a chained id.
	lp := rpc(t, localOrigin, "ListPlugins", map[string]any{})
	var sshRoot, homeRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		switch pm["label"] {
		case "rtb":
			sshRoot, _ = pm["rootGridId"].(string)
		case "home":
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}
	if strings.Count(sshRoot, "/") != 2 {
		t.Fatalf("ssh mount root = %q, want a chained <ssh>/<rnode>/0 id — did gridwell-ssh spawn?", sshRoot)
	}

	// 2. The remote node grid lists both remote plugins through the tunnel.
	ng := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": sshRoot})
	tiles := ng["tiles"].([]any)
	if len(tiles) != 2 {
		t.Fatalf("remote node grid has %d tiles through the tunnel, want 2", len(tiles))
	}
	workChild := ""
	for _, ti := range tiles {
		tm := ti.(map[string]any)
		if tm["altText"] == "work" {
			workChild, _ = tm["childGridId"].(string)
		}
	}
	if workChild == "" {
		t.Fatal("no 'work' tile on the remote node grid")
	}

	// 3. Create a named well with content on the remote, through the chain.
	well := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": workChild,
		"tile":   map[string]any{"kind": "well", "x": 1, "y": 1, "w": 1, "h": 1, "altText": "remote grid"},
	})["tile"].(map[string]any)
	wellID := well["id"].(string)
	wellChild := well["childGridId"].(string)
	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": wellChild,
		"path":   map[string]any{"wellIds": []string{wellID}},
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
		"data":   base64.StdEncoding.EncodeToString([]byte("# across the spawn gate")),
	})["tile"].(map[string]any)

	// 4. Link the remote well into the LOCAL home grid (cross-plugin clone
	//    across the tunnel) and read the content back through the link.
	link := rpc(t, localOrigin, "CloneTile", map[string]any{
		"tileId": wellID, "version": 0, "destGridId": homeRoot, "x": 0, "y": 0,
	})["tile"].(map[string]any)
	if link["childGridId"] != wellChild {
		t.Fatalf("link child = %v, want the shared remote grid %s", link["childGridId"], wellChild)
	}
	if link["reference"] != true {
		t.Fatal("the link must arrive as a dashed reference")
	}
	body := rpc(t, localOrigin, "GetTileContent", map[string]any{"tileId": txt["id"]})
	got, _ := base64.StdEncoding.DecodeString(body["data"].(string))
	if string(got) != "# across the spawn gate" {
		t.Fatalf("content through the chain = %q", got)
	}
	fmt.Println("federation spawn gate: production binaries, real tunnel, chained write/read OK")
}
