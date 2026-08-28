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
)

// The host-death seam (issue #197): a SIGKILLed host must not orphan its
// provider subprocesses. go-plugin gives the guest no host-death detection
// in our configuration (the guest inherits the host's stdin and a dead
// host is just a disconnected gRPC client), so guest.Serve runs
// its own watchdog on the pid the host hands over at spawn. This test
// FAILS WITHOUT the watchdog: the provider survives its host indefinitely.
//
// Topology: the test re-execs ITSELF as an intermediate host
// (TestHelperProviderHost, gated by an env flag) which spawns the real
// provider binary via the production LoadPlugin, prints the child pid,
// and blocks. The test then SIGKILLs the host — the exact harness-timeout
// / crashed-sidecar shape that produced the observed orphans — and asserts
// the provider exits within the watchdog bound.

// TestHelperProviderHost is not a test: it is the intermediate host body,
// entered only when the test re-execs itself with GRIDWELL_TEST_HOST=1.
func TestHelperProviderHost(t *testing.T) {
	if os.Getenv("GRIDWELL_TEST_HOST") != "1" {
		t.Skip("helper process body; run only via re-exec")
	}
	_, closer, err := compose.LoadPlugin(os.Getenv("GRIDWELL_TEST_PROVIDER_BIN"), map[string]string{
		"root": os.Getenv("GRIDWELL_TEST_PROVIDER_ROOT"),
		"uuid": "hostdeath",
		"kind": "fs",
	})
	if err != nil {
		fmt.Printf("HELPER-ERR %v\n", err)
		os.Exit(1)
	}
	defer closer()
	// Report the provider child pid: the only child of this process.
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		fmt.Printf("HELPER-ERR pgrep: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("HELPER-CHILD %s\n", strings.TrimSpace(strings.Split(string(out), "\n")[0]))
	// Block forever — the test kills us hard.
	select {}
}

func TestProviderExitsWhenHostDiesHard(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a provider binary; skipped under -short")
	}
	host := exec.Command(os.Args[0], "-test.run", "TestHelperProviderHost")
	host.Env = append(os.Environ(),
		"GRIDWELL_TEST_HOST=1",
		"GRIDWELL_TEST_PROVIDER_BIN="+buildProviderBinary(t, "fs"),
		"GRIDWELL_TEST_PROVIDER_ROOT="+t.TempDir(),
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

	// Read the provider child pid the host reports.
	var providerPID int
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
	case providerPID = <-got:
	case <-deadline:
		t.Fatal("helper host never reported its provider child")
	}
	if providerPID <= 0 {
		t.Fatal("helper host failed to spawn the provider")
	}
	if err := syscall.Kill(providerPID, 0); err != nil {
		t.Fatalf("provider pid %d not alive before the host dies: %v", providerPID, err)
	}

	// The crash: SIGKILL the host — no graceful path, no Registry.Close.
	if err := host.Process.Kill(); err != nil {
		t.Fatalf("kill host: %v", err)
	}
	_, _ = host.Process.Wait()

	// The watchdog polls every 2s; give it a bounded margin.
	deadline2 := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline2) {
		if err := syscall.Kill(providerPID, 0); err == syscall.ESRCH {
			return // reaped — the fix holds
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Clean up the orphan so a failing run doesn't itself leak.
	_ = syscall.Kill(providerPID, syscall.SIGKILL)
	t.Fatalf("provider pid %d survived its host's death — the orphan leak (issue #197)", providerPID)
}
