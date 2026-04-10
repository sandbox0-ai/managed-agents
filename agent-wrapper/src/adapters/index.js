import { ClaudeRuntime } from './claude.js';

export function createAdapterRegistry() {
  return {
    claude: new ClaudeRuntime(),
  };
}
