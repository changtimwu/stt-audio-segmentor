import { useState, useEffect, useRef } from 'react';
import { SelectFile, Run, ReadFileAsBase64 } from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';
import './App.css';

function FilePicker({ label, value, onChange, filters }) {
  async function pick() {
    const path = await SelectFile(label, filters);
    if (path) onChange(path);
  }
  return (
    <div className="field">
      <label>{label}</label>
      <div className="file-row">
        <span className="file-path" title={value}>{value || 'No file selected'}</span>
        <button className="pick-btn" onClick={pick}>Browse...</button>
      </div>
    </div>
  );
}

function SegmentPlayer({ segments }) {
  const audioRef = useRef(new Audio());
  const [playingIdx, setPlayingIdx] = useState(null);
  const [loadingIdx, setLoadingIdx] = useState(null);
  const blobUrls = useRef({});

  useEffect(() => {
    const audio = audioRef.current;
    const onEnded = () => setPlayingIdx(null);
    audio.addEventListener('ended', onEnded);
    return () => {
      audio.removeEventListener('ended', onEnded);
      audio.pause();
      Object.values(blobUrls.current).forEach(URL.revokeObjectURL);
    };
  }, []);

  async function toggle(idx, path) {
    const audio = audioRef.current;
    if (playingIdx === idx) {
      audio.pause();
      setPlayingIdx(null);
      return;
    }
    audio.pause();
    setPlayingIdx(null);
    setLoadingIdx(idx);
    try {
      if (!blobUrls.current[path]) {
        const b64 = await ReadFileAsBase64(path);
        const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
        const blob = new Blob([bytes], { type: 'audio/wav' });
        blobUrls.current[path] = URL.createObjectURL(blob);
      }
      audio.src = blobUrls.current[path];
      await audio.play();
      setPlayingIdx(idx);
    } finally {
      setLoadingIdx(null);
    }
  }

  return (
    <div className="segment-panel">
      <div className="segment-header">Segments ({segments.length})</div>
      <div className="segment-list">
        {segments.map((seg, idx) => {
          const isPlaying = playingIdx === idx;
          const isLoading = loadingIdx === idx;
          return (
            <div key={idx} className={`segment-row ${isPlaying ? 'playing' : ''}`}>
              <button
                className="play-btn"
                onClick={() => toggle(idx, seg.path)}
                disabled={isLoading}
                title={isPlaying ? 'Stop' : 'Play'}
              >
                {isLoading ? '…' : isPlaying ? '■' : '▶'}
              </button>
              <span className="seg-num">{idx + 1}</span>
              <span className="seg-text" title={seg.text}>{seg.text || '—'}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function App() {
  const [excelPath, setExcelPath] = useState('');
  const [audioPath, setAudioPath] = useState('');
  const [targetCol, setTargetCol] = useState('Relative Timestamp');
  const [targetText, setTargetText] = useState('Text');
  const [log, setLog] = useState([]);
  const [running, setRunning] = useState(false);
  const [status, setStatus] = useState(null);
  const [segments, setSegments] = useState([]);
  const logRef = useRef(null);

  useEffect(() => {
    const unlisten = EventsOn('progress', (msg) => {
      setLog(prev => [...prev, msg]);
    });
    return () => unlisten();
  }, []);

  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight;
    }
  }, [log]);

  async function handleRun() {
    if (!excelPath || !audioPath) return;
    setLog([]);
    setStatus(null);
    setSegments([]);
    setRunning(true);
    try {
      const result = await Run(excelPath, audioPath, targetCol, targetText);
      setStatus(result.success ? 'ok' : 'err');
      setLog(prev => [...prev, result.message]);
      if (result.segments?.length) {
        setSegments(result.segments);
      }
    } catch (e) {
      setStatus('err');
      setLog(prev => [...prev, String(e)]);
    } finally {
      setRunning(false);
    }
  }

  const canRun = excelPath && audioPath && !running;

  return (
    <div id="App">
      <h1>STT Audio Segmentor</h1>

      <FilePicker
        label="Excel / CSV File"
        value={excelPath}
        onChange={setExcelPath}
        filters={[{ DisplayName: 'Spreadsheet Files', Pattern: '*.xlsx;*.xls;*.csv' }]}
      />

      <FilePicker
        label="Audio File (WAV / M4A)"
        value={audioPath}
        onChange={setAudioPath}
        filters={[{ DisplayName: 'Audio Files', Pattern: '*.wav;*.m4a' }]}
      />

      <div className="advanced">
        <details>
          <summary>Advanced options</summary>
          <div className="adv-fields">
            <div className="field">
              <label>Timestamp column</label>
              <input value={targetCol} onChange={e => setTargetCol(e.target.value)} />
            </div>
            <div className="field">
              <label>Text column</label>
              <input value={targetText} onChange={e => setTargetText(e.target.value)} />
            </div>
          </div>
        </details>
      </div>

      <button
        className={`run-btn ${!canRun ? 'disabled' : ''}`}
        onClick={handleRun}
        disabled={!canRun}
      >
        {running ? 'Processing...' : 'Run'}
      </button>

      {log.length > 0 && (
        <div className={`log-box ${status === 'ok' ? 'log-ok' : status === 'err' ? 'log-err' : ''}`} ref={logRef}>
          {log.map((line, i) => <div key={i}>{line}</div>)}
        </div>
      )}

      {segments.length > 0 && <SegmentPlayer segments={segments} />}
    </div>
  );
}

export default App;
