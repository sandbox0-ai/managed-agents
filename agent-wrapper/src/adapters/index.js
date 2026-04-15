import { ClaudeRuntime } from './claude.js';
import { CodexRuntime } from './codex.js';
import { RuntimeRouter } from './runtime.js';

export {
  AgentRuntime,
  RuntimeRouter,
  finalStatusEventForSessionError,
  normalizeVendor,
  providerErrorEventForText,
  sessionErrorEventForError,
} from './runtime.js';
export { ClaudeRuntime } from './claude.js';
export { CodexRuntime } from './codex.js';

export function createAdapterRegistry() {
  return {
    claude: new ClaudeRuntime(),
    codex: new CodexRuntime(),
  };
}

export function createDefaultRuntime({ runtimes = createAdapterRegistry(), defaultVendor } = {}) {
  return new RuntimeRouter(runtimes, { defaultVendor });
}
