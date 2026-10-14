package call

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The latency harness's skeleton (B8); calibration against targets is
// Layer C (05-testing-calibration.md, "Voice calibration"). It times each
// addressed question from the moment its final segment reaches the room to
// the agent starting to speak, its first audio and its last. What it can't
// see yet: speech end in the browser to the final segment (RealtimeKit's
// transcription), and the client's playback. Recorded clips played into a
// real call, and network profiles, come with the call screen.

var questions = []string{
	"what is seven times eight?",
	"what day comes after Tuesday?",
	"name a primary colour.",
	"what is the capital of Japan?",
	"how many minutes are in an hour?",
}

type sample struct {
	Question   string  `json:"question"`
	SpeakingMs float64 `json:"speakingMs"`
	FirstAudio float64 `json:"firstAudioMs"`
	LastAudio  float64 `json:"lastAudioMs"`
	Frames     int     `json:"frames"`
	Answer     string  `json:"answer"`
}

type report struct {
	At      string             `json:"at"`
	Room    string             `json:"room"`
	Samples []sample           `json:"samples"`
	Summary map[string]float64 `json:"summary"`
}

func TestVoiceLatency(t *testing.T) {
	count, _ := strconv.Atoi(os.Getenv("CALL_LIVE_LATENCY_SAMPLES"))
	if count <= 0 {
		t.Skip("CALL_LIVE_LATENCY_SAMPLES is not set")
	}
	r := setup(t).newRoom(t)
	call := r.join(r.a)
	e := r.listen(r.a)
	e.expect(10*time.Second, "voice.state", isType("voice.state"))

	var samples []sample
	for i := range count {
		question := questions[i%len(questions)]
		start := time.Now()
		r.say(r.a, call.CallID, said(r.a, r.question(question), false))
		speaking, audio := e.utterance(answerWithin)
		samples = append(samples, sample{
			Question:   question,
			SpeakingMs: ms(speaking.at.Sub(start)),
			FirstAudio: ms(audio[0].at.Sub(start)),
			LastAudio:  ms(audio[len(audio)-1].at.Sub(start)),
			Frames:     len(audio),
			Answer:     speaking.Text,
		})
		// Past the follow-up window, so each question stands alone.
		time.Sleep(11 * time.Second)
	}

	firstAudio := make([]float64, 0, len(samples))
	speaking := make([]float64, 0, len(samples))
	for _, s := range samples {
		firstAudio = append(firstAudio, s.FirstAudio)
		speaking = append(speaking, s.SpeakingMs)
	}
	out := report{
		At:      time.Now().UTC().Format(time.RFC3339),
		Room:    string(r.room.Kind) + "/" + r.room.ID,
		Samples: samples,
		Summary: map[string]float64{
			"firstAudioP50": percentile(firstAudio, 50), "firstAudioP95": percentile(firstAudio, 95),
			"speakingP50": percentile(speaking, 50), "speakingP95": percentile(speaking, 95),
		},
	}
	t.Logf("final segment → first agent audio: p50 %.0f ms, p95 %.0f ms (%d samples)", out.Summary["firstAudioP50"], out.Summary["firstAudioP95"], len(samples))
	if path := os.Getenv("CALL_LIVE_REPORT"); path != "" {
		encoded, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Errorf("write the report: %v", err)
		}
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// percentile is the nearest-rank percentile of values.
func percentile(values []float64, p int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := (p*len(sorted) + 99) / 100
	return sorted[max(rank-1, 0)]
}
