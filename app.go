package main

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xuri/excelize/v2"
)

type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// SelectFile opens a file picker dialog and returns the selected path.
func (a *App) SelectFile(title string, filters []runtime.FileFilter) string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   title,
		Filters: filters,
	})
	if err != nil {
		return ""
	}
	return path
}

// SegmentInfo describes one output segment.
type SegmentInfo struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Text string `json:"text"`
}

// ProcessResult holds the outcome of a run.
type ProcessResult struct {
	Success  bool          `json:"success"`
	Message  string        `json:"message"`
	Segments []SegmentInfo `json:"segments"`
}

// ReadFileAsBase64 reads a file from disk and returns it as a base64 string.
// Used by the frontend to load audio for in-app playback.
func (a *App) ReadFileAsBase64(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

// Run executes the segmentation logic and streams progress via events.
func (a *App) Run(excelPath, audioPath, targetCol, targetText string) (result ProcessResult) {
	// Catch unexpected panics (e.g. invalid slice bounds from bad timestamps)
	defer func() {
		if r := recover(); r != nil {
			result = ProcessResult{Success: false, Message: fmt.Sprintf("Unexpected error: %v", r)}
		}
	}()

	emit := func(msg string) {
		runtime.EventsEmit(a.ctx, "progress", msg)
	}

	// Validate inputs
	for _, p := range []string{excelPath, audioPath} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return ProcessResult{Success: false, Message: fmt.Sprintf("File not found: %s", p)}
		}
	}

	emit("Reading input file...")
	var (
		timestamps, texts []string
		err               error
	)
	switch strings.ToLower(filepath.Ext(excelPath)) {
	case ".csv":
		timestamps, texts, err = readCSV(excelPath, targetCol, targetText)
	default:
		timestamps, texts, err = readExcel(excelPath, targetCol, targetText)
	}
	if err != nil {
		return ProcessResult{Success: false, Message: err.Error()}
	}
	emit(fmt.Sprintf("Found %d rows.", len(texts)))

	// Convert M4A to a temporary WAV if needed
	wavPath := audioPath
	if strings.ToLower(filepath.Ext(audioPath)) == ".m4a" {
		emit("M4A detected — converting to WAV via ffmpeg...")
		tmp, err := convertM4AToWav(audioPath)
		if err != nil {
			return ProcessResult{Success: false, Message: err.Error()}
		}
		defer os.Remove(tmp)
		wavPath = tmp
		emit("Conversion done.")
	}

	emit("Loading audio file...")
	audioFile, err := os.Open(wavPath)
	if err != nil {
		return ProcessResult{Success: false, Message: fmt.Sprintf("Cannot open audio: %v", err)}
	}
	defer audioFile.Close()

	decoder := wav.NewDecoder(audioFile)
	if !decoder.IsValidFile() {
		return ProcessResult{Success: false, Message: "Invalid or unsupported audio file. Only WAV is supported."}
	}

	buf, err := decoder.FullPCMBuffer()
	if err != nil {
		return ProcessResult{Success: false, Message: fmt.Sprintf("Failed to decode audio: %v", err)}
	}

	sr := int(decoder.SampleRate)
	numChannels := buf.Format.NumChannels
	totalSamples := len(buf.Data) / numChannels
	emit(fmt.Sprintf("Audio loaded: %d Hz, %d channels, %.1f seconds.", sr, numChannels, float64(totalSamples)/float64(sr)))

	// Create output directory
	baseName := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	segDir := filepath.Join(filepath.Dir(audioPath), baseName+"_segments")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		return ProcessResult{Success: false, Message: fmt.Sprintf("Cannot create output folder: %v", err)}
	}

	// Write transcripts.txt
	tf, err := os.Create(filepath.Join(segDir, "transcripts.txt"))
	if err != nil {
		return ProcessResult{Success: false, Message: fmt.Sprintf("Cannot create transcripts.txt: %v", err)}
	}
	for i, text := range texts {
		fmt.Fprintf(tf, "%s_%d.wav\t%s\n", baseName, i, text)
	}
	tf.Close()

	// Write segments
	bitDepth := int(decoder.BitDepth)
	if bitDepth == 0 {
		bitDepth = 16
	}

	writeSegment := func(index, startSample, endSample int) error {
		if startSample < 0 {
			startSample = 0
		}
		if endSample > totalSamples {
			endSample = totalSamples
		}
		if startSample >= endSample {
			return fmt.Errorf("zero-length segment (start=%d >= end=%d) — duplicate or out-of-order timestamp?", startSample, endSample)
		}
		segData := buf.Data[startSample*numChannels : endSample*numChannels]
		segBuf := &audio.IntBuffer{Data: segData, Format: buf.Format}
		outPath := filepath.Join(segDir, fmt.Sprintf("%s_%d.wav", baseName, index))
		out, err := os.Create(outPath)
		if err != nil {
			return err
		}
		defer out.Close()
		enc := wav.NewEncoder(out, sr, bitDepth, numChannels, 1)
		if err := enc.Write(segBuf); err != nil {
			return err
		}
		return enc.Close()
	}

	emit("Segmenting audio...")
	var segments []SegmentInfo

	addSegment := func(index int) {
		name := fmt.Sprintf("%s_%d.wav", baseName, index)
		text := ""
		if index < len(texts) {
			text = texts[index]
		}
		segments = append(segments, SegmentInfo{
			Path: filepath.Join(segDir, name),
			Name: name,
			Text: text,
		})
	}

	skipped := 0
	for i := 0; i < len(timestamps)-1; i++ {
		start, err := parseTime(timestamps[i])
		if err != nil {
			emit(fmt.Sprintf("WARNING row %d: %v — skipped.", i+1, err))
			skipped++
			continue
		}
		end, err := parseTime(timestamps[i+1])
		if err != nil {
			emit(fmt.Sprintf("WARNING row %d: %v — skipped.", i+2, err))
			skipped++
			continue
		}
		if err := writeSegment(i, int(start*float64(sr)), int(end*float64(sr))); err != nil {
			emit(fmt.Sprintf("WARNING segment %d: %v — skipped.", i+1, err))
			skipped++
			continue
		}
		addSegment(i)
		emit(fmt.Sprintf("Segment %d / %d written.", i+1, len(timestamps)))
	}

	// Last segment: last timestamp to end of audio
	if len(timestamps) > 0 {
		last := len(timestamps) - 1
		start, err := parseTime(timestamps[last])
		if err != nil {
			emit(fmt.Sprintf("WARNING last row: %v — skipped.", err))
			skipped++
		} else if err := writeSegment(last, int(start*float64(sr)), totalSamples); err != nil {
			emit(fmt.Sprintf("WARNING last segment: %v — skipped.", err))
			skipped++
		} else {
			addSegment(last)
			emit(fmt.Sprintf("Segment %d / %d written.", last+1, len(timestamps)))
		}
	}

	written := len(segments)
	msg := fmt.Sprintf("Done! %d segments saved to:\n%s", written, segDir)
	if skipped > 0 {
		msg += fmt.Sprintf("\n(%d segment(s) skipped due to warnings above)", skipped)
	}
	return ProcessResult{
		Success:  written > 0,
		Message:  msg,
		Segments: segments,
	}
}

// ffmpegPath returns the path to the ffmpeg binary, checking both PATH and
// common install locations (needed because .app bundles don't inherit shell PATH).
func ffmpegPath() (string, error) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/opt/homebrew/bin/ffmpeg", // macOS Apple Silicon (Homebrew)
		"/usr/local/bin/ffmpeg",    // macOS Intel (Homebrew)
		"/usr/bin/ffmpeg",          // Linux
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf(
		"ffmpeg not found. Please install it:\n" +
			"  Windows: https://ffmpeg.org/download.html (add to PATH)\n" +
			"  Mac:     brew install ffmpeg")
}

// convertM4AToWav uses ffmpeg to decode an M4A file into a temporary WAV file.
// The caller is responsible for removing the returned temp path when done.
func convertM4AToWav(src string) (string, error) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "sae-audio-*.wav")
	if err != nil {
		return "", fmt.Errorf("cannot create temp file: %v", err)
	}
	tmp.Close()
	tmpPath := tmp.Name()

	out, err := exec.Command(ffmpeg, "-y", "-i", src, "-acodec", "pcm_s16le", tmpPath).CombinedOutput()
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg conversion failed: %v\n%s", err, string(out))
	}
	return tmpPath, nil
}

// readCSV parses a CSV file and returns the timestamp and text columns.
func readCSV(path, tsCol, textCol string) ([]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open CSV: %v", err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot parse CSV: %v", err)
	}
	if len(rows) < 2 {
		return nil, nil, fmt.Errorf("CSV file has no data rows")
	}

	tsIdx, textIdx := -1, -1
	for i, col := range rows[0] {
		if col == tsCol {
			tsIdx = i
		}
		if col == textCol {
			textIdx = i
		}
	}
	if tsIdx == -1 {
		return nil, nil, fmt.Errorf("column %q not found", tsCol)
	}
	if textIdx == -1 {
		return nil, nil, fmt.Errorf("column %q not found", textCol)
	}

	var timestamps, texts []string
	for _, row := range rows[1:] {
		ts := ""
		if tsIdx < len(row) {
			ts = strings.TrimSpace(row[tsIdx])
		}
		if ts == "" {
			continue
		}
		text := ""
		if textIdx < len(row) {
			text = row[textIdx]
		}
		timestamps = append(timestamps, ts)
		texts = append(texts, text)
	}
	return timestamps, texts, nil
}

// readExcel parses the first sheet and returns the timestamp and text columns.
func readExcel(path, tsCol, textCol string) ([]string, []string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open Excel: %v", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil, fmt.Errorf("no sheets found in Excel file")
	}

	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read rows: %v", err)
	}
	if len(rows) < 2 {
		return nil, nil, fmt.Errorf("Excel file has no data rows")
	}

	tsIdx, textIdx := -1, -1
	for i, col := range rows[0] {
		if col == tsCol {
			tsIdx = i
		}
		if col == textCol {
			textIdx = i
		}
	}
	if tsIdx == -1 {
		return nil, nil, fmt.Errorf("column %q not found", tsCol)
	}
	if textIdx == -1 {
		return nil, nil, fmt.Errorf("column %q not found", textCol)
	}

	var timestamps, texts []string
	for _, row := range rows[1:] {
		ts := ""
		if tsIdx < len(row) {
			ts = row[tsIdx]
		}
		text := ""
		if textIdx < len(row) {
			text = row[textIdx]
		}
		if ts == "" {
			continue
		}
		timestamps = append(timestamps, ts)
		texts = append(texts, text)
	}
	return timestamps, texts, nil
}

// parseTime converts "MM:SS" or "HH:MM:SS" to seconds.
func parseTime(s string) (float64, error) {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 2:
		m, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		sec, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		return float64(m)*60 + sec, nil
	case 3:
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		sec, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		return float64(h)*3600 + float64(m)*60 + sec, nil
	default:
		return 0, fmt.Errorf("invalid time format %q", s)
	}
}
