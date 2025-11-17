// Rocket.Chat Incoming Webhook Script: Deduplication + TTL + Max Cache
// - Dedupes alerts pro key (alertname|job)
// - Forward firing only once per DEDUP_WINDOW_MS
// - Forward resolved only if firing was sent
// - Cache automatically cleans up old entries
// - Memory-safe with MAX_CACHE_ENTRIES limit

class Script {
  process_incoming_request({ request }) {
    const payload = request.content || {};
    const alerts = payload.alerts || [];

    // ===== CONFIGURATION =====
    const DEDUP_WINDOW_MS = 10 * 60 * 1000; // 10 min dedupe window
    const MAX_CACHE_ENTRIES = 10000;        // max entries to avoid memory bloat

    // ===== IN-MEMORY STORE =====
    if (!global.__amDedupStore) {
      // Map: key -> { sent: boolean, ts: timestamp }
      global.__amDedupStore = new Map();
    }
    const store = global.__amDedupStore;

    // ===== HELPER FUNCTIONS =====
    function cleanupStore() {
      if (store.size <= MAX_CACHE_ENTRIES) return;
      // remove oldest entries
      const entries = Array.from(store.entries()).sort((a, b) => a[1].ts - b[1].ts);
      const toDelete = Math.ceil(store.size - MAX_CACHE_ENTRIES);
      for (let i = 0; i < toDelete; i++) {
        store.delete(entries[i][0]);
      }
    }

    function buildKey(alert) {
      const labels = alert.labels || {};
      const name = labels.alertname || labels.rule || "unknown-alert";
      const job = labels.job || labels.instance || labels.namespace || "unknown-job";
      return `${name}|${job}`;
    }

    const now = Date.now();
    const toForward = [];

    for (const alert of alerts) {
      const status = (alert.status || "").toLowerCase() || (payload.status || "").toLowerCase();
      const key = buildKey(alert);
      const entry = store.get(key);

      if (status === "firing") {
        if (entry && entry.sent && (now - entry.ts) < DEDUP_WINDOW_MS) {
          // duplicate firing → suppress
          continue;
        } else {
          toForward.push({ alert, key, status: "firing" });
          store.set(key, { sent: true, ts: now });
          cleanupStore();
        }
      } else if (status === "resolved") {
        if (entry && entry.sent) {
          // only forward resolved if firing was sent
          toForward.push({ alert, key, status: "resolved" });
          store.delete(key); // remove from cache to allow future firings
        } else {
          continue; // suppressed firing → suppress resolved
        }
      } else {
        // unknown status → forward conservatively
        toForward.push({ alert, key, status: status || "unknown" });
        store.set(key, { sent: true, ts: now });
        cleanupStore();
      }
    }

    if (toForward.length === 0) return;

    // ===== BUILD MESSAGE =====
    const attachments = toForward.map(item => {
      const a = item.alert;
      const labels = a.labels || {};
      const annotations = a.annotations || {};
      const severity = labels.severity || annotations.severity || "unknown";

      const color =
        severity === "critical" || severity === "fatal" ? "#FF0000" :
        severity === "warning" ? "#FFA500" :
        "#36A64F";

      const title = `${severity === "critical" ? "🔥" : severity === "warning" ? "⚠️" : "ℹ️"} ${labels.alertname || "Alert"}`;
      let text = `**Status:** ${item.status}\n`;
      if (labels.job) text += `**Job:** ${labels.job}\n`;
      if (labels.site) text += `**Site:** ${labels.site}\n`;
      if (labels.instance) text += `**Instance:** ${labels.instance}\n`;
      if (annotations.summary) text += `**Summary:** ${annotations.summary}\n`;
      if (annotations.description) text += `**Description:** ${annotations.description}\n`;
      if (a.startsAt) text += `**Starts At:** ${a.startsAt}\n`;
      if (a.endsAt && item.status === "resolved") text += `**Ends At:** ${a.endsAt}\n`;

      return { color, title, text, ts: new Date(a.startsAt || now) };
    });

    return {
      content: {
        username: "Alertmanager (dedupe)",
        icon_emoji: ":rotating_light:",
        attachments
      }
    };
  }
}
