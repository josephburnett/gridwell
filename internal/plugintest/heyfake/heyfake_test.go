package heyfake

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func runHey(t *testing.T, c *CLI, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(c.Path(), args...)
	cmd.Env = append(os.Environ(), "HEY_NONINTERACTIVE=1")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func TestTheCLIAnswersWhatTheTestSet(t *testing.T) {
	c := New(t)
	c.SetBox("imbox", Thread{TopicID: 7, Subject: "Kites", Summary: "windy"})

	out, _, code := runHey(t, c, "box", "view", "imbox", "--json", "--all")
	if code != 0 || !strings.Contains(out, `"topic_id":7`) || !strings.Contains(out, `"name":"Kites"`) {
		t.Errorf("box view = %d %q", code, out)
	}
	if out, _, code := runHey(t, c, "box", "view", "laterbox", "--json", "--all"); code != 0 || !strings.Contains(out, `"postings":[]`) {
		t.Errorf("an empty box = %d %q", code, out)
	}
	if _, errOut, code := runHey(t, c, "box", "view", "nosuchbox", "--json", "--all"); code != ExitNotFound || !strings.Contains(errOut, `"ok":false`) {
		t.Errorf("an unknown box = %d %q", code, errOut)
	}
	if out, _, code := runHey(t, c, "thread", "read", "7", "--html"); code != 0 || !strings.Contains(out, "windy") {
		t.Errorf("thread read = %d %q", code, out)
	}
	if _, errOut, code := runHey(t, c, "thread", "read", "8", "--html"); code != ExitNotFound || !strings.HasPrefix(errOut, "Error: ") {
		t.Errorf("a missing thread = %d %q", code, errOut)
	}

	c.FailBox("imbox", ExitAuthRequired, "Not logged in")
	if _, errOut, code := runHey(t, c, "box", "view", "imbox", "--json", "--all"); code != ExitAuthRequired || !strings.Contains(errOut, "Not logged in") {
		t.Errorf("a failing box = %d %q", code, errOut)
	}
	c.HealBox("imbox")
	if _, _, code := runHey(t, c, "box", "view", "imbox", "--json", "--all"); code != 0 {
		t.Errorf("a healed box = %d", code)
	}
}

func TestTheCLIRefusesToPrompt(t *testing.T) {
	c := New(t)
	cmd := exec.Command(c.Path(), "box", "view", "imbox", "--json", "--all")
	cmd.Env = append(os.Environ(), "HEY_NONINTERACTIVE=")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != ExitUsage {
		t.Errorf("a run that could prompt = %v", err)
	}
}

func TestTheFeedPrintsWhatTheTestSendsUntilItEnds(t *testing.T) {
	c := New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path(), "watch", "--events", "added")
	cmd.Env = append(os.Environ(), "HEY_NONINTERACTIVE=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := c.AwaitFeed(t)
	f.Ready(t)
	sc := bufio.NewScanner(out)
	if !sc.Scan() || !strings.Contains(sc.Text(), `"change":"ready"`) {
		t.Fatalf("first line = %q", sc.Text())
	}
	f.End(ExitNetworkFailed)
	for sc.Scan() {
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != ExitNetworkFailed {
		t.Errorf("the ended feed = %v", err)
	}
}

func TestARemovedCLICannotRunUntilRestored(t *testing.T) {
	c := New(t)
	c.Remove(t)
	if _, err := exec.LookPath(c.Path()); err == nil {
		t.Fatal("the removed CLI is still there")
	}
	c.Restore(t)
	if _, _, code := runHey(t, c, "box", "view", "imbox", "--json", "--all"); code != 0 {
		t.Errorf("the restored CLI = %d", code)
	}
}
