const REDACTED = '[redacted]';
const MAX_LOG_STRING_LENGTH = 4096;

const SENSITIVE_KEY_PATTERN = /(^|[_-])(authorization|api[_-]?key|token|secret|password|passwd|pwd|credential|private[_-]?key|client[_-]?secret|access[_-]?token|refresh[_-]?token|signature|sig)($|[_-])/i;

export function logInfo(msg, fields = {}) {
  writeLog('info', msg, fields);
}

export function logWarn(msg, fields = {}) {
  writeLog('warn', msg, fields);
}

export function logError(msg, fields = {}) {
  writeLog('error', msg, fields);
}

export function safeErrorMessage(error) {
  return sanitizeLogString(error instanceof Error ? error.message : String(error ?? ''));
}

export function summarizePendingActions(actions) {
  const list = Array.isArray(actions) ? actions : [];
  return {
    pending_action_count: list.length,
    pending_action_ids: list.map((action) => firstNonEmptyString(action?.id, action?.tool_use_id, action?.custom_tool_use_id)).filter(Boolean),
    pending_action_kinds: list.map((action) => String(action?.kind ?? '')).filter(Boolean),
  };
}

export function sanitizeLogFields(fields) {
  return sanitizeLogValue(fields);
}

function writeLog(level, msg, fields) {
  const payload = sanitizeLogValue({
    level,
    msg,
    ...(fields && typeof fields === 'object' && !Array.isArray(fields) ? fields : {}),
  });
  console.log(JSON.stringify(payload));
}

function sanitizeLogValue(value, key = '', depth = 0) {
  if (value == null || typeof value === 'number' || typeof value === 'boolean') {
    return value;
  }
  if (isSensitiveKey(key)) {
    return REDACTED;
  }
  if (typeof value === 'string') {
    if (isURLKey(key)) {
      return sanitizeURL(value);
    }
    return sanitizeLogString(value);
  }
  if (Array.isArray(value)) {
    if (depth >= 8) {
      return '[truncated]';
    }
    return value.map((entry) => sanitizeLogValue(entry, key, depth + 1));
  }
  if (typeof value === 'object') {
    if (depth >= 8) {
      return '[truncated]';
    }
    const sanitized = {};
    for (const [entryKey, entryValue] of Object.entries(value)) {
      sanitized[entryKey] = sanitizeLogValue(entryValue, entryKey, depth + 1);
    }
    return sanitized;
  }
  return sanitizeLogString(String(value));
}

function sanitizeURL(value) {
  const sanitized = truncateLogString(String(value ?? ''));
  return sanitizeURLLiteral(sanitized);
}

function sanitizeURLLiteral(value) {
  try {
    const url = new URL(value);
    if (url.username) {
      url.username = REDACTED;
    }
    if (url.password) {
      url.password = REDACTED;
    }
    for (const key of Array.from(url.searchParams.keys())) {
      if (isSensitiveKey(key)) {
        url.searchParams.set(key, REDACTED);
      }
    }
    return truncateLogString(url.toString());
  } catch {
    return value;
  }
}

function sanitizeLogString(value) {
  return sanitizeEmbeddedURLs(redactInlineSecrets(truncateLogString(String(value ?? ''))));
}

function sanitizeEmbeddedURLs(value) {
  return value.replace(/\bhttps?:\/\/[^\s"'<>]+/gi, (match) => sanitizeURLLiteral(match));
}

function redactInlineSecrets(value) {
  return value
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+/=-]+/gi, `$1${REDACTED}`)
    .replace(/\b(Authorization\s*[:=]\s*Basic\s+)[A-Za-z0-9._~+/=-]+/gi, `$1${REDACTED}`)
    .replace(/\b(sk-[A-Za-z0-9_-]{8,})\b/g, REDACTED)
    .replace(/((?:api[_-]?key|token|secret|password|passwd|pwd|credential|private[_-]?key|client[_-]?secret|access[_-]?token|refresh[_-]?token|signature|sig)["']?\s*[:=]\s*["']?)([^"'\s,&}]+)/gi, `$1${REDACTED}`);
}

function truncateLogString(value) {
  if (value.length <= MAX_LOG_STRING_LENGTH) {
    return value;
  }
  return `${value.slice(0, MAX_LOG_STRING_LENGTH)}...[truncated]`;
}

function isSensitiveKey(key) {
  const normalized = String(key ?? '');
  if (!normalized || normalized.startsWith('has_') || normalized.startsWith('has-')) {
    return false;
  }
  return SENSITIVE_KEY_PATTERN.test(normalized);
}

function isURLKey(key) {
  return /(^|[_-])(url|uri|endpoint)($|[_-])/i.test(String(key ?? ''));
}

function firstNonEmptyString(...values) {
  for (const value of values) {
    const normalized = String(value ?? '').trim();
    if (normalized !== '') {
      return normalized;
    }
  }
  return '';
}
