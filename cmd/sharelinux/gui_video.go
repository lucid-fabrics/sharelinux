//go:build linux

package main

import (
	"fmt"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
)

const (
	// videoSettle is how long a session waits before it asks for the video stream:
	// the Mac's display may still be changing size, and the stream is made for one size.
	videoSettle = 600 * time.Millisecond
	// videoSettleVirtual is the wait after a virtual display was asked for.
	videoSettleVirtual = 1500 * time.Millisecond
	// videoHealthy is how long pictures must flow before earlier trouble is forgotten.
	videoHealthy = 5 * time.Second
	// videoLogInterval is how often a running stream logs its counters.
	videoLogInterval = 5 * time.Second
)

// videoSession is a session's use of the video stream.
type videoSession struct {
	stream *videoStream
	plan   videoPlan
	// on is set while the Mac's pictures are what the widget shows.
	on bool
	// gaveUp is set when the video is not used for the rest of the session.
	gaveUp bool
	// started is set once a stream was asked for, so the first start happens once.
	started bool
	policy  restartPolicy
	retry   coreglib.SourceHandle
	// hold is set after the stream ended: the widget keeps its last picture and the
	// tiles wait for the Mac's next update.
	hold        bool
	resumeAfter uint64
	presented   uint64
	onSince     time.Time
	startedAt   time.Time
	askedAt     time.Time
	loggedAt    time.Time
	lastCounter screenVideo
}

// maybeStartVideo asks for the video stream when it is time: the picture has
// been up for a moment and a virtual display, if one is coming, has been asked for.
func (v *sessionView) maybeStartVideo() {
	vid := &v.vid
	if vid.started || vid.gaveUp || v.ended {
		return
	}
	now := time.Now()
	if vid.startedAt.IsZero() {
		vid.startedAt = now
	}
	wait := videoSettle
	if v.g.app.preferences().virtualFollowsWindow(&v.live.req.opts) {
		if !v.virtualAsked {
			return
		}
		if vid.askedAt.IsZero() {
			vid.askedAt = now
		}
		if now.Sub(vid.askedAt) < videoSettleVirtual {
			return
		}
	}
	if now.Sub(vid.startedAt) < wait {
		return
	}
	v.startVideo()
}

// startVideo plans the stream and asks the Mac for it.
func (v *sessionView) startVideo() {
	vid := &v.vid
	vid.started = true
	app := v.g.app
	if vid.policy.Limit == 0 {
		vid.policy = restartPolicy{Limit: 4, Base: time.Second}
	}
	info := v.client.Info()
	caps := app.videoCapabilities()
	plan := planVideo(app.videoMode(), caps, serverHasVideo(&info))
	if !plan.enabled() {
		app.logger.Info("video", "plan", "tiles", "reason", plan.Reason)
		vid.gaveUp = true
		return
	}
	vid.plan = plan
	client := v.client
	keyframe := func() {
		if stream := client.MediaStream(); stream != nil {
			stream.RequestKeyframe()
		}
	}
	renderer := v.screen.chooseDMABuf()
	app.logger.Info("video", "renderer", renderer.Name, "gl", renderer.GL, "accelerated", renderer.Accelerated, "dmabuf", v.screen.dmabuf)
	stream := newVideoStream(app.logger, plan, caps.Device, videoThreads(), v.screen, keyframe)
	vid.stream = stream
	vid.presented = 0
	vid.lastCounter = v.screen.videoCounters()
	app.logger.Info("video", "plan", "stream", "codec", codecName(plan.Codec), "decoders", decoderNames(plan.Kinds), "device", caps.Device, "driver", caps.Driver)
	go func() {
		err := client.StartMediaStream(plan.Offer, stream)
		if err != nil {
			stream.event(videoEvent{Kind: videoFailed, Text: err.Error()})
		}
	}()
}

func decoderNames(kinds []decoderKind) string {
	names := make([]string, len(kinds))
	for index, kind := range kinds {
		names[index] = kind.String()
	}
	return strings.Join(names, ",")
}

// pollVideo handles what the stream reported and keeps the counters moving. It runs on the main loop.
func (v *sessionView) pollVideo() {
	v.maybeStartVideo()
	vid := &v.vid
	stream := vid.stream
	if stream == nil {
		return
	}
	for {
		select {
		case event := <-stream.events:
			v.handleVideoEvent(stream, event)
			if vid.stream != stream {
				return
			}
			continue
		default:
		}
		break
	}
	now := time.Now()
	counters := v.screen.videoCounters()
	if counters.Presented != vid.presented {
		vid.presented = counters.Presented
		v.live.echo.Updated(now)
	}
	if vid.on && !vid.onSince.IsZero() && now.Sub(vid.onSince) >= videoHealthy {
		vid.policy.healthy()
	}
	if vid.on && now.Sub(vid.loggedAt) >= videoLogInterval {
		vid.loggedAt = now
		stats := v.videoStatistics()
		v.g.app.logger.Info("video stats", "decoder", videoLabel(stats.Codec, stats.Kind), "output", stats.Output.String(),
			"units", stats.Units, "frames", stats.Frames, "presented", stats.Presented, "dropped", stats.Dropped,
			"decode_ms", float64(stats.DecodeAvg.Microseconds())/1000, "decode_max_ms", float64(stats.DecodeMax.Microseconds())/1000,
			"age_ms", float64(stats.AgeAvg.Microseconds())/1000, "size", fmt.Sprintf("%dx%d", stats.Width, stats.Height))
	}
}

func (v *sessionView) handleVideoEvent(stream *videoStream, event videoEvent) {
	vid := &v.vid
	logger := v.g.app.logger
	switch event.Kind {
	case videoFirstPicture:
		vid.on = true
		vid.hold = false
		vid.onSince = time.Now()
		v.screen.setVideo(true)
		stats := stream.stats()
		logger.Info("video", "state", "showing", "decoder", videoLabel(stats.Codec, stats.Kind), "output", stats.Output.String())
	case videoDecoderChanged:
		logger.Info("video", "state", "decoder changed", "reason", event.Text)
	case videoFailed:
		logger.Warn("video", "state", "failed", "reason", event.Text)
		v.giveUpVideo("Video is not available here: " + event.Text)
	case videoStopped:
		kind := classifyStop(event.Err)
		logger.Info("video", "state", "stopped", "kind", int(kind), "error", event.Err)
		v.leaveVideo()
		delay, restart := vid.policy.next(kind)
		if !restart {
			v.closeVideo()
			if kind != stopRequested {
				vid.gaveUp = true
				v.notify("The video stream stopped, using the picture path")
			}
			return
		}
		v.closeVideo()
		vid.retry = coreglib.TimeoutAdd(uint(delay.Milliseconds()), func() bool {
			vid.retry = 0
			if v.ended || vid.gaveUp {
				return false
			}
			v.startVideo()
			return false
		})
	}
}

// leaveVideo takes the video off the screen and lets the tiles resume where the
// video stopped: the last picture stays until the Mac sends a new one.
func (v *sessionView) leaveVideo() {
	vid := &v.vid
	if !vid.on {
		return
	}
	vid.on = false
	vid.hold = true
	vid.resumeAfter = v.h.updateCount()
	v.screen.setVideo(false)
	v.h.invalidate()
	go func() { _ = v.client.RequestRefresh() }()
}

// closeVideo frees the stream's decoder and everything it holds.
func (v *sessionView) closeVideo() {
	vid := &v.vid
	if vid.stream != nil {
		vid.stream.close()
		vid.stream = nil
	}
}

// giveUpVideo returns to the tiles for the rest of the session and tells the Mac to stop the stream.
func (v *sessionView) giveUpVideo(message string) {
	vid := &v.vid
	vid.gaveUp = true
	if vid.retry != 0 {
		coreglib.SourceRemove(vid.retry)
		vid.retry = 0
	}
	wasOn := vid.on
	v.leaveVideo()
	v.closeVideo()
	client := v.client
	go func() { _ = client.StopMediaStream() }()
	if message != "" && (wasOn || vid.started) {
		v.notify(message + ", using the picture path")
	}
}

// stopVideo ends the session's video: no event is handled after it.
func (v *sessionView) stopVideo() {
	vid := &v.vid
	vid.gaveUp = true
	if vid.retry != 0 {
		coreglib.SourceRemove(vid.retry)
		vid.retry = 0
	}
	v.closeVideo()
	vid.on = false
}

// videoActive says the widget is showing the Mac's video.
func (v *sessionView) videoActive() bool {
	return v.vid.on
}

// videoWaiting says the tiles must not be drawn yet: the video just stopped and
// the Mac has not sent a picture since.
func (v *sessionView) videoWaiting() bool {
	vid := &v.vid
	if !vid.hold {
		return false
	}
	if v.h.updateCount() > vid.resumeAfter {
		vid.hold = false
		return false
	}
	return true
}

// videoStatistics merges the decoder's counters with what the widget drew.
func (v *sessionView) videoStatistics() videoStats {
	stream := v.vid.stream
	if stream == nil {
		return videoStats{}
	}
	stats := stream.stats()
	counters := v.screen.videoCounters()
	stats.Active = v.vid.on
	stats.Presented = counters.Presented
	stats.Dropped += counters.Replaced
	stats.AgeAvg, stats.AgeLast = counters.AgeAverage, counters.AgeLast
	return stats
}

// videoBytes counts the video the Mac has sent, which does not travel on the RFB connection.
func (v *sessionView) videoBytes() uint64 {
	if stream := v.client.MediaStream(); stream != nil {
		return stream.Stats().VideoBytes
	}
	return 0
}
