package storage

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/aac"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/pion/rtp"
)

// Recorder is a core.Consumer that writes incoming packets to segmented
// files using a pluggable SegmentWriter (MP4, MPEG-TS, etc.).
type Recorder struct {
	core.Connection

	camera   string
	basePath string
	segDur   time.Duration
	format   string

	writer  SegmentWriter
	mu      sync.Mutex
	started bool
	closed  bool

	// current segment state
	file     *os.File
	segStart time.Time
	segBytes int64

	// metrics
	Segments int   `json:"segments"`
	Bytes    int64 `json:"bytes"`
}

// NewRecorder creates a recorder for a camera.
func NewRecorder(camera, basePath string, segDur time.Duration, format string) *Recorder {
	if segDur <= 0 {
		segDur = 5 * time.Minute
	}
	if format == "" {
		format = "mp4"
	}

	return &Recorder{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: format + "/record",
			Medias: []*core.Media{
				{
					Kind:      core.KindVideo,
					Direction: core.DirectionSendonly,
					Codecs: []*core.Codec{
						{Name: core.CodecH264},
						{Name: core.CodecH265},
					},
				},
				{
					Kind:      core.KindAudio,
					Direction: core.DirectionSendonly,
					Codecs: []*core.Codec{
						{Name: core.CodecAAC},
					},
				},
			},
		},
		camera:   camera,
		basePath: basePath,
		segDur:   segDur,
		format:   format,
		writer:   newWriter(format),
	}
}

func (r *Recorder) GetMedias() []*core.Media {
	return r.Medias
}

func (r *Recorder) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	codec := track.Codec.Clone()
	trackID := r.writer.AddTrack(codec)
	handler := core.NewSender(media, codec)

	switch track.Codec.Name {
	case core.CodecH264:
		handler.Handler = func(packet *rtp.Packet) {
			isKey := h264.IsKeyframe(packet.Payload)
			r.writePacket(trackID, packet, isKey)
		}
		if track.Codec.IsRTP() {
			handler.Handler = h264.RTPDepay(track.Codec, handler.Handler)
		} else {
			handler.Handler = h264.RepairAVCC(track.Codec, handler.Handler)
		}

	case core.CodecH265:
		handler.Handler = func(packet *rtp.Packet) {
			isKey := h265.IsKeyframe(packet.Payload)
			r.writePacket(trackID, packet, isKey)
		}
		if track.Codec.IsRTP() {
			handler.Handler = h265.RTPDepay(track.Codec, handler.Handler)
		} else {
			handler.Handler = h265.RepairAVCC(track.Codec, handler.Handler)
		}

	case core.CodecAAC:
		handler.Handler = func(packet *rtp.Packet) {
			r.writePacket(trackID, packet, false)
		}
		if track.Codec.IsRTP() {
			handler.Handler = aac.RTPDepay(handler.Handler)
		}

	default:
		return errors.New("storage: unsupported codec: " + track.Codec.Name)
	}

	handler.HandleRTP(track)
	r.Senders = append(r.Senders, handler)

	return nil
}

func (r *Recorder) writePacket(trackID byte, packet *rtp.Packet, isKeyframe bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	if isKeyframe {
		if !r.started {
			r.started = true
			r.rotateSegment()
		} else if time.Since(r.segStart) >= r.segDur {
			r.rotateSegment()
		}
	}

	if r.file == nil {
		return
	}

	n, err := r.writer.WritePacket(r.file, trackID, packet)
	if err != nil {
		log.Error().Err(err).Str("camera", r.camera).Msg("[storage] write")
		return
	}
	r.segBytes += int64(n)
	r.Bytes += int64(n)
}

func (r *Recorder) rotateSegment() {
	if r.file != nil {
		r.closeSegment()
	}

	now := time.Now()
	dir := segmentDir(r.basePath, r.camera, now)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("[storage] mkdir")
		return
	}

	ext := r.writer.Extension()
	path := segmentDir(r.basePath, r.camera, now) + "/" + now.Format("15-04-05") + ext
	f, err := os.Create(path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("[storage] create")
		return
	}

	if err := r.writer.WriteHeader(f); err != nil {
		log.Error().Err(err).Msg("[storage] write header")
		f.Close()
		os.Remove(path)
		return
	}

	r.file = f
	r.segStart = now
	r.segBytes = 0
	r.Segments++

	log.Debug().Str("camera", r.camera).Str("path", path).Msg("[storage] new segment")
}

func (r *Recorder) closeSegment() {
	if r.file == nil {
		return
	}
	if err := r.file.Close(); err != nil {
		log.Error().Err(err).Msg("[storage] close segment")
	}

	dur := time.Since(r.segStart).Seconds()
	log.Debug().
		Str("camera", r.camera).
		Float64("duration", dur).
		Int64("bytes", r.segBytes).
		Msg("[storage] segment complete")

	r.file = nil
}

func (r *Recorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.closed = true
	r.closeSegment()

	for _, sender := range r.Senders {
		sender.Close()
	}

	return nil
}
