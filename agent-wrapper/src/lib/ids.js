import crypto from 'node:crypto';

export function newID(prefix) {
  return `${prefix}_${crypto.randomUUID().replace(/-/g, '')}`;
}
