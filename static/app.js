(() => {
  "use strict";

  const messagesEl = document.getElementById("messages");
  const inputEl = document.getElementById("input");
  const sendBtn = document.getElementById("send");
  const micBtn = document.getElementById("mic");
  const ttsToggle = document.getElementById("tts");
  const resetBtn = document.getElementById("reset");
  const statusEl = document.getElementById("status");
  const errorEl = document.getElementById("error");
  const composer = document.getElementById("composer");

  const SESSION_KEY = "self_coding_agent_session_id";
  let sessionId = sessionStorage.getItem(SESSION_KEY) || "";
  let pending = false;

  // ---------- UI helpers ---------- //

  function setStatus(text, kind) {
    statusEl.textContent = text;
    statusEl.className = "status " + (kind || "");
  }

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

  function setPending(yes) {
    pending = yes;
    sendBtn.disabled = yes;
    inputEl.disabled = yes;
  }

  // ---------- Text-to-speech ---------- //

  function speak(text) {
    if (!ttsToggle.checked) return;
    if (!("speechSynthesis" in window)) return;
    if (!text) return;
    try {
      window.speechSynthesis.cancel();
      const u = new SpeechSynthesisUtterance(text);
      u.rate = 1.0;
      u.pitch = 1.0;
      window.speechSynthesis.speak(u);
    } catch (e) {
      console.warn("TTS failed:", e);
    }
  }

  // ---------- Speech-to-text ---------- //

  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  let recognition = null;
  let recognizing = false;

  if (SR) {
    recognition = new SR();
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = navigator.language || "en-US";

    recognition.onstart = () => {
      recognizing = true;
      micBtn.classList.add("recording");
      micBtn.setAttribute("aria-pressed", "true");
      setError("");
    };
    recognition.onend = () => {
      recognizing = false;
      micBtn.classList.remove("recording");
      micBtn.setAttribute("aria-pressed", "false");
    };
    recognition.onerror = (ev) => {
      recognizing = false;
      micBtn.classList.remove("recording");
      const detail =
        ev && ev.error ? ev.error : "speech recognition unavailable";
      if (detail === "no-speech") {
        setError("Didn't catch that. Try again.");
      } else if (detail === "not-allowed" || detail === "service-not-allowed") {
        setError(
          "Microphone permission denied. Allow it in your browser settings.",
        );
      } else {
        setError("Voice error: " + detail);
      }
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
        const existing = inputEl.value.trim();
        inputEl.value = (existing ? existing + " " : "") + finalText.trim();
        autoGrow();
      } else if (interimText) {
        inputEl.placeholder = "🎙️ " + interimText.trim();
      }
    };

    micBtn.addEventListener("click", () => {
      if (recognizing) {
        recognition.stop();
        return;
      }
      try {
        recognition.start();
      } catch (e) {
        // .start() throws if already started
        console.warn(e);
      }
    });
  } else {
    micBtn.disabled = true;
    micBtn.title =
      "Voice input requires the Web Speech API (try Chrome or Edge).";
  }

  // ---------- Networking ---------- //

  async function sendMessage(text) {
    if (!text.trim() || pending) return;
    setError("");
    addMessage("user", text);
    inputEl.value = "";
    autoGrow();
    setPending(true);

    const thinking = addMessage("assistant thinking", "thinking…");

    try {
      const res = await fetch("/api/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session_id: sessionId, message: text }),
      });
      const data = await res.json().catch(() => ({}));
      thinking.remove();

      if (!res.ok) {
        const msg = (data && data.error) || res.statusText;
        addMessage("assistant", "⚠️ " + msg);
        setError(msg);
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
      speak(responseText);
    } catch (e) {
      thinking.remove();
      const msg = e && e.message ? e.message : String(e);
      addMessage("assistant", "⚠️ network error: " + msg);
      setError(msg);
    } finally {
      setPending(false);
      inputEl.focus();
    }
  }

  composer.addEventListener("submit", (e) => {
    e.preventDefault();
    sendMessage(inputEl.value);
  });

  inputEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendMessage(inputEl.value);
    }
  });
  inputEl.addEventListener("input", autoGrow);

  resetBtn.addEventListener("click", async () => {
    if (!sessionId) {
      messagesEl.innerHTML = "";
      return;
    }
    try {
      await fetch("/api/reset", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session_id: sessionId }),
      });
    } catch (e) {
      console.warn(e);
    }
    sessionId = "";
    sessionStorage.removeItem(SESSION_KEY);
    messagesEl.innerHTML = "";
    setError("");
  });

  // ---------- Health check on load ---------- //

  fetch("/api/health")
    .then((r) => r.json())
    .then((h) => {
      if (h.has_api_key) {
        setStatus("ready · " + h.tools + " tools", "ok");
      } else {
        setStatus("no ANTHROPIC_API_KEY set", "bad");
        setError(
          "Server has no ANTHROPIC_API_KEY. Export it and restart the agent.",
        );
      }
    })
    .catch(() => setStatus("server unreachable", "bad"));
})();
