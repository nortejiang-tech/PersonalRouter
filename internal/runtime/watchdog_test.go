package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func notifyReceiver(t *testing.T, abstract bool) (string, *net.UnixConn) {
	t.Helper()
	name := "/tmp/nrwd-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if abstract {
		name = "@personalrouter-watchdog-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if !abstract {
		t.Cleanup(func() { _ = os.Remove(name) })
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return name, conn
}

func receiveNotification(t *testing.T, conn *net.UnixConn, timeout time.Duration) (string, error) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buffer := make([]byte, 512)
	n, _, err := conn.ReadFromUnix(buffer)
	if err != nil {
		return "", err
	}
	return string(buffer[:n]), nil
}

func drainNotifications(t *testing.T, conn *net.UnixConn, timeout time.Duration) []string {
	t.Helper()
	items := make([]string, 0)
	for {
		message, err := receiveNotification(t, conn, timeout)
		if err != nil {
			return items
		}
		items = append(items, message)
	}
}

func waitForStopping(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		message, err := receiveNotification(t, conn, time.Until(deadline))
		if err != nil {
			t.Fatalf("stopping notification missing: %v", err)
		}
		if message == "STOPPING=1\n" {
			return
		}
	}
	t.Fatal("stopping notification missing")
}

func assertNoHeartbeat(t *testing.T, conn *net.UnixConn, wait time.Duration) {
	t.Helper()
	for _, message := range drainNotifications(t, conn, wait) {
		if message == "WATCHDOG=1\n" {
			t.Fatal("heartbeat continued after watchdog stop or unhealthy state")
		}
	}
}

func TestWatchdogAbsentEnvironmentIsNoOp(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	t.Setenv("WATCHDOG_USEC", "")
	t.Setenv("WATCHDOG_PID", "")
	called := false
	watchdog, err := StartWatchdog(context.Background(), func(context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	watchdog.Stop(true)
	if called {
		t.Fatal("no-op watchdog called health callback")
	}
}

func TestWatchdogReadyAndRecurringHeartbeat(t *testing.T) {
	socket, receiver := notifyReceiver(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "60000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()))
	var calls atomic.Int32
	watchdog, err := StartWatchdog(context.Background(), func(context.Context) error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if message, err := receiveNotification(t, receiver, time.Second); err != nil || message != "READY=1\n" {
		t.Fatalf("ready notification = %q, %v", message, err)
	}
	foundHeartbeat := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		message, err := receiveNotification(t, receiver, time.Until(deadline))
		if err != nil {
			break
		}
		if message == "WATCHDOG=1\n" {
			foundHeartbeat = true
			break
		}
	}
	if !foundHeartbeat || calls.Load() < 2 {
		t.Fatal("watchdog did not perform a recurring healthy check")
	}
	watchdog.Stop(true)
	waitForStopping(t, receiver)
	assertNoHeartbeat(t, receiver, 100*time.Millisecond)
}

func TestWatchdogUnhealthySkipsHeartbeatAndStopCleansUp(t *testing.T) {
	socket, receiver := notifyReceiver(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "60000")
	var healthy atomic.Bool
	var unhealthyChecks atomic.Int32
	healthy.Store(true)
	watchdog, err := StartWatchdog(context.Background(), func(context.Context) error {
		if healthy.Load() {
			return nil
		}
		unhealthyChecks.Add(1)
		return errors.New("fixture health failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiveNotification(t, receiver, time.Second); err != nil {
		t.Fatal(err)
	}
	healthy.Store(false)
	deadline := time.Now().Add(time.Second)
	for unhealthyChecks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if unhealthyChecks.Load() == 0 {
		t.Fatal("unhealthy callback was not observed")
	}
	_ = drainNotifications(t, receiver, 10*time.Millisecond)
	assertNoHeartbeat(t, receiver, 100*time.Millisecond)
	healthy.Store(true)
	foundRecoveryHeartbeat := false
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		message, err := receiveNotification(t, receiver, time.Until(deadline))
		if err != nil {
			break
		}
		if message == "WATCHDOG=1\n" {
			foundRecoveryHeartbeat = true
			break
		}
	}
	if !foundRecoveryHeartbeat {
		t.Fatal("watchdog did not resume heartbeat after health recovery")
	}
	watchdog.Stop(true)
	waitForStopping(t, receiver)
	assertNoHeartbeat(t, receiver, 100*time.Millisecond)
}

func TestWatchdogParentContextCancellationStopsLoop(t *testing.T) {
	socket, receiver := notifyReceiver(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "60000")
	t.Setenv("WATCHDOG_PID", "")
	parent, cancel := context.WithCancel(context.Background())
	watchdog, err := StartWatchdog(parent, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiveNotification(t, receiver, time.Second); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)
	assertNoHeartbeat(t, receiver, 20*time.Millisecond)
	watchdog.Stop(false)
}

func TestWatchdogHealthFailurePreventsReadyAndZeroWatchdogOnlyReady(t *testing.T) {
	socket, receiver := notifyReceiver(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "0")
	watchdog, err := StartWatchdog(context.Background(), func(context.Context) error { return errors.New("fixture failure") })
	if err == nil || watchdog != nil {
		t.Fatal("failed health callback claimed readiness")
	}
	if _, readErr := receiveNotification(t, receiver, 50*time.Millisecond); readErr == nil {
		t.Fatal("failed health callback emitted notification")
	}

	watchdog, err = StartWatchdog(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if message, readErr := receiveNotification(t, receiver, time.Second); readErr != nil || message != "READY=1\n" {
		t.Fatalf("zero watchdog ready = %q, %v", message, readErr)
	}
	if _, readErr := receiveNotification(t, receiver, 80*time.Millisecond); readErr == nil {
		t.Fatal("zero watchdog emitted heartbeat")
	}
	watchdog.Stop(true)
}

func TestWatchdogInvalidConfigurationAndPID(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "malformed usec", key: "WATCHDOG_USEC", value: "not-a-number"},
		{name: "overflow usec", key: "WATCHDOG_USEC", value: "18446744073709551615"},
		{name: "malformed pid", key: "WATCHDOG_PID", value: "not-a-pid"},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			t.Setenv("NOTIFY_SOCKET", "")
			t.Setenv("WATCHDOG_USEC", "")
			t.Setenv("WATCHDOG_PID", "")
			t.Setenv(fixture.key, fixture.value)
			watchdog, err := StartWatchdog(context.Background(), func(context.Context) error { return nil })
			if err == nil || watchdog != nil {
				t.Fatal("invalid watchdog configuration accepted")
			}
		})
	}
	t.Setenv("NOTIFY_SOCKET", "")
	t.Setenv("WATCHDOG_USEC", "1")
	t.Setenv("WATCHDOG_PID", "")
	if watchdog, err := StartWatchdog(context.Background(), func(context.Context) error { return nil }); err == nil || watchdog != nil {
		t.Fatal("watchdog without notify socket accepted")
	}
}

func TestWatchdogForeignPIDDisablesHeartbeats(t *testing.T) {
	socket, receiver := notifyReceiver(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "60000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()+1))
	watchdog, err := StartWatchdog(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if message, err := receiveNotification(t, receiver, time.Second); err != nil || !strings.Contains(message, "READY=1") {
		t.Fatalf("foreign PID ready = %q, %v", message, err)
	}
	if _, err := receiveNotification(t, receiver, 100*time.Millisecond); err == nil {
		t.Fatal("foreign PID emitted heartbeat")
	}
	watchdog.Stop(true)
}

func TestWatchdogAbstractAddressOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abstract UNIX sockets are Linux-specific")
	}
	socket, receiver := notifyReceiver(t, true)
	t.Setenv("NOTIFY_SOCKET", socket)
	t.Setenv("WATCHDOG_USEC", "0")
	t.Setenv("WATCHDOG_PID", "")
	watchdog, err := StartWatchdog(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if message, err := receiveNotification(t, receiver, time.Second); err != nil || message != "READY=1\n" {
		t.Fatalf("abstract ready = %q, %v", message, err)
	}
	watchdog.Stop(false)
}
