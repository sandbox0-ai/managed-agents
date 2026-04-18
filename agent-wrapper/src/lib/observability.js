import crypto from 'node:crypto';
import { logInfo, logWarn, safeErrorMessage } from './log.js';

export function startOperation(name, fields = {}) {
  const startedAt = Date.now();
  const baseFields = normalizeFields(fields);
  const operationID = crypto.randomUUID();
  let ended = false;

  logInfo('wrapper operation started', {
    operation: name,
    operation_id: operationID,
    ...baseFields,
  });

  return {
    id: operationID,
    addFields(extraFields = {}) {
      Object.assign(baseFields, normalizeFields(extraFields));
    },
    async runPhase(phase, fn, fieldsForPhase = {}) {
      const phaseStartedAt = Date.now();
      try {
        const result = await fn();
        logInfo('wrapper phase completed', {
          operation: name,
          operation_id: operationID,
          phase,
          status: 'success',
          duration_ms: Date.now() - phaseStartedAt,
          ...baseFields,
          ...normalizeFields(fieldsForPhase),
        });
        return result;
      } catch (error) {
        logWarn('wrapper phase completed', {
          operation: name,
          operation_id: operationID,
          phase,
          status: 'error',
          duration_ms: Date.now() - phaseStartedAt,
          ...baseFields,
          ...normalizeFields(fieldsForPhase),
          error: safeErrorMessage(error),
        });
        throw error;
      }
    },
    end(error = null, endFields = {}) {
      if (ended) {
        return;
      }
      ended = true;
      const payload = {
        operation: name,
        operation_id: operationID,
        status: error ? 'error' : 'success',
        duration_ms: Date.now() - startedAt,
        ...baseFields,
        ...normalizeFields(endFields),
      };
      if (error) {
        logWarn('wrapper operation completed', {
          ...payload,
          error: safeErrorMessage(error),
        });
        return;
      }
      logInfo('wrapper operation completed', payload);
    },
  };
}

export function requestFields(req, route, extraFields = {}) {
  const traceparent = firstHeaderValue(req, ['traceparent']);
  return normalizeFields({
    route,
    method: req?.method ?? null,
    path: safePathname(req?.url),
    request_id: firstHeaderValue(req, ['x-request-id', 'x-correlation-id']),
    traceparent,
    trace_id: traceIDFromTraceparent(traceparent),
    user_agent: firstHeaderValue(req, ['user-agent']),
    ...extraFields,
  });
}

function normalizeFields(fields) {
  if (!fields || typeof fields !== 'object' || Array.isArray(fields)) {
    return {};
  }
  return fields;
}

function firstHeaderValue(req, names) {
  for (const name of names) {
    const rawValue = req?.headers?.[name];
    if (Array.isArray(rawValue)) {
      for (const value of rawValue) {
        const normalized = String(value ?? '').trim();
        if (normalized) {
          return normalized;
        }
      }
      continue;
    }
    const normalized = String(rawValue ?? '').trim();
    if (normalized) {
      return normalized;
    }
  }
  return null;
}

function safePathname(rawURL) {
  try {
    return new URL(rawURL ?? '/', 'http://agent-wrapper.local').pathname;
  } catch {
    return String(rawURL ?? '');
  }
}

function traceIDFromTraceparent(traceparent) {
  const value = String(traceparent ?? '').trim();
  const match = value.match(/^[\da-f]{2}-([\da-f]{32})-[\da-f]{16}-[\da-f]{2}$/i);
  return match ? match[1].toLowerCase() : null;
}
