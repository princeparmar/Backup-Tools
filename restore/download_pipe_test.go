package restore

import (
	"errors"
	"io"
	"testing"
	"time"
)

// Regression: restore may finish without reading content (file already exists / validate fail).
// If we await the download goroutine before closing the pipe reader, Write blocks forever
// and awaitStorxStream never returns — restore-all hangs after "Restore item started".
func TestPipeCloseBeforeAwait_PreventsDeadlock(t *testing.T) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		_, err := pw.Write([]byte("payload"))
		_ = pw.CloseWithError(err)
		errCh <- err
	}()

	// Simulate successful restore that never reads (owned file already in Drive).
	var restoreErr error

	done := make(chan struct{})
	var awaitErr error
	go func() {
		// BUG pattern (hangs): awaitStorxStream(errCh, restoreErr) before pr.Close()
		// FIX: close reader first so the writer unblocks.
		_ = pr.Close()
		awaitErr = awaitStorxStream(errCh, restoreErr)
		close(done)
	}()

	select {
	case <-done:
		if awaitErr != nil {
			t.Fatalf("successful skip restore should ignore closed-pipe download err, got %v", awaitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: awaitStorxStream blocked because pipe reader was not closed before wait")
	}
}

func TestPipeAwaitWithoutClose_WouldHang(t *testing.T) {
	// Documents the failure mode; must not run the hang in CI — use a short probe.
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		_, err := pw.Write([]byte("payload"))
		_ = pw.CloseWithError(err)
		errCh <- err
	}()

	hung := make(chan struct{})
	go func() {
		_ = awaitStorxStream(errCh, nil) // never closes pr
		close(hung)
	}()

	select {
	case <-hung:
		t.Fatal("expected hang without closing reader, but await returned")
	case <-time.After(200 * time.Millisecond):
		// expected: still blocked
		_ = pr.Close() // cleanup so the goroutine can exit
		<-hung
	}
}

func TestAwaitStorxStream_PrefersRestoreError(t *testing.T) {
	errCh := make(chan error, 1)
	errCh <- errors.New("download boom")
	got := awaitStorxStream(errCh, errors.New("restore boom"))
	if got == nil || got.Error() != "restore boom" {
		t.Fatalf("got %v, want restore boom", got)
	}
}

func TestAwaitStorxStream_IgnoresClosedPipeWhenRestoreOK(t *testing.T) {
	errCh := make(chan error, 1)
	errCh <- errors.New("read data: io: read/write on closed pipe")
	if err := awaitStorxStream(errCh, nil); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestAwaitStorxStream_KeepsRealDownloadError(t *testing.T) {
	errCh := make(chan error, 1)
	errCh <- errors.New("storx unavailable")
	got := awaitStorxStream(errCh, nil)
	if got == nil || got.Error() != "storx unavailable" {
		t.Fatalf("got %v, want storx unavailable", got)
	}
}
