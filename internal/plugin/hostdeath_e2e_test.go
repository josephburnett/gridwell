package plugin_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// The host-death seam: a SIGKILLed host must not orphan its plugin
// subprocesses. go-plugin gives the guest no host-death detection in our
// configuration — the guest inherits the host's stdin and a dead host is just a
// disconnected gRPC client — so the guest helper runs its own watchdog on the pid
// the host hands over at spawn. This test fails without the watchdog: the
// plugin survives its host indefinitely.
//
// The test re-execs itself as an intermediate host, TestHelperPluginHost,
// gated by an env flag. That host spawns the real plugin binary through the
// production LoadPlugin, prints the child pid, and blocks. The test then
// SIGKILLs the host — the crashed-sidecar shape — and asserts the plugin exits
// within the watchdog bound.

// TestHelperPluginHost is not a test: it is the intermediate host body,
// entered only when the test re-execs itself with GRIDWELL_TEST_HOST=1.
func TestHelperPluginHost(t *testing.T) {
	if os.Getenv("GRIDWELL_TEST_HOST") != "1" {
		t.Skip("helper process body; run only via re-exec")
	}
	proc, err := compose.LoadPlugin(os.Getenv("GRIDWELL_TEST_PLUGIN_BIN"), map[string]string{
		"root": os.Getenv("GRIDWELL_TEST_PLUGIN_ROOT"),
		"uuid": "hostdeath",
		"kind": "fs",
	})
	if err != nil {
		fmt.Printf("HELPER-ERR %v\n", err)
		os.Exit(1)
	}
	defer proc.Kill()
	// Report the plugin child pid: the only child of this process.
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		fmt.Printf("HELPER-ERR pgrep: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("HELPER-CHILD %s\n", strings.TrimSpace(strings.Split(string(out), "\n")[0]))
	// Block forever — the test kills us hard.
	select {}
}

func TestPluginExitsWhenHostDiesHard(t *testing.T) {
	host := exec.Command(os.Args[0], "-test.run", "TestHelperPluginHost")
	host.Env = append(os.Environ(),
		"GRIDWELL_TEST_HOST=1",
		"GRIDWELL_TEST_PLUGIN_BIN="+plugintest.Binary(t, "fs"),
		"GRIDWELL_TEST_PLUGIN_ROOT="+t.TempDir(),
	)
	stdout, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	host.Stderr = os.Stderr
	if err := host.Start(); err != nil {
		t.Fatalf("start helper host: %v", err)
	}
	defer func() { _ = host.Process.Kill(); _, _ = host.Process.Wait() }()

	// Read the plugin child pid the host reports.
	var pluginPID int
	sc := bufio.NewScanner(stdout)
	deadline := time.After(30 * time.Second)
	got := make(chan int, 1)
	go func() {
		for sc.Scan() {
			line := sc.Text()
			if pid, ok := strings.CutPrefix(line, "HELPER-CHILD "); ok {
				n, _ := strconv.Atoi(strings.TrimSpace(pid))
				got <- n
				return
			}
			if strings.HasPrefix(line, "HELPER-ERR") {
				t.Log(line)
				got <- 0
				return
			}
		}
		got <- 0
	}()
	select {
	case pluginPID = <-got:
	case <-deadline:
		t.Fatal("helper host never reported its plugin child")
	}
	if pluginPID <= 0 {
		t.Fatal("helper host failed to spawn the plugin")
	}
	if err := syscall.Kill(pluginPID, 0); err != nil {
		t.Fatalf("plugin pid %d not alive before the host dies: %v", pluginPID, err)
	}

	// The crash: SIGKILL the host, with no graceful path and no
	// Registry.Close.
	if err := host.Process.Kill(); err != nil {
		t.Fatalf("kill host: %v", err)
	}
	_, _ = host.Process.Wait()

	// The watchdog polls every 2s; give it a bounded margin.
	deadline2 := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline2) {
		if err := syscall.Kill(pluginPID, 0); err == syscall.ESRCH {
			return // reaped: the watchdog holds
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Clean up the orphan so a failing run does not itself leak.
	_ = syscall.Kill(pluginPID, syscall.SIGKILL)
	t.Fatalf("plugin pid %d survived its host's death — the orphan leak (issue #197)", pluginPID)
}
