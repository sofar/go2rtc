package storage

import (
	"os"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
	"github.com/AlexxIT/go2rtc/pkg/mpegts"
	"github.com/pion/rtp"
)

// SegmentWriter handles format-specific muxing to a file.
type SegmentWriter interface {
	// AddTrack registers a codec track, returns a track ID.
	AddTrack(codec *core.Codec) byte
	// WriteHeader writes the container header to the file.
	WriteHeader(f *os.File) error
	// WritePacket writes a muxed packet to the file.
	WritePacket(f *os.File, trackID byte, packet *rtp.Packet) (int, error)
	// Extension returns the file extension (e.g. ".mp4", ".ts").
	Extension() string
}

// --- MP4 writer ---

type mp4Writer struct {
	muxer     *mp4.Muxer
	initBytes []byte
	trackCount byte
}

func newMP4Writer() *mp4Writer {
	return &mp4Writer{muxer: &mp4.Muxer{}}
}

func (w *mp4Writer) AddTrack(codec *core.Codec) byte {
	id := w.trackCount
	w.muxer.AddTrack(codec)
	w.trackCount++
	return id
}

func (w *mp4Writer) WriteHeader(f *os.File) error {
	if w.initBytes == nil {
		init, err := w.muxer.GetInit()
		if err != nil {
			return err
		}
		w.initBytes = init
	}
	_, err := f.Write(w.initBytes)
	return err
}

func (w *mp4Writer) WritePacket(f *os.File, trackID byte, packet *rtp.Packet) (int, error) {
	b := w.muxer.GetPayload(trackID, packet)
	return f.Write(b)
}

func (w *mp4Writer) Extension() string { return ".mp4" }

// --- MPEG-TS writer ---

type tsWriter struct {
	muxer     *mpegts.Muxer
	pids      []uint16 // maps trackID to MPEG-TS PID
	clockRate []float64
	headerWritten bool
}

func newTSWriter() *tsWriter {
	return &tsWriter{muxer: mpegts.NewMuxer()}
}

func (w *tsWriter) AddTrack(codec *core.Codec) byte {
	id := byte(len(w.pids))

	var streamType byte
	switch codec.Name {
	case core.CodecH264:
		streamType = mpegts.StreamTypeH264
	case core.CodecH265:
		streamType = mpegts.StreamTypeH265
	case core.CodecAAC:
		streamType = mpegts.StreamTypeAAC
	default:
		return id
	}

	pid := w.muxer.AddTrack(streamType)
	w.pids = append(w.pids, pid)

	// Store clock rate for timestamp conversion (AAC needs 90kHz)
	rate := 1.0
	if codec.Name == core.CodecAAC && codec.ClockRate > 0 {
		rate = 90000.0 / float64(codec.ClockRate)
	}
	w.clockRate = append(w.clockRate, rate)

	return id
}

func (w *tsWriter) WriteHeader(f *os.File) error {
	if !w.headerWritten {
		b := w.muxer.GetHeader()
		if _, err := f.Write(b); err != nil {
			return err
		}
		w.headerWritten = true
	}
	return nil
}

func (w *tsWriter) WritePacket(f *os.File, trackID byte, packet *rtp.Packet) (int, error) {
	if int(trackID) >= len(w.pids) {
		return 0, nil
	}
	pid := w.pids[trackID]
	ts := uint32(float64(packet.Timestamp) * w.clockRate[trackID])
	b := w.muxer.GetPayload(pid, ts, packet.Payload)
	return f.Write(b)
}

func (w *tsWriter) Extension() string { return ".ts" }

// --- MKV writer (via ffmpeg) ---
// MKV requires ffmpeg since go2rtc has no native Matroska muxer.
// For now, record as MPEG-TS (which ffmpeg can remux to MKV later).
// A native MKV writer would need EBML encoding.

// newWriter creates a SegmentWriter for the given format.
func newWriter(format string) SegmentWriter {
	switch format {
	case "ts", "mpegts":
		return newTSWriter()
	case "mp4", "":
		return newMP4Writer()
	default:
		log.Warn().Str("format", format).Msg("[storage] unknown format, using mp4")
		return newMP4Writer()
	}
}
