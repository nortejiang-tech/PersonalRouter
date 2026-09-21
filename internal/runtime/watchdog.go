package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	watchdogNotifyLimit = 500 * time.Millisecond
	watchdogHealthLimit = 2 * time.Second
)

var errWatchdogUnavailable = errors.New("watchdog unavailable")

// Watchdog owns the optional systemd notification loop. It is a no-op when
// NOTIFY_SOCKET is absent, which keeps manual and non-Linux invocation intact.
type Watchdog struct {
	address *net.UnixAddr
	timeout time.Duration
	stop    context.CancelFunc
	done    chan struct{}
	sendMu  sync.Mutex
	once    sync.Once
}

// StartWatchdog sends READY=1 only after the supplied bounded health callback
// succeeds. A positive WATCHDOG_USEC enables periodic health-gated heartbeats.
func StartWatchdog(ctx context.Context, health func(context.Context) error) (*Watchdog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	notifySocket := os.Getenv("NOTIFY_SOCKET")
	watchdogUsec, watchdogConfigured, err := watchdogDuration()
	if err != nil {
		return nil, errWatchdogUnavailable
	}
	watchdogPID, err := watchdogPID()
	if err != nil {
		return nil, errWatchdogUnavailable
	}
	address, err := notifyAddress(notifySocket)
	if err != nil {
		return nil, errWatchdogUnavailable
	}
	if address == nil {
		if watchdogConfigured {
			return nil, errWatchdogUnavailable
		}
		return &Watchdog{}, nil
	}
	if health == nil {
		return nil, errWatchdogUnavailable
	}
	healthLimit := watchdogHealthLimit
	if watchdogUsec > 0 && watchdogUsec/4 < healthLimit {
		healthLimit = watchdogUsec / 4
	}
	if healthLimit <= 0 {
		return nil, errWatchdogUnavailable
	}
	checkCtx, cancel := context.WithTimeout(ctx, healthLimit)
	healthErr := health(checkCtx)
	cancel()
	if healthErr != nil {
		return nil, errWatchdogUnavailable
	}
	watchdog := &Watchdog{address: address}
	if err := watchdog.send("READY=1\n"); err != nil {
		return nil, errWatchdogUnavailable
	}
	if watchdogUsec == 0 || watchdogPID != 0 && watchdogPID != os.Getpid() {
		return watchdog, nil
	}
	watchdog.timeout = watchdogUsec
	loopCtx, stop := context.WithCancel(ctx)
	watchdog.stop = stop
	watchdog.done = make(chan struct{})
	go watchdog.loop(loopCtx, health, healthLimit)
	return watchdog, nil
}

// Stop cancels heartbeats and optionally sends the systemd STOPPING message.
func (w *Watchdog) Stop(notifyStopping bool) {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if w.stop != nil {
			w.stop()
			select {
			case <-w.done:
			case <-time.After(watchdogNotifyLimit):
			}
		}
		if notifyStopping && w.address != nil {
			_ = w.send("STOPPING=1\n")
		}
	})
}

func (w *Watchdog) loop(ctx context.Context, health func(context.Context) error, healthLimit time.Duration) {
	defer close(w.done)
	tick := w.timeout / 3
	if tick <= 0 {
		tick = time.Nanosecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			checkCtx, cancel := context.WithTimeout(ctx, healthLimit)
			err := health(checkCtx)
			cancel()
			if err == nil && ctx.Err() == nil {
				_ = w.send("WATCHDOG=1\n")
			}
		}
	}
}

func (w *Watchdog) send(message string) error {
	if w == nil || w.address == nil || len(message) > 500 {
		return errWatchdogUnavailable
	}
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	conn, err := net.DialUnix("unixgram", nil, w.address)
	if err != nil {
		return errWatchdogUnavailable
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(watchdogNotifyLimit))
	if _, err := conn.Write([]byte(message)); err != nil {
		return errWatchdogUnavailable
	}
	return nil
}

func watchdogDuration() (time.Duration, bool, error) {
	raw, ok := os.LookupEnv("WATCHDOG_USEC")
	if !ok || raw == "" {
		return 0, false, nil
	}
	usec, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false, errWatchdogUnavailable
	}
	if usec == 0 {
		return 0, false, nil
	}
	const maxMicroseconds = uint64(1<<63-1) / uint64(time.Microsecond)
	if usec > maxMicroseconds {
		return 0, false, errWatchdogUnavailable
	}
	return time.Duration(usec) * time.Microsecond, true, nil
}

func watchdogPID() (int, error) {
	raw, ok := os.LookupEnv("WATCHDOG_PID")
	if !ok || raw == "" {
		return 0, nil
	}
	pid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || pid <= 0 || pid > int64(^uint(0)>>1) {
		return 0, errWatchdogUnavailable
	}
	return int(pid), nil
}

func notifyAddress(raw string) (*net.UnixAddr, error) {
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "/") {
		return &net.UnixAddr{Name: raw, Net: "unixgram"}, nil
	}
	if strings.HasPrefix(raw, "@") && len(raw) > 1 {
		return &net.UnixAddr{Name: raw, Net: "unixgram"}, nil
	}
	return nil, errWatchdogUnavailable
}
