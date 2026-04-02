# Subtitle Audio Extract

A desktop utility that slices an audio recording into individual segments using timestamps from a subtitle/transcript Excel file. Each segment corresponds to one subtitle line, making it easy to produce per-sentence audio clips for TTS training, language learning, or dubbing workflows.

## How segmentation works

The core idea is straightforward: every row in the Excel file carries a **relative timestamp** marking when that subtitle line begins. The tool treats each consecutive pair of timestamps as a `[start, end)` boundary and extracts the audio between them.

```
Excel rows                 Audio timeline
─────────────              ──────────────────────────────────────────────►
Row 1  0:00.000  "Hello"   ├── seg_0 ──┤
Row 2  0:02.450  "World"             ├─── seg_1 ────┤
Row 3  0:05.800  "Bye"                           ├──── seg_2 ─────────────
Row 4  0:09.100  ...
```

- **Segments 0 … N-2** span from `timestamp[i]` to `timestamp[i+1]`
- **Last segment** spans from the final timestamp to the end of the audio file — no audio is dropped

Timestamps are converted to sample offsets using the audio's sample rate:

```
sample_offset = seconds × sample_rate
```

Each slice of the raw PCM buffer is written out as an independent WAV file. No re-encoding takes place — the audio data is copied verbatim, so there is no quality loss.

### Handling edge cases

| Situation | Behaviour |
|---|---|
| Duplicate timestamps (zero-length segment) | Warning logged, segment skipped, processing continues |
| Out-of-order timestamps | Warning logged, segment skipped, processing continues |
| Final timestamp → end of file | Captured as the last segment automatically |
| M4A input | Converted to PCM WAV via ffmpeg before slicing |

## Output

For an input file `recording.wav` the tool creates a `recording_segments/` folder alongside it:

```
recording_segments/
  recording_0.wav
  recording_1.wav
  ...
  recording_N.wav
  transcripts.txt        ← tab-separated: filename <TAB> subtitle text
```

`transcripts.txt` format:
```
recording_0.wav    Hello
recording_1.wav    World
recording_2.wav    Bye
```

## Excel file format

The first sheet is used. Required columns (names are configurable in Advanced options):

| Column | Default name | Description |
|---|---|---|
| Timestamp | `Relative Timestamp` | `MM:SS` or `HH:MM:SS[.ms]` format |
| Text | `Text` | Subtitle/transcript text for that line |

## Supported audio formats

| Format | Support |
|---|---|
| WAV (PCM) | Native |
| M4A (AAC) | Via ffmpeg (must be installed) |

## Requirements

- **Mac**: no dependencies for WAV; `brew install ffmpeg` for M4A support
- **Windows**: no dependencies for WAV; install [ffmpeg](https://ffmpeg.org/download.html) and add it to `PATH` for M4A support

## Building from source

Prerequisites: [Go](https://go.dev), [Node.js](https://nodejs.org), [Wails CLI](https://wails.io)

```bash
# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Build for current platform
wails build

# Cross-compile for Windows from Mac/Linux (produces .exe)
GOOS=windows GOARCH=amd64 wails build -platform windows/amd64

# Build Windows NSIS installer (produces -installer.exe)
GOOS=windows GOARCH=amd64 wails build -platform windows/amd64 -nsis
```

### Build output — `build/bin/`

| Platform | Command flag | Output |
|---|---|---|
| macOS | _(default)_ | `subtitle-audio-extract.app` |
| Windows | `-platform windows/amd64` | `subtitle-audio-extract.exe` |
| Windows installer | `-platform windows/amd64 -nsis` | `subtitle-audio-extract-amd64-installer.exe` |

#### Windows installer details

The Windows installer is built with [NSIS](https://nsis.sourceforge.io) using the script at `build/windows/installer/project.nsi`. It:

- Installs the app to `Program Files\subtitle-audio-extract\`
- Creates a Start Menu shortcut and a Desktop shortcut
- Bundles the [WebView2 runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/) bootstrap (downloads it silently if not already present on the user's machine)
- Includes a standard uninstaller (accessible from Add/Remove Programs)

Distribute the `-installer.exe` file to Windows users — it is fully self-contained and requires no manual setup steps beyond running the installer.
