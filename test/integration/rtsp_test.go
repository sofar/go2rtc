// Package integration provides tests against real RTSP cameras on the LAN.
//
// These tests are skipped unless camera URLs are provided via environment
// variables. See env.example for the expected format.
//
// Run with:
//
//	source test/integration/env.sh
//	go test ./test/integration/ -v -count=1
package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// camera holds a discovered test camera from the environment.
type camera struct {
	Name string
	URL  string
}

// cameras returns all configured cameras, skipping the test if none are set.
func cameras(t *testing.T) []camera {
	t.Helper()
	var cams []camera
	for i := 1; i <= 9; i++ {
		key := fmt.Sprintf("GO2RTC_CAM%d_URL", i)
		if url := os.Getenv(key); url != "" {
			name := fmt.Sprintf("cam%d", i)
			if n := os.Getenv(fmt.Sprintf("GO2RTC_CAM%d_NAME", i)); n != "" {
				name = n
			}
			cams = append(cams, camera{Name: name, URL: url})
		}
	}
	if len(cams) == 0 {
		t.Skip("no cameras configured — set GO2RTC_CAM1_URL etc. (see env.example)")
	}
	return cams
}

// --- Low-level RTSP client tests ---

// TestRTSP_Dial verifies TCP connectivity to each camera's RTSP port.
func TestRTSP_Dial(t *testing.T) {
	for _, cam := range cameras(t) {
		t.Run(cam.Name, func(t *testing.T) {
			client := rtsp.NewClient(cam.URL)
			err := client.Dial()
			require.NoError(t, err, "failed to dial %s", cam.URL)
			defer client.Stop()

			t.Logf("connected to %s — remote=%s protocol=%s",
				cam.Name, client.RemoteAddr, client.Protocol)
		})
	}
}

// TestRTSP_Describe connects and retrieves the SDP, validating that at
// least one media track (video or audio) is advertised.
func TestRTSP_Describe(t *testing.T) {
	for _, cam := range cameras(t) {
		t.Run(cam.Name, func(t *testing.T) {
			client := dialWithRetry(t, cam.URL)
			defer client.Stop()

			medias := client.Medias
			require.NotEmpty(t, medias, "no media tracks in SDP")

			for _, m := range medias {
				t.Logf("  %s %s codecs=%d", m.Kind, m.Direction, len(m.Codecs))
				for _, c := range m.Codecs {
					t.Logf("    %s clock=%d ch=%d pt=%d",
						c.Name, c.ClockRate, c.Channels, c.PayloadType)
				}
			}

			// At least one video track expected
			hasVideo := false
			for _, m := range medias {
				if m.Kind == core.KindVideo {
					hasVideo = true
					break
				}
			}
			assert.True(t, hasVideo, "expected at least one video track")
		})
	}
}

// TestRTSP_ReceivePackets sets up all tracks via GetTrack, runs Start
// in a goroutine (which calls Play + Handle), and verifies that RTP
// packets arrive within a reasonable timeout.
func TestRTSP_ReceivePackets(t *testing.T) {
	for _, cam := range cameras(t) {
		t.Run(cam.Name, func(t *testing.T) {
			client := dialWithRetry(t, cam.URL)
			defer client.Stop()

			require.NotEmpty(t, client.Medias)

			// Use GetTrack for each media/codec to register receivers
			for _, media := range client.Medias {
				if media.Direction == core.DirectionSendonly {
					continue
				}
				for _, codec := range media.Codecs {
					_, err := client.GetTrack(media, codec)
					if err != nil {
						t.Logf("skip track %s/%s: %v", media.Kind, codec.Name, err)
					}
				}
			}

			// Start runs Play + Handle (blocking read loop) in background
			go client.Start()

			// Let packets flow for 3 seconds, then check built-in counters
			time.Sleep(3 * time.Second)

			totalPackets := 0
			for _, recv := range client.Receivers {
				t.Logf("  %s: %d packets, %d bytes",
					recv.Codec.Name, recv.Packets, recv.Bytes)
				totalPackets += recv.Packets
			}
			require.Greater(t, totalPackets, 0, "no packets received from any track")
		})
	}
}

// --- go2rtc daemon integration tests ---

// testDaemon starts go2rtc as a subprocess on a random port, returns the
// API base URL and a cleanup function.
func testDaemon(t *testing.T, cams []camera) (apiBase string, cleanup func()) {
	t.Helper()

	// Find a free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Build stream config
	streamsCfg := make(map[string]string, len(cams))
	for _, cam := range cams {
		streamsCfg[cam.Name] = cam.URL
	}
	streamsJSON, _ := json.Marshal(streamsCfg)

	cfg := fmt.Sprintf(`{"api":{"listen":"127.0.0.1:%d"},"rtsp":{"listen":":0"},"streams":%s}`, port, streamsJSON)

	// Build binary if not already built
	binPath := t.TempDir() + "/go2rtc"
	gobin, err := os.Executable()
	_ = gobin
	cmd := commandBuild(t, binPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	// Start daemon
	daemon := commandRun(binPath, cfg)
	daemon.Stdout = os.Stdout
	daemon.Stderr = os.Stderr
	require.NoError(t, daemon.Start())

	apiBase = fmt.Sprintf("http://127.0.0.1:%d", port)

	// Wait for API to be ready
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(apiBase + "/api")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return apiBase, func() {
		daemon.Process.Kill()
		daemon.Wait()
	}
}

func commandBuild(t *testing.T, binPath string) *execCmd {
	t.Helper()
	return newExecCmd("go", "build", "-o", binPath, ".")
}

func commandRun(binPath, cfg string) *execCmd {
	return newExecCmd(binPath, "-c", cfg)
}

// TestDaemon_StreamsAPI starts go2rtc, registers camera streams, and
// verifies they appear in the streams API.
func TestDaemon_StreamsAPI(t *testing.T) {
	cams := cameras(t)
	apiBase, cleanup := testDaemon(t, cams)
	defer cleanup()

	resp, err := http.Get(apiBase + "/api/streams")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, "body: %s", body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))

	for _, cam := range cams {
		_, ok := result[cam.Name]
		assert.True(t, ok, "stream %q not found in API response", cam.Name)
		t.Logf("stream %s registered", cam.Name)
	}
}

// TestDaemon_Snapshot requests a JPEG snapshot from each camera via the
// go2rtc API and validates it's a valid JPEG.
func TestDaemon_Snapshot(t *testing.T) {
	cams := cameras(t)
	apiBase, cleanup := testDaemon(t, cams)
	defer cleanup()

	for _, cam := range cams {
		t.Run(cam.Name, func(t *testing.T) {
			url := fmt.Sprintf("%s/api/frame.jpeg?src=%s", apiBase, cam.Name)
			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Get(url)
			require.NoError(t, err)
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != 200 {
				t.Skipf("snapshot not available (status %d): %s",
					resp.StatusCode, truncate(string(body), 200))
				return
			}

			require.True(t, len(body) > 100,
				"JPEG too small (%d bytes)", len(body))

			// JPEG magic bytes: FF D8 FF
			require.True(t, len(body) >= 3 &&
				body[0] == 0xFF && body[1] == 0xD8 && body[2] == 0xFF,
				"not a valid JPEG (first bytes: %x)", body[:min(8, len(body))])

			t.Logf("snapshot OK: %d bytes", len(body))
		})
	}
}

// TestDaemon_RTSPProxy starts go2rtc, then connects as an RTSP client
// to the go2rtc RTSP server to verify end-to-end proxy works.
func TestDaemon_RTSPProxy(t *testing.T) {
	cams := cameras(t)
	if len(cams) == 0 {
		return
	}
	cam := cams[len(cams)-1] // use last camera (typically most reliable)

	// Start daemon with known RTSP port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	apiPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rtspPort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	streamsCfg, _ := json.Marshal(map[string]string{cam.Name: cam.URL})
	cfg := fmt.Sprintf(`{"api":{"listen":"127.0.0.1:%d"},"rtsp":{"listen":"127.0.0.1:%d"},"streams":%s}`,
		apiPort, rtspPort, streamsCfg)

	binPath := t.TempDir() + "/go2rtc"
	out, err := newExecCmd("go", "build", "-o", binPath, ".").CombinedOutput()
	require.NoError(t, err, "build: %s", out)

	daemon := newExecCmd(binPath, "-c", cfg)
	daemon.Stdout = os.Stdout
	daemon.Stderr = os.Stderr
	require.NoError(t, daemon.Start())
	defer func() { daemon.Process.Kill(); daemon.Wait() }()

	// Wait for API
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api", apiPort))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Connect as RTSP client to the proxy
	proxyURL := fmt.Sprintf("rtsp://127.0.0.1:%d/%s", rtspPort, cam.Name)
	client := rtsp.NewClient(proxyURL)
	require.NoError(t, client.Dial())
	defer client.Stop()

	require.NoError(t, client.Describe())
	require.NotEmpty(t, client.Medias, "proxy returned no media tracks")

	for _, m := range client.Medias {
		t.Logf("proxy track: %s %s", m.Kind, m.Direction)
	}

	// Use GetTrack for video
	for _, media := range client.Medias {
		if media.Kind == core.KindVideo && media.Direction != core.DirectionSendonly {
			for _, codec := range media.Codecs {
				_, err := client.GetTrack(media, codec)
				require.NoError(t, err)
				break
			}
			break
		}
	}

	// Start runs Play + Handle in background
	go client.Start()

	// Let packets flow, then check counters
	time.Sleep(3 * time.Second)

	totalPackets := 0
	for _, recv := range client.Receivers {
		t.Logf("  proxy %s: %d packets, %d bytes",
			recv.Codec.Name, recv.Packets, recv.Bytes)
		totalPackets += recv.Packets
	}
	assert.Greater(t, totalPackets, 0, "no packets received through RTSP proxy")
}

// --- helpers ---

// dialWithRetry connects to an RTSP camera with retries. Some cheap cameras
// send stale RTP data from prior sessions on new TCP connections, causing
// DESCRIBE to fail with garbled responses.
func dialWithRetry(t *testing.T, url string) *rtsp.Conn {
	t.Helper()
	var client *rtsp.Conn
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		client = rtsp.NewClient(url)
		if err = client.Dial(); err != nil {
			t.Logf("attempt %d dial failed: %v", attempt, err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		if err = client.Describe(); err == nil {
			return client
		}
		client.Stop()
		t.Logf("attempt %d describe failed: %v, retrying...", attempt, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	require.NoError(t, err) // will fail with last error
	return client
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
