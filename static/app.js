(() => {
  "use strict";

  // ---------- DOM ---------- //

  const messagesEl = document.getElementById("messages");
  const inputEl = document.getElementById("input");
  const sendBtn = document.getElementById("send");
  const micBtn = document.getElementById("mic");
  const ttsToggle = document.getElementById("tts");
  const voiceModeBtn = document.getElementById("voiceMode");
  const voiceSelect = document.getElementById("voiceSelect");
  const resetBtn = document.getElementById("reset");
  const statusEl = document.getElementById("status");
  const errorEl = document.getElementById("error");
  const composer = document.getElementById("composer");
  const orb = document.getElementById("orb");
  const orbIcon = orb.querySelector(".orb-icon");
  const stateLabel = document.getElementById("stateLabel");
  const stateHint = document.getElementById("stateHint");
  const interruptBtn = document.getElementById("interrupt");

  // ---------- State machine ---------- //

  const S = Object.freeze({
    IDLE: "idle",
    LISTENING: "listening",
    THINKING: "thinking",
    SPEAKING: "speaking",
  });

  const LABELS = {
    [S.IDLE]: "tap mic or type to start",
    [S.LISTENING]: "listening…",
    [S.THINKING]: "thinking…",
    [S.SPEAKING]: "speaking…",
  };
  const ICONS = {
    [S.IDLE]: "🎙️",
    [S.LISTENING]: "🎧",
    [S.THINKING]: "💭",
    [S.SPEAKING]: "🔊",
  };

  let state = S.IDLE;
  let voiceMode = false;
  let consecutiveEmptyTurns = 0;

  function setState(next, hint) {
    state = next;
    orb.dataset.state = next;
    orbIcon.textContent = ICONS[next];
    stateLabel.textContent = LABELS[next];
    stateHint.textContent = hint || "";
    interruptBtn.hidden = !(next === S.SPEAKING || next === S.THINKING);
  }

  // ---------- Session ---------- //

  const SESSION_KEY = "self_coding_agent_session_id";
  let sessionId = sessionStorage.getItem(SESSION_KEY) || "";

  // ---------- Messages ---------- //

  function setError(msg) {
    errorEl.textContent = msg || "";
  }

  function autoGrow() {
    inputEl.style.height = "auto";
    inputEl.style.height = Math.min(inputEl.scrollHeight, 200) + "px";
  }

  function addMessage(role, text) {
    const li = document.createElement("li");
    li.className = "msg " + role;
    li.textContent = text;
    messagesEl.appendChild(li);
    li.scrollIntoView({ behavior: "smooth", block: "end" });
    return li;
  }

  function addToolCall(tc) {
    const li = document.createElement("li");
    li.className = "msg tool";
    const head = document.createElement("div");
    head.className = "name";
    head.textContent = "🔧 " + tc.name + "(" + (tc.input || "") + ")";
    const body = document.createElement("div");
    body.className = "body";
    body.textContent = tc.error
      ? "error: " + tc.error
      : (tc.result || "").slice(0, 4000);
    li.appendChild(head);
    li.appendChild(body);
    messagesEl.appendChild(li);
    li.scrollIntoView({ behavior: "smooth", block: "end" });
  }

  // ---------- Speech synthesis (TTS) ---------- //

  const ttsAvailable = "speechSynthesis" in window;
  let voicesCache = [];
  let preferredVoiceURI =
    localStorage.getItem("self_coding_agent_voice") || "";

  function loadVoices() {
    if (!ttsAvailable) return;
    voicesCache = window.speechSynthesis.getVoices() || [];
    voiceSelect.innerHTML = "";
    if (voicesCache.length === 0) {
      const opt = document.createElement("option");
      opt.textContent = "(default)";
      voiceSelect.appendChild(opt);
      voiceSelect.disabled = true;
      return;
    }
    voiceSelect.disabled = false;
    const userLang = (navigator.language || "en").toLowerCase();
    const sorted = [...voicesCache].sort((a, b) => {
      const aMatch = a.lang.toLowerCase().startsWith(userLang.slice(0, 2));
      const bMatch = b.lang.toLowerCase().startsWith(userLang.slice(0, 2));
      if (aMatch !== bMatch) return aMatch ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    for (const v of sorted) {
      const opt = document.createElement("option");
      opt.value = v.voiceURI;
      opt.textContent = v.name + " — " + v.lang + (v.default ? " (default)" : "");
      voiceSelect.appendChild(opt);
    }
    if (preferredVoiceURI) {
      voiceSelect.value = preferredVoiceURI;
    }
  }

  function selectedVoice() {
    if (!ttsAvailable || voicesCache.length === 0) return null;
    const uri = voiceSelect.value;
    return voicesCache.find((v) => v.voiceURI === uri) || null;
  }

  voiceSelect.addEventListener("change", () => {
    preferredVoiceURI = voiceSelect.value;
    localStorage.setItem("self_coding_agent_voice", preferredVoiceURI);
  });

  if (ttsAvailable) {
    window.speechSynthesis.onvoiceschanged = loadVoices;
    loadVoices();
  } else {
    voiceSelect.disabled = true;
    ttsToggle.disabled = true;
    voiceModeBtn.disabled = true;
  }

  function shouldSpeak() {
    return ttsAvailable && (voiceMode || ttsToggle.checked);
  }

  function speak(text, onDone) {
    if (!shouldSpeak() || !text) {
      if (onDone) onDone();
      return;
    }
    try {
      window.speechSynthesis.cancel();
    } catch (_) {}
    setState(S.SPEAKING);

    // Each speak() call gets a session token so cancelSpeech() can abort the
    // chunked playback without triggering the "all chunks done" callback.
    const session = { aborted: false };
    activeSpeech = session;

    // SpeechSynthesis chokes on very long utterances in some browsers,
    // so split into ~250-char sentence-ish chunks.
    const chunks = splitForSpeech(text);
    let i = 0;
    const speakNext = () => {
      if (session.aborted) return;
      if (i >= chunks.length) {
        if (onDone) onDone();
        return;
      }
      const u = new SpeechSynthesisUtterance(chunks[i++]);
      const v = selectedVoice();
      if (v) u.voice = v;
      u.rate = 1.0;
      u.pitch = 1.0;
      u.onend = speakNext;
      u.onerror = (e) => {
        console.warn("TTS error", e);
        speakNext();
      };
      window.speechSynthesis.speak(u);
    };
    speakNext();
  }

  function splitForSpeech(text) {
    const max = 250;
    const out = [];
    let buf = "";
    const parts = text.split(/(?<=[.!?])\s+|\n+/);
    for (const p of parts) {
      if ((buf + " " + p).trim().length > max && buf) {
        out.push(buf.trim());
        buf = p;
      } else {
        buf = (buf + " " + p).trim();
      }
    }
    if (buf) out.push(buf);
    return out.length ? out : [text];
  }

  let activeSpeech = null;

  function cancelSpeech() {
    if (activeSpeech) activeSpeech.aborted = true;
    activeSpeech = null;
    if (ttsAvailable) {
      try {
        window.speechSynthesis.cancel();
      } catch (_) {}
    }
  }

  // ---------- Speech recognition (STT) ---------- //

  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  const sttAvailable = !!SR;
  let recognition = null;
  let lastFinalTranscript = "";

  if (sttAvailable) {
    recognition = new SR();
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = navigator.language || "en-US";

    recognition.onstart = () => {
      lastFinalTranscript = "";
      setError("");
    };

    recognition.onresult = (ev) => {
      let finalText = "";
      let interimText = "";
      for (let i = ev.resultIndex; i < ev.results.length; i++) {
        const r = ev.results[i];
        if (r.isFinal) finalText += r[0].transcript;
        else interimText += r[0].transcript;
      }
      if (finalText) {
        lastFinalTranscript = (lastFinalTranscript + " " + finalText).trim();
      }
      if (interimText) {
        stateHint.textContent = "🎙 " + interimText.trim();
      } else if (finalText) {
        stateHint.textContent = "✓ " + finalText.trim();
      }
    };

    recognition.onerror = (ev) => {
      const detail =
        ev && ev.error ? ev.error : "speech recognition unavailable";
      if (detail === "no-speech") {
        // benign in voice mode — let onend decide what to do
      } else if (detail === "not-allowed" || detail === "service-not-allowed") {
        setError(
          "Microphone permission denied. Allow it in browser settings.",
        );
        stopVoiceMode();
      } else if (detail !== "aborted") {
        setError("Voice error: " + detail);
      }
    };

    recognition.onend = () => {
      const text = lastFinalTranscript.trim();
      if (text) {
        consecutiveEmptyTurns = 0;
        sendMessage(text);
      } else if (voiceMode) {
        // No speech captured. Either restart or bail out.
        consecutiveEmptyTurns += 1;
        if (consecutiveEmptyTurns >= 3) {
          stopVoiceMode("No speech detected. Voice mode paused.");
        } else {
          // Brief pause, then re-listen.
          setTimeout(() => {
            if (voiceMode && state !== S.SPEAKING && state !== S.THINKING) {
              startListening();
            }
          }, 300);
        }
      } else if (state === S.LISTENING) {
        setState(S.IDLE);
      }
    };
  } else {
    micBtn.disabled = true;
    voiceModeBtn.disabled = true;
    micBtn.title = voiceModeBtn.title =
      "Voice input requires the Web Speech API (try Chrome or Edge).";
  }

  function startListening() {
    if (!sttAvailable) return;
    if (state === S.LISTENING) return;
    cancelSpeech();
    setState(S.LISTENING);
    try {
      recognition.start();
    } catch (_) {
      // already started
    }
  }

  function stopListening() {
    if (!sttAvailable) return;
    try {
      recognition.stop();
    } catch (_) {}
  }

  // ---------- Voice mode (hands-free loop) ---------- //

  function startVoiceMode() {
    if (!sttAvailable) {
      setError("Voice mode needs the Web Speech API (try Chrome or Edge).");
      return;
    }
    voiceMode = true;
    voiceModeBtn.classList.add("on");
    voiceModeBtn.setAttribute("aria-pressed", "true");
    consecutiveEmptyTurns = 0;
    if (state === S.IDLE) startListening();
  }

  function stopVoiceMode(message) {
    voiceMode = false;
    voiceModeBtn.classList.remove("on");
    voiceModeBtn.setAttribute("aria-pressed", "false");
    if (state === S.LISTENING) stopListening();
    if (state === S.SPEAKING) cancelSpeech();
    setState(S.IDLE, message || "");
  }

  voiceModeBtn.addEventListener("click", () => {
    if (voiceMode) stopVoiceMode();
    else startVoiceMode();
  });

  // Single-shot mic in composer
  micBtn.addEventListener("click", () => {
    if (!sttAvailable) return;
    if (state === S.LISTENING) {
      stopListening();
    } else {
      startListening();
    }
  });

  // Tap orb: toggle listening, or interrupt if speaking
  orb.addEventListener("click", () => {
    if (state === S.SPEAKING) {
      cancelSpeech();
      if (voiceMode) startListening();
      else setState(S.IDLE);
    } else if (state === S.LISTENING) {
      stopListening();
    } else if (state === S.IDLE) {
      startListening();
    }
  });

  interruptBtn.addEventListener("click", () => {
    cancelSpeech();
    if (voiceMode) startListening();
    else setState(S.IDLE);
  });

  // ---------- Networking ---------- //

  async function sendMessage(text) {
    if (!text.trim()) return;
    setError("");
    addMessage("user", text);
    inputEl.value = "";
    autoGrow();
    setState(S.THINKING);

    const thinking = addMessage("assistant thinking", "thinking…");

    try {
      const res = await fetch("/api/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          session_id: sessionId,
          message: text,
        }),
      });
      const data = await res.json().catch(() => ({}));
      thinking.remove();

      if (!res.ok) {
        const msg = (data && data.error) || res.statusText;
        addMessage("assistant", "⚠️ " + msg);
        setError(msg);
        if (voiceMode) {
          // Pause voice mode rather than spam errors aloud.
          stopVoiceMode("Server error — voice mode paused.");
        } else {
          setState(S.IDLE);
        }
        return;
      }

      if (data.session_id) {
        sessionId = data.session_id;
        sessionStorage.setItem(SESSION_KEY, sessionId);
      }

      if (Array.isArray(data.tool_calls)) {
        for (const tc of data.tool_calls) addToolCall(tc);
      }

      const responseText = data.response || "(no response)";
      addMessage("assistant", responseText);

      speak(responseText, () => {
        if (voiceMode) {
          startListening();
        } else {
          setState(S.IDLE);
        }
      });
    } catch (e) {
      thinking.remove();
      const msg = e && e.message ? e.message : String(e);
      addMessage("assistant", "⚠️ network error: " + msg);
      setError(msg);
      if (voiceMode) {
        stopVoiceMode("Network error — voice mode paused.");
      } else {
        setState(S.IDLE);
      }
    } finally {
      inputEl.focus();
    }
  }

  // ---------- Composer ---------- //

  composer.addEventListener("submit", (e) => {
    e.preventDefault();
    const text = inputEl.value;
    if (!text.trim()) return;
    sendMessage(text);
  });

  inputEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendMessage(inputEl.value);
    }
  });
  inputEl.addEventListener("input", autoGrow);

  resetBtn.addEventListener("click", async () => {
    cancelSpeech();
    if (voiceMode) stopVoiceMode();
    if (sessionId) {
      try {
        await fetch("/api/reset", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ session_id: sessionId }),
        });
      } catch (_) {}
    }
    sessionId = "";
    sessionStorage.removeItem(SESSION_KEY);
    messagesEl.innerHTML = "";
    setError("");
    setState(S.IDLE);
  });

  // ---------- Health ---------- //

  fetch("/api/health")
    .then((r) => r.json())
    .then((h) => {
      const bits = [];
      bits.push(h.tools + " tools");
      if (sttAvailable) bits.push("voice in ✓");
      else bits.push("voice in ✗");
      if (ttsAvailable) bits.push("voice out ✓");
      else bits.push("voice out ✗");
      if (h.has_api_key) {
        statusEl.textContent = "ready · " + bits.join(" · ");
        statusEl.className = "status ok";
      } else {
        statusEl.textContent = "no API key · " + bits.join(" · ");
        statusEl.className = "status bad";
        setError(
          "Server has no ANTHROPIC_API_KEY. Export it and restart the agent.",
        );
      }
    })
    .catch(() => {
      statusEl.textContent = "server unreachable";
      statusEl.className = "status bad";
    });

  setState(S.IDLE);
})();
