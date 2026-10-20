package main

import (
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/filecopy"
)

func TestNewProgressToaster(t *testing.T) {
	t.Run("returns non-nil callback", func(t *testing.T) {
		toaster := newProgressToaster(func(string) {}, time.Now)
		if toaster == nil {
			t.Fatal("newProgressToaster returned nil")
		}
	})

	t.Run("does not toast when total is not positive", func(t *testing.T) {
		var toasts []string
		toaster := newProgressToaster(func(message string) {
			toasts = append(toasts, message)
		}, time.Now)

		toaster(filecopy.Progress{
			Item:        "/a/name.txt",
			Total:       0,
			Transferred: 50,
		})

		if len(toasts) != 0 {
			t.Fatalf("expected no toasts, got %v", toasts)
		}
	})

	t.Run("does not toast when transfer is complete", func(t *testing.T) {
		var toasts []string
		toaster := newProgressToaster(func(message string) {
			toasts = append(toasts, message)
		}, time.Now)

		toaster(filecopy.Progress{
			Item:        "/a/name.txt",
			Total:       100,
			Transferred: 100,
		})

		if len(toasts) != 0 {
			t.Fatalf("expected no toasts, got %v", toasts)
		}
	})

	t.Run("throttles toasts to one per second", func(t *testing.T) {
		var toasts []string
		current := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
		toaster := newProgressToaster(func(message string) {
			toasts = append(toasts, message)
		}, func() time.Time {
			return current
		})

		toaster(filecopy.Progress{Item: "/a/name.txt", Total: 100, Transferred: 10})
		current = current.Add(999 * time.Millisecond)
		toaster(filecopy.Progress{Item: "/a/name.txt", Total: 100, Transferred: 20})

		if len(toasts) != 1 {
			t.Fatalf("expected one toast before one second elapsed, got %v", toasts)
		}

		current = current.Add(time.Millisecond)
		toaster(filecopy.Progress{Item: "/a/name.txt", Total: 100, Transferred: 30})

		if len(toasts) != 2 {
			t.Fatalf("expected a second toast after one second elapsed, got %v", toasts)
		}
	})

	t.Run("formats progress toast", func(t *testing.T) {
		var toasts []string
		toaster := newProgressToaster(func(message string) {
			toasts = append(toasts, message)
		}, time.Now)

		toaster(filecopy.Progress{
			Item:        "/a/name.txt",
			Total:       100,
			Transferred: 50,
		})

		if len(toasts) != 1 {
			t.Fatalf("expected one toast, got %v", toasts)
		}
		if toasts[0] != "Copying name.txt: 50%" {
			t.Fatalf("unexpected toast: %q", toasts[0])
		}
	})
}
